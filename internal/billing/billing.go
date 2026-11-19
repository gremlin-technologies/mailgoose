// Package billing runs the paid plan and sells credit packs through Stripe
// (BIL-3 to BIL-5, BIL-7).
//
// The paid plan is a Stripe subscription, $5 a month or $50 a year for each
// plan unit (PlanCheckout); Stripe's subscription events (HandleWebhook)
// make the account paid or free again, and pause sending while a renewal
// payment is past due. A paid account buys credit packs (Checkout); the
// webhook adds them as a purchase lot that lasts 24 months.
//
// Prices live in config (CREDIT_PACKS, PLAN_PRICES), not in Stripe: each
// Checkout session carries its price, so no Stripe products or prices need
// setting up. Stripe Tax works out sales tax at checkout, and Stripe emails
// receipts to the account's address.
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

// SubscriptionRequest is what the paid plan's Checkout session is made from.
type SubscriptionRequest struct {
	AccountID string
	Email     string
	// CustomerID is the account's Stripe customer from an earlier
	// subscription, if any; Checkout makes one otherwise.
	CustomerID string
	// Interval is "month" or "year".
	Interval       string
	UnitCents      int64
	Units          int
	SuccessURL     string
	CancelURL      string
	IdempotencyKey string
}

// Stripe is the part of Stripe's API billing uses; Client is the real one.
type Stripe interface {
	CreateCheckoutSession(ctx context.Context, r SessionRequest) (*Session, error)
	CreateSubscriptionCheckout(ctx context.Context, r SubscriptionRequest) (*Session, error)
	CreatePortalSession(ctx context.Context, customerID, returnURL string) (string, error)
	SetSubscriptionUnits(ctx context.Context, subscriptionID, itemID string, units int, idempotencyKey string) error
	CancelSubscription(ctx context.Context, subscriptionID string) error
	// GetSubscription returns a subscription as Stripe has it now, as the
	// JSON of its subscription object.
	GetSubscription(ctx context.Context, subscriptionID string) ([]byte, error)
}

// ErrNotConfigured means there are no Stripe keys.
var ErrNotConfigured = errors.New("billing isn't set up on this server")

// ErrStripeUnreachable means Stripe didn't answer, or answered with
// something billing couldn't read.
var ErrStripeUnreachable = errors.New("stripe didn't answer")

// ErrPlanRequired means a free account tried to buy credits: packs are for
// the paid plan.
var ErrPlanRequired = errors.New("buying credits needs the paid plan")

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
	// Prices are the paid plan's prices; zero means DefaultPrices.
	Prices Prices
	// BaseURL is the app's public URL, where Checkout sends people back.
	BaseURL string
	Log     *slog.Logger
	// Alert, when set, tells mailgoose's operators about a payment someone
	// has to sort out by hand in Stripe (a refund or a cancellation). Each
	// case is logged as an error as well.
	Alert func(ctx context.Context, subject, text string)
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

func (s *Service) alert(ctx context.Context, subject, text string) {
	if s.Alert != nil {
		s.Alert(ctx, subject, text)
	}
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
		var plan string
		if err := s.DB.QueryRow(ctx, `SELECT plan FROM accounts WHERE id = $1`, accountID).Scan(&plan); err != nil {
			return nil, p, err
		}
		if plan != "paid" {
			return nil, p, ErrPlanRequired
		}
		if email, err = s.email(ctx, accountID); err != nil {
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

// email is the account's address, where Stripe sends receipts: one person
// per account (MVP).
func (s *Service) email(ctx context.Context, accountID string) (string, error) {
	var email string
	err := s.DB.QueryRow(ctx, `SELECT email FROM users WHERE account_id = $1 ORDER BY created_at LIMIT 1`, accountID).Scan(&email)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return email, err
}

// purchase adds a paid session's credits, once per session (BIL-3).
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
	if _, err := credits.Purchase(ctx, tx, accountID, amount, ref, now); err != nil {
		return false, err
	}
	return true, nil
}
