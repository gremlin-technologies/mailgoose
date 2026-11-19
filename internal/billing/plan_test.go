package billing

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestPlanUnits(t *testing.T) {
	for contacts, want := range map[int]int{0: 1, 500: 1, 2_500: 1, 2_501: 2, 12_500: 2, 12_501: 3, 102_500: 11} {
		if got := PlanUnits(contacts); got != want {
			t.Errorf("%d contacts: %d units, want %d", contacts, got, want)
		}
	}
}

func TestParsePrices(t *testing.T) {
	if p, err := ParsePrices(""); err != nil || p != DefaultPrices {
		t.Fatalf("empty: %+v %v", p, err)
	}
	if DefaultPrices.Cents(Monthly) != 500 || DefaultPrices.Cents(Yearly) != 5_000 {
		t.Errorf("defaults %+v", DefaultPrices)
	}
	p, err := ParsePrices(" year:4800 , month:450")
	if err != nil || p != (Prices{MonthCents: 450, YearCents: 4_800}) {
		t.Errorf("got %+v %v", p, err)
	}
	for _, bad := range []string{"month:500", "month:500,year:0", "week:5,month:5,year:5", "month:x,year:5", "month,year"} {
		if _, err := ParsePrices(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

// contacts adds n contacts with a status to the harness's account.
func (h *harness) contacts(t *testing.T, n int, status string) {
	t.Helper()
	if _, err := h.db.Exec(context.Background(), `INSERT INTO contacts (account_id, email, status, source)
		SELECT $1, $3 || g || '@example.com', $2, 'api' FROM generate_series(1, $4) g`, h.acc, status, status, n); err != nil {
		t.Fatal(err)
	}
}

func TestPlanCheckout(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, _, err := h.svc.PlanCheckout(ctx, h.acc, Monthly, ""); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("no Stripe: %v", err)
	}
	f := &fakeStripe{}
	h.svc.Stripe = f
	if _, _, err := h.svc.PlanCheckout(ctx, h.acc, "week", ""); !errors.Is(err, ErrUnknownInterval) {
		t.Errorf("week: %v", err)
	}
	if _, err := h.db.Exec(ctx, `INSERT INTO users (account_id, email) VALUES ($1, 'pat@bakery.example')`, h.acc); err != nil {
		t.Fatal(err)
	}
	// 2,600 counted contacts (unsubscribed ones don't count): two units.
	h.contacts(t, 2_000, "subscribed")
	h.contacts(t, 600, "unconfirmed")
	h.contacts(t, 50, "unsubscribed")
	sess, offer, err := h.svc.PlanCheckout(ctx, h.acc, Yearly, "k")
	if err != nil {
		t.Fatal(err)
	}
	if sess.URL == "" || offer != (PlanOffer{Interval: Yearly, Units: 2, UnitCents: 5_000}) {
		t.Errorf("session %+v offer %+v", sess, offer)
	}
	want := SubscriptionRequest{AccountID: h.acc, Email: "pat@bakery.example", Interval: Yearly, UnitCents: 5_000, Units: 2,
		SuccessURL: "https://app.example/billing?checkout=done", CancelURL: "https://app.example/billing?checkout=cancelled",
		IdempotencyKey: "k"}
	if len(f.subs) != 1 || f.subs[0] != want {
		t.Errorf("Stripe got %+v", f.subs)
	}

	// Subscribed: no second checkout. After it ends, the same customer.
	h.subscribe(t, "active")
	if _, _, err := h.svc.PlanCheckout(ctx, h.acc, Monthly, ""); !errors.Is(err, ErrAlreadySubscribed) {
		t.Errorf("already subscribed: %v", err)
	}
	h.set(h.now.Add(1))
	h.send(t, h.subBody(subEvent{id: "evt_end", typ: "customer.subscription.deleted", status: "canceled"}))
	if _, _, err := h.svc.PlanCheckout(ctx, h.acc, Monthly, ""); err != nil {
		t.Fatal(err)
	}
	if got := f.subs[len(f.subs)-1]; got.CustomerID != "cus_1" || got.UnitCents != 500 {
		t.Errorf("resubscribe %+v", got)
	}
}

func TestPortalAndCancel(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	f := &fakeStripe{}
	h.svc.Stripe = f
	if _, err := h.svc.Portal(ctx, h.acc); !errors.Is(err, ErrNotSubscribed) {
		t.Errorf("portal before subscribing: %v", err)
	}
	// Nothing to cancel yet.
	if err := h.svc.CancelSubscription(ctx, h.acc); err != nil || len(f.canceled) != 0 {
		t.Errorf("cancel with no subscription: %v %v", err, f.canceled)
	}
	h.subscribe(t, "active")
	url, err := h.svc.Portal(ctx, h.acc)
	if err != nil || url != "https://billing.stripe.test/p/cus_1?return=https://app.example/billing" {
		t.Errorf("portal %q %v", url, err)
	}

	// Stripe down: the error comes back and nothing changes.
	f.err = &StripeError{Status: 503}
	if err := h.svc.CancelSubscription(ctx, h.acc); err == nil {
		t.Error("no error with Stripe down")
	}
	if st := h.state(t); st.status != "active" {
		t.Errorf("status %s", st.status)
	}
	// Already gone in Stripe counts as cancelled.
	f.err = &StripeError{Status: 404}
	if err := h.svc.CancelSubscription(ctx, h.acc); err != nil {
		t.Fatal(err)
	}
	if st := h.state(t); st.status != "canceled" {
		t.Errorf("status %s", st.status)
	}
	if len(f.canceled) != 2 || f.canceled[0] != "sub_1" {
		t.Errorf("canceled %v", f.canceled)
	}
	// Without Stripe keys a live subscription can't be cancelled.
	h2 := newHarness(t)
	h2.subscribe(t, "active")
	if err := h2.svc.CancelSubscription(ctx, h2.acc); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("no keys: %v", err)
	}
}

func TestSyncUnits(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.subscribe(t, "active")
	if run, err := h.svc.SyncUnits(ctx); err != nil || run != (UnitRun{}) {
		t.Errorf("without Stripe: %+v %v", run, err)
	}
	f := &fakeStripe{}
	h.svc.Stripe = f
	if run, err := h.svc.SyncUnits(ctx); err != nil || run != (UnitRun{Accounts: 1}) {
		t.Errorf("unchanged: %+v %v", run, err)
	}
	h.contacts(t, 12_501, "subscribed")
	run, err := h.svc.SyncUnits(ctx)
	if err != nil || run != (UnitRun{Accounts: 1, Changed: 1}) {
		t.Fatalf("grown: %+v %v", run, err)
	}
	if want := "sub_1/si_sub_1=3 mailgoose-units:sub_1:1-3:20261118T120000Z"; len(f.units) != 1 || f.units[0] != want {
		t.Errorf("Stripe got %v", f.units)
	}
	if st := h.state(t); st.units != 3 {
		t.Errorf("units %d", st.units)
	}
	// Synced: nothing more to change. A refusal is counted, not fatal.
	if run, _ := h.svc.SyncUnits(ctx); run.Changed != 0 {
		t.Errorf("second run %+v", run)
	}
	if _, err := h.db.Exec(ctx, `UPDATE accounts SET plan_units = 1 WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	f.err = fmt.Errorf("%w: timeout", ErrStripeUnreachable)
	if run, err := h.svc.SyncUnits(ctx); err != nil || run.Failed != 1 {
		t.Errorf("refused: %+v %v", run, err)
	}
	if st := h.state(t); st.units != 1 {
		t.Errorf("units changed after a refusal: %d", st.units)
	}
	// The same change in a later run gets its own key, so Stripe doesn't
	// answer it from the first request and change nothing.
	f.err = nil
	h.set(h.now.Add(time.Hour))
	if run, err := h.svc.SyncUnits(ctx); err != nil || run.Changed != 1 {
		t.Fatalf("changed again: %+v %v", run, err)
	}
	if want := "sub_1/si_sub_1=3 mailgoose-units:sub_1:1-3:20261118T130000Z"; len(f.units) != 2 || f.units[1] != want {
		t.Errorf("Stripe got %v", f.units)
	}
}

func TestPlanSummary(t *testing.T) {
	h := newHarness(t)
	h.contacts(t, 3_000, "subscribed")
	p, err := h.svc.Plan(context.Background(), h.acc)
	if err != nil {
		t.Fatal(err)
	}
	if p.Plan != "free" || p.Status != "" || p.Contacts != 3_000 || p.UnitsNeeded != 2 || p.Prices != DefaultPrices {
		t.Errorf("plan %+v", p)
	}
	h.svc.Prices = Prices{MonthCents: 600, YearCents: 6_000}
	h.subscribe(t, "active")
	p, _ = h.svc.Plan(context.Background(), h.acc)
	if p.Plan != "paid" || p.Interval != Monthly || p.Units != 1 || p.CurrentPeriodEnd == nil || p.Prices.MonthCents != 600 ||
		!strings.Contains(fmt.Sprint(p.Status), "active") {
		t.Errorf("paid %+v", p)
	}
}
