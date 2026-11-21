package mail

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
// A message may go from the system domain, but only when it is mailgoose's
// own mail (KindSystem) or a double opt-in confirmation (KindConfirm); a
// test send or a campaign from it, or a message with no kind or another one,
// is refused, so customer content never goes out as the system domain
// (MG-90). Otherwise it may go from a domain that Verified says its account
// has verified in this environment's database. Only an environment's own
// domains service can have verified a domain there, since SES holds one
// identity per name in the account and adopting another environment's
// identity is refused.
//
// With a shared sending domain (MG-85), a message may also go from it, but
// only as its own account's sending slug and with every Reply-To one the
// account has verified: SharedSender says so.
type Guard struct {
	Sender
	// System is this environment's system sending domain
	// (SYSTEM_SENDING_DOMAIN). Mail from exactly that domain is let through
	// when its Kind is KindSystem or KindConfirm, and refused otherwise.
	System string
	// Verified returns nil when the domain of from is, or is a subdomain of,
	// a domain accountID has verified in this environment, and an error
	// otherwise. It's domains.Service.RequireVerifiedFrom.
	Verified func(ctx context.Context, accountID, from string) error
	// Shared is this environment's shared sending domain
	// (SHARED_SENDING_DOMAIN); empty means none, and mail from it is refused
	// like any other domain the account hasn't verified.
	Shared string
	// SharedSender returns nil when local, the From's local part on the
	// shared domain, is accountID's own sending slug and every replyTo (at
	// least one; the guard refuses a message with none)
	// address is a verified sender of accountID or on one of its verified
	// domains, and an error otherwise. It's senders.Resolver.CheckShared.
	SharedSender func(ctx context.Context, accountID, local string, replyTo []string) error
	// TTL is how long a domain that passed stays trusted without asking
	// Verified again, so a campaign doesn't query the database for every
	// copy. Zero means one minute; a refusal is never remembered.
	TTL time.Duration
	// Now is the clock; nil means time.Now.
	Now func() time.Time

	mu     sync.Mutex
	passed map[guardKey]time.Time
}

// guardKey is what a pass was for. A message on the shared domain passes
// for its exact From and Reply-To addresses (sender), so a pass never lets
// through another account's slug or a Reply-To that wasn't checked.
type guardKey struct{ account, domain, sender string }

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
		if systemKind(m.Kind) {
			return nil
		}
		return fmt.Errorf("%w: %s from the system domain %s", ErrNotThisEnvironment, kindName(m.Kind), domain)
	}
	shared := g.Shared != "" && domain == strings.ToLower(strings.TrimSuffix(g.Shared, "."))
	if m.AccountID == "" || (!shared && g.Verified == nil) || (shared && g.SharedSender == nil) {
		return fmt.Errorf("%w: %s", ErrNotThisEnvironment, domain)
	}
	key := guardKey{account: m.AccountID, domain: domain}
	local := m.From.Email[:at]
	var replyTo []string
	if shared {
		for _, r := range m.ReplyTo {
			replyTo = append(replyTo, strings.ToLower(strings.TrimSpace(r.Email)))
		}
		// Replies go to the address the account verified, so a message on
		// the shared domain without one isn't one mailgoose made.
		if len(replyTo) == 0 {
			return fmt.Errorf("%w: %s with no Reply-To", ErrNotThisEnvironment, domain)
		}
		slices.Sort(replyTo)
		key.sender = strings.ToLower(local) + "\x00" + strings.Join(replyTo, "\x00")
	}
	now := g.now()
	g.mu.Lock()
	until, ok := g.passed[key]
	g.mu.Unlock()
	if ok && now.Before(until) {
		return nil
	}
	var err error
	if shared {
		err = g.SharedSender(ctx, m.AccountID, local, replyTo)
	} else {
		err = g.Verified(ctx, m.AccountID, m.From.Email)
	}
	if err != nil {
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

// systemKind reports whether mail of kind k may go from the system domain:
// mailgoose's own mail and double opt-in confirmations. Test sends and
// campaigns carry customer content, and an unset or unknown kind isn't known
// to be either, so none of them may.
func systemKind(k Kind) bool {
	return k == KindSystem || k == KindConfirm
}

// kindName describes mail of kind k in a refusal, saying so when it's unset.
func kindName(k Kind) string {
	if k == "" {
		return "mail with no kind"
	}
	return fmt.Sprintf("%q mail", string(k))
}

func (g *Guard) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}
