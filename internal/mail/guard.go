package mail

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ErrNotThisEnvironment is Guard's refusal: the From address isn't one this
// environment may send as.
var ErrNotThisEnvironment = errors.New("from address is not a sending identity of this environment")

// Guard checks every message's From address before Sender sends it, and
// refuses one this environment doesn't own. Staging and prod share one AWS
// account, and SES ignores tag conditions on sends, so each environment's
// role may send as any identity in the account (decisions, 2026-11-20).
// This check is what keeps staging from sending as a prod customer's domain,
// and the other way round.
//
// A message may go from the system domain, mailgoose's own mail, or from a
// domain that Verified says its account has verified in this environment's
// database. Only an environment's own domains service can have verified a
// domain there, since SES holds one identity per name in the account and
// adopting another environment's identity is refused.
type Guard struct {
	Sender
	// System is this environment's system sending domain
	// (SYSTEM_SENDING_DOMAIN). Mail from exactly that domain is let through.
	System string
	// Verified returns nil when the domain of from is, or is a subdomain of,
	// a domain accountID has verified in this environment, and an error
	// otherwise. It's domains.Service.RequireVerifiedFrom.
	Verified func(ctx context.Context, accountID, from string) error
	// TTL is how long a domain that passed stays trusted without asking
	// Verified again, so a campaign doesn't query the database for every
	// copy. Zero means one minute; a refusal is never remembered.
	TTL time.Duration
	// Now is the clock; nil means time.Now.
	Now func() time.Time

	mu     sync.Mutex
	passed map[guardKey]time.Time
}

type guardKey struct{ account, domain string }

// guardCacheMax bounds the remembered passes; past it the cache starts over.
const guardCacheMax = 10_000

// Send implements Sender.
func (g *Guard) Send(ctx context.Context, m Message) (string, error) {
	if err := g.Check(ctx, m); err != nil {
		return "", err
	}
	return g.Sender.Send(ctx, m)
}

// Check returns nil when m's From address belongs to this environment, and
// an error wrapping ErrNotThisEnvironment when it doesn't.
func (g *Guard) Check(ctx context.Context, m Message) error {
	at := strings.LastIndexByte(m.From.Email, '@')
	if at < 0 {
		return fmt.Errorf("%w: %q", ErrNotThisEnvironment, m.From.Email)
	}
	domain := strings.ToLower(strings.TrimSuffix(m.From.Email[at+1:], "."))
	if g.System != "" && domain == strings.ToLower(strings.TrimSuffix(g.System, ".")) {
		return nil
	}
	if m.AccountID == "" || g.Verified == nil {
		return fmt.Errorf("%w: %s", ErrNotThisEnvironment, domain)
	}
	key := guardKey{m.AccountID, domain}
	now := g.now()
	g.mu.Lock()
	until, ok := g.passed[key]
	g.mu.Unlock()
	if ok && now.Before(until) {
		return nil
	}
	if err := g.Verified(ctx, m.AccountID, m.From.Email); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %s (%w)", ErrNotThisEnvironment, domain, err)
	}
	ttl := g.TTL
	if ttl <= 0 {
		ttl = time.Minute
	}
	g.mu.Lock()
	if g.passed == nil || len(g.passed) >= guardCacheMax {
		g.passed = make(map[guardKey]time.Time)
	}
	g.passed[key] = now.Add(ttl)
	g.mu.Unlock()
	return nil
}

func (g *Guard) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}
