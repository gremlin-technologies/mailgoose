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

	"github.com/gremlin-technologies/mailgoose/internal/credits"
)

// The paid plan (BIL-4, changed Nov 18): a card subscription. One plan unit
// covers IncludedContacts counted contacts; each further ContactsPerUnit,
// or part of one, is another unit.
const (
	IncludedContacts = 2_500
	ContactsPerUnit  = 10_000
)

// Billing intervals.
const (
	Monthly = "month"
	Yearly  = "year"
)

// SubscriptionPastDue is the pause code while a renewal payment is due.
const SubscriptionPastDue = "subscription_past_due"

// Prices are what one plan unit costs, in US cents, per month and per year.
type Prices struct {
	MonthCents int64
	YearCents  int64
}

// DefaultPrices are the decided prices: $5 a month, or $50 a year.
var DefaultPrices = Prices{MonthCents: 500, YearCents: 5_000}

// ParsePrices reads PLAN_PRICES, "month:500,year:5000". Empty means
// DefaultPrices.
func ParsePrices(s string) (Prices, error) {
	if strings.TrimSpace(s) == "" {
		return DefaultPrices, nil
	}
	var p Prices
	for _, part := range strings.Split(s, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), ":")
		cents, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil || cents <= 0 {
			return Prices{}, fmt.Errorf("PLAN_PRICES: %q isn't interval:cents", part)
		}
		switch strings.TrimSpace(k) {
		case Monthly:
			p.MonthCents = cents
		case Yearly:
			p.YearCents = cents
		default:
			return Prices{}, fmt.Errorf("PLAN_PRICES: %q isn't month or year", k)
		}
	}
	if p.MonthCents == 0 || p.YearCents == 0 {
		return Prices{}, errors.New("PLAN_PRICES needs both month and year")
	}
	return p, nil
}

// Cents is one unit's price for an interval.
func (p Prices) Cents(interval string) int64 {
	if interval == Yearly {
		return p.YearCents
	}
	return p.MonthCents
}

func (s *Service) prices() Prices {
	if s.Prices.MonthCents == 0 {
		return DefaultPrices
	}
	return s.Prices
}

// PlanUnits is how many plan units an account with this many counted
// contacts pays for. A partial block rounds up: 2,501 to 12,500 contacts is
// two units.
func PlanUnits(contacts int) int {
	units := 1
	if contacts > IncludedContacts {
		units += (contacts - IncludedContacts + ContactsPerUnit - 1) / ContactsPerUnit
	}
	return units
}

// Errors from the plan's methods.
var (
	ErrUnknownInterval   = errors.New(`the interval must be "month" or "year"`)
	ErrAlreadySubscribed = errors.New("the account is already on the paid plan")
	ErrNotSubscribed     = errors.New("the account has no subscription")
)

// liveStatuses are Stripe subscription statuses that keep an account paid:
// past_due and unpaid while Stripe retries the card (sending is paused).
var liveStatuses = map[string]bool{"active": true, "trialing": true, "past_due": true, "unpaid": true}

// overdueStatuses pause sending until the payment goes through.
var overdueStatuses = map[string]bool{"past_due": true, "unpaid": true}

// countedContacts is the contacts the plan's units count: those that can
// get campaigns now or once confirmed.
func countedContacts(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, accountID string) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM contacts WHERE account_id = $1 AND status IN ('unconfirmed', 'subscribed')`,
		accountID).Scan(&n)
	return n, err
}

// Plan is an account's plan and subscription, as Stripe last reported it.
type Plan struct {
	// Plan is "free" or "paid".
	Plan string
	// Status is Stripe's subscription status; empty without one.
	Status            string
	Interval          string
	Units             int
	CurrentPeriodEnd  *time.Time
	CancelAtPeriodEnd bool
	// Contacts is the counted contacts now, and UnitsNeeded the units they
	// make, which the subscription moves to at its next renewal.
	Contacts    int
	UnitsNeeded int
	Prices      Prices
}

// Plan reads an account's plan.
func (s *Service) Plan(ctx context.Context, accountID string) (*Plan, error) {
	p := Plan{Prices: s.prices()}
	var status, interval *string
	var units *int
	if err := s.DB.QueryRow(ctx, `SELECT plan, subscription_status, billing_interval, plan_units, current_period_end, cancel_at_period_end
		FROM accounts WHERE id = $1`, accountID).
		Scan(&p.Plan, &status, &interval, &units, &p.CurrentPeriodEnd, &p.CancelAtPeriodEnd); err != nil {
		return nil, err
	}
	p.Status, p.Interval = deref(status), deref(interval)
	if units != nil {
		p.Units = *units
	}
	n, err := countedContacts(ctx, s.DB, accountID)
	if err != nil {
		return nil, err
	}
	p.Contacts, p.UnitsNeeded = n, PlanUnits(n)
	return &p, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// PlanOffer is what a plan checkout charges: Units at UnitCents each Interval.
type PlanOffer struct {
	Interval  string
	Units     int
	UnitCents int64
}

// PlanCheckout is a Checkout session that starts the paid plan, billed each
// interval ("month" or "year"). The subscription starts with the units the
// account's contacts make now. idempotencyKey, when set, makes a retry
// return the same session.
func (s *Service) PlanCheckout(ctx context.Context, accountID, interval, idempotencyKey string) (*Session, PlanOffer, error) {
	if interval != Monthly && interval != Yearly {
		return nil, PlanOffer{}, ErrUnknownInterval
	}
	offer := PlanOffer{Interval: interval, UnitCents: s.prices().Cents(interval), Units: 1}
	if s.Stripe == nil {
		return nil, offer, ErrNotConfigured
	}
	var status, customer *string
	if err := s.DB.QueryRow(ctx, `SELECT subscription_status, stripe_customer_id FROM accounts WHERE id = $1`, accountID).
		Scan(&status, &customer); err != nil {
		return nil, offer, err
	}
	if liveStatuses[deref(status)] {
		return nil, offer, ErrAlreadySubscribed
	}
	n, err := countedContacts(ctx, s.DB, accountID)
	if err != nil {
		return nil, offer, err
	}
	offer.Units = PlanUnits(n)
	email, err := s.email(ctx, accountID)
	if err != nil {
		return nil, offer, err
	}
	base := strings.TrimRight(s.BaseURL, "/")
	sess, err := s.Stripe.CreateSubscriptionCheckout(ctx, SubscriptionRequest{
		AccountID: accountID, Email: email, CustomerID: deref(customer), Interval: interval, UnitCents: offer.UnitCents, Units: offer.Units,
		SuccessURL: base + "/billing?checkout=done", CancelURL: base + "/billing?checkout=cancelled",
		IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		return nil, offer, err
	}
	return sess, offer, nil
}

// Portal is a Stripe customer portal page for the account's subscription:
// card, invoices, cancelling.
func (s *Service) Portal(ctx context.Context, accountID string) (string, error) {
	if s.Stripe == nil {
		return "", ErrNotConfigured
	}
	var customer *string
	if err := s.DB.QueryRow(ctx, `SELECT stripe_customer_id FROM accounts WHERE id = $1`, accountID).Scan(&customer); err != nil {
		return "", err
	}
	if deref(customer) == "" {
		return "", ErrNotSubscribed
	}
	return s.Stripe.CreatePortalSession(ctx, *customer, strings.TrimRight(s.BaseURL, "/")+"/billing")
}

// CancelSubscription ends an account's live subscription at once, for
// deleting the account. Without one it does nothing; with one and no
// Stripe keys it's ErrNotConfigured, so the account isn't deleted while
// still paying.
func (s *Service) CancelSubscription(ctx context.Context, accountID string) error {
	var id, status *string
	if err := s.DB.QueryRow(ctx, `SELECT stripe_subscription_id, subscription_status FROM accounts WHERE id = $1`, accountID).
		Scan(&id, &status); err != nil {
		return err
	}
	if deref(id) == "" || !liveStatuses[deref(status)] {
		return nil
	}
	if s.Stripe == nil {
		return ErrNotConfigured
	}
	if err := s.Stripe.CancelSubscription(ctx, *id); err != nil {
		// Already gone in Stripe counts as cancelled.
		if se := (*StripeError)(nil); !errors.As(err, &se) || se.Status != 404 {
			return err
		}
	}
	_, err := s.DB.Exec(ctx, `UPDATE accounts SET subscription_status = 'canceled' WHERE id = $1`, accountID)
	return err
}

// UnitRun is what one SyncUnits did.
type UnitRun struct {
	Accounts int // subscriptions looked at
	Changed  int // subscriptions whose units changed
	Failed   int // changes Stripe refused or didn't answer
}

// SyncUnits moves each live subscription to the units its account's
// contacts make now. Stripe bills the new quantity from the next renewal,
// with no proration.
func (s *Service) SyncUnits(ctx context.Context) (UnitRun, error) {
	var run UnitRun
	if s.Stripe == nil {
		return run, nil
	}
	rows, err := s.DB.Query(ctx, `SELECT id, stripe_subscription_id, subscription_item_id, coalesce(plan_units, 0) FROM accounts
		WHERE status = 'active' AND subscription_status IN ('active', 'trialing', 'past_due', 'unpaid')
			AND stripe_subscription_id IS NOT NULL AND subscription_item_id IS NOT NULL ORDER BY id`)
	if err != nil {
		return run, err
	}
	type sub struct {
		account, id, item string
		units             int
	}
	subs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (sub, error) {
		var x sub
		err := r.Scan(&x.account, &x.id, &x.item, &x.units)
		return x, err
	})
	if err != nil {
		return run, err
	}
	// The key names the change and this run, so a retry within the run is
	// deduplicated but a later change back to the same units isn't mistaken
	// for an earlier one (Stripe would answer from the earlier request and
	// change nothing).
	stamp := s.now().UTC().Format("20060102T150405Z")
	for _, x := range subs {
		run.Accounts++
		n, err := countedContacts(ctx, s.DB, x.account)
		if err != nil {
			return run, err
		}
		units := PlanUnits(n)
		if units == x.units {
			continue
		}
		key := fmt.Sprintf("mailgoose-units:%s:%d-%d:%s", x.id, x.units, units, stamp)
		if err := s.Stripe.SetSubscriptionUnits(ctx, x.id, x.item, units, key); err != nil {
			run.Failed++
			s.log().ErrorContext(ctx, "couldn't change a subscription's plan units", "account_id", x.account, "err", err)
			continue
		}
		if _, err := s.DB.Exec(ctx, `UPDATE accounts SET plan_units = $2 WHERE id = $1`, x.account, units); err != nil {
			return run, err
		}
		run.Changed++
	}
	return run, nil
}

// ExpireCredits writes the expiry entries for every lot past its expiry
// that still has credits left (BIL-3). The ledger also does this whenever an
// account is read, so this only keeps the entries on time for accounts
// nobody looks at. It returns how many accounts it caught up.
func (s *Service) ExpireCredits(ctx context.Context) (int, error) {
	now := s.now()
	rows, err := s.DB.Query(ctx, `
		SELECT DISTINCT l.account_id FROM credit_transactions l
		WHERE l.expires_at IS NOT NULL AND l.expires_at <= $1
			AND l.amount + COALESCE((SELECT sum(c.amount) FROM credit_allocations c WHERE c.lot_id = l.id), 0) > 0`, now)
	if err != nil {
		return 0, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		if err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error { return credits.CatchUp(ctx, tx, id, now) }); err != nil {
			return 0, fmt.Errorf("expire credits of %s: %w", id, err)
		}
	}
	return len(ids), nil
}

// RunJobs syncs plan units and expires credits every interval until ctx
// ends.
func (s *Service) RunJobs(ctx context.Context, interval time.Duration, log *slog.Logger) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		run, err := s.SyncUnits(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			log.ErrorContext(ctx, "sync plan units", "err", err)
		case run.Changed > 0 || run.Failed > 0:
			log.InfoContext(ctx, "plan units synced", "changed", run.Changed, "failed", run.Failed)
		}
		n, err := s.ExpireCredits(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			log.ErrorContext(ctx, "expire credits", "err", err)
		case n > 0:
			log.InfoContext(ctx, "expired credits written", "accounts", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
