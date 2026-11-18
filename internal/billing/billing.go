// Package billing sells credit packs through Stripe Checkout (BIL-3,
// BIL-5, BIL-7). Checkout makes a Checkout session for a pack and returns
// its URL; the person pays in the browser, and Stripe's webhook
// (HandleWebhook) adds the credits, as a purchase lot that lasts 24 months,
// and makes the account paid.
//
// Prices live in config (CREDIT_PACKS), not in Stripe: each session carries
// its pack's price, so no Stripe products or prices need setting up. Stripe
// Tax works out sales tax at checkout, and Stripe emails the receipt to the
// account's address.
package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/credits"
)

// Pack is a credit pack: Credits for PriceCents US cents.
type Pack struct {
	Name       string `json:"name"`
	Credits    int64  `json:"credits"`
	PriceCents int64  `json:"price_cents"`
}

// DefaultPacks are the decided packs (BIL-3).
var DefaultPacks = []Pack{
	{Name: "10k", Credits: 10_000, PriceCents: 2_000},
	{Name: "50k", Credits: 50_000, PriceCents: 7_500},
	{Name: "200k", Credits: 200_000, PriceCents: 24_000},
	{Name: "1m", Credits: 1_000_000, PriceCents: 90_000},
}

// ParsePacks reads CREDIT_PACKS: "name=credits:cents" pairs separated by
// commas, such as "10k=10000:2000,50k=50000:7500". Empty means
// DefaultPacks.
func ParsePacks(s string) ([]Pack, error) {
	if strings.TrimSpace(s) == "" {
		return DefaultPacks, nil
	}
	var out []Pack
	seen := map[string]bool{}
	for _, part := range strings.Split(s, ",") {
		name, rest, ok1 := strings.Cut(strings.TrimSpace(part), "=")
		n, cents, ok2 := strings.Cut(rest, ":")
		credits, err1 := strconv.ParseInt(n, 10, 64)
		price, err2 := strconv.ParseInt(cents, 10, 64)
		name = strings.ToLower(strings.TrimSpace(name))
		if !ok1 || !ok2 || err1 != nil || err2 != nil || name == "" || credits <= 0 || price <= 0 || seen[name] {
			return nil, fmt.Errorf("CREDIT_PACKS: %q isn't name=credits:cents", part)
		}
		seen[name] = true
		out = append(out, Pack{Name: name, Credits: credits, PriceCents: price})
	}
	return out, nil
}

// Session is a Checkout session to pay at.
type Session struct {
	ID        string
	URL       string
	ExpiresAt time.Time
}

// SessionRequest is what a Checkout session is made from.
type SessionRequest struct {
	AccountID string
	// Email is where Stripe sends the receipt; empty lets the person type one.
	Email      string
	Pack       Pack
	SuccessURL string
	CancelURL  string
	// IdempotencyKey makes a retried request return the same session.
	IdempotencyKey string
}

// Stripe makes Checkout sessions; Client is the real one.
type Stripe interface {
	CreateCheckoutSession(ctx context.Context, r SessionRequest) (*Session, error)
}

// ErrNotConfigured means there are no Stripe keys.
var ErrNotConfigured = errors.New("buying credits isn't set up on this server")

// UnknownPackError means there's no pack by that name.
type UnknownPackError struct {
	Name  string
	Packs []Pack
}

func (e *UnknownPackError) Error() string {
	names := make([]string, len(e.Packs))
	for i, p := range e.Packs {
		names[i] = p.Name
	}
	return fmt.Sprintf("there's no %q pack; the packs are %s", e.Name, strings.Join(names, ", "))
}

// Service sells credits.
type Service struct {
	DB *pgxpool.Pool
	// Stripe is nil when no secret key is configured.
	Stripe Stripe
	// WebhookSecret checks Stripe's webhook signatures.
	WebhookSecret string
	Packs         []Pack
	// BaseURL is the app's public URL, where Checkout sends people back.
	BaseURL string
	Log     *slog.Logger
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.New(slog.DiscardHandler)
}

// Pack finds a pack by name, ignoring case.
func (s *Service) Pack(name string) (Pack, error) {
	for _, p := range s.Packs {
		if strings.EqualFold(p.Name, strings.TrimSpace(name)) {
			return p, nil
		}
	}
	return Pack{}, &UnknownPackError{Name: name, Packs: s.Packs}
}

// Checkout is a Checkout session for an account to buy a pack.
// idempotencyKey, when set, makes a retry return the same session.
func (s *Service) Checkout(ctx context.Context, accountID, pack, idempotencyKey string) (*Session, Pack, error) {
	p, err := s.Pack(pack)
	if err != nil {
		return nil, Pack{}, err
	}
	if s.Stripe == nil {
		return nil, p, ErrNotConfigured
	}
	email := ""
	if s.DB != nil {
		// One person per account (MVP): the receipt goes to them.
		if err := s.DB.QueryRow(ctx, `SELECT email FROM users WHERE account_id = $1 ORDER BY created_at LIMIT 1`, accountID).
			Scan(&email); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, p, err
		}
	}
	base := strings.TrimRight(s.BaseURL, "/")
	sess, err := s.Stripe.CreateCheckoutSession(ctx, SessionRequest{
		AccountID:      accountID,
		Email:          email,
		Pack:           p,
		SuccessURL:     base + "/credits?checkout=done",
		CancelURL:      base + "/credits?checkout=cancelled",
		IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		return nil, p, err
	}
	return sess, p, nil
}

// purchase adds a paid session's credits and makes the account paid, once
// per session (BIL-3, BIL-5).
func purchase(ctx context.Context, tx pgx.Tx, accountID, sessionID string, amount int64, now time.Time) (bool, error) {
	ref := "stripe:" + sessionID
	// Lock the account first, so two events for one session take turns; the
	// unique index on purchase refs backs this up.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM accounts WHERE id = $1 FOR UPDATE`, accountID); err != nil {
		return false, err
	}
	var done bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM credit_transactions WHERE account_id = $1 AND type = 'purchase' AND ref = $2)`,
		accountID, ref).Scan(&done); err != nil {
		return false, err
	}
	if done {
		return false, nil
	}
	// Paid first, so the catch-up inside Purchase gives no free grant.
	if _, err := tx.Exec(ctx, `UPDATE accounts SET plan = 'paid', paid_since = coalesce(paid_since, $2), updated_at = $2 WHERE id = $1`,
		accountID, now); err != nil {
		return false, err
	}
	if _, err := credits.Purchase(ctx, tx, accountID, amount, ref, now); err != nil {
		return false, err
	}
	return true, nil
}
