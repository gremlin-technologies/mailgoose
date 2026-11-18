package billing

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/credits"
)

// subEvent is a Stripe subscription event for the harness's account.
type subEvent struct {
	id, typ, sub, status, interval string
	units                          int
	created                        time.Time
	cancelAtEnd                    bool
	noMetadata                     bool
	account                        string
}

func (h *harness) subBody(e subEvent) []byte {
	if e.typ == "" {
		e.typ = "customer.subscription.updated"
	}
	if e.sub == "" {
		e.sub = "sub_1"
	}
	if e.interval == "" {
		e.interval = Monthly
	}
	if e.units == 0 {
		e.units = 1
	}
	if e.created.IsZero() {
		e.created = h.clock()
	}
	meta := map[string]string{"account_id": h.acc}
	if e.account != "" {
		meta["account_id"] = e.account
	}
	if e.noMetadata {
		meta = map[string]string{}
	}
	b, _ := json.Marshal(map[string]any{
		"id": e.id, "type": e.typ, "created": e.created.Unix(),
		"data": map[string]any{"object": map[string]any{
			"id": e.sub, "object": "subscription", "customer": "cus_1", "status": e.status,
			"cancel_at_period_end": e.cancelAtEnd, "metadata": meta,
			"items": map[string]any{"data": []map[string]any{{
				"id": "si_" + e.sub, "quantity": e.units, "current_period_end": e.created.AddDate(0, 1, 0).Unix(),
				"price": map[string]any{"currency": "usd", "recurring": map[string]any{"interval": e.interval}},
			}}},
		}},
	})
	return b
}

// subscribe puts the harness's account on a subscription with this status.
func (h *harness) subscribe(t *testing.T, status string) {
	t.Helper()
	if got := h.send(t, h.subBody(subEvent{id: "evt_sub_" + status, typ: "customer.subscription.created", status: status})); got != OutcomeSubscription {
		t.Fatalf("subscribe: %s", got)
	}
}

type accountState struct {
	plan, status, interval, sub, pauseCode string
	units                                  int
	paused, cancelAtEnd                    bool
	paidSince                              *time.Time
}

func (h *harness) state(t *testing.T) accountState {
	t.Helper()
	var st accountState
	var status, interval, sub, code *string
	var units *int
	var pausedAt *time.Time
	if err := h.db.QueryRow(context.Background(), `SELECT plan, subscription_status, billing_interval, stripe_subscription_id,
			plan_units, sending_paused_at, sending_paused_code, cancel_at_period_end, paid_since
		FROM accounts WHERE id = $1`, h.acc).
		Scan(&st.plan, &status, &interval, &sub, &units, &pausedAt, &code, &st.cancelAtEnd, &st.paidSince); err != nil {
		t.Fatal(err)
	}
	st.status, st.interval, st.sub, st.pauseCode = deref(status), deref(interval), deref(sub), deref(code)
	st.paused = pausedAt != nil
	if units != nil {
		st.units = *units
	}
	return st
}

func TestSubscriptionLifecycle(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.subscribe(t, "active")
	st := h.state(t)
	if st.plan != "paid" || st.status != "active" || st.interval != Monthly || st.units != 1 || st.sub != "sub_1" ||
		st.paused || st.paidSince == nil || !st.paidSince.Equal(h.now) {
		t.Fatalf("after subscribing: %+v", st)
	}
	sum, err := h.svc.Plan(ctx, h.acc)
	if err != nil || sum.Plan != "paid" {
		t.Fatalf("plan %+v %v", sum, err)
	}

	// A pack bought, then a renewal a month on: still paid, the period moves
	// on, and no free grant arrives.
	if got := h.send(t, h.event("evt_buy", "checkout.session.completed", "cs_1", "paid", "10k", 2000)); got != OutcomeCredited {
		t.Fatalf("buy: %s", got)
	}
	h.set(h.now.AddDate(0, 1, 0))
	h.send(t, h.subBody(subEvent{id: "evt_renew", status: "active"}))
	var periodEnd time.Time
	if err := h.db.QueryRow(ctx, `SELECT current_period_end FROM accounts WHERE id = $1`, h.acc).Scan(&periodEnd); err != nil {
		t.Fatal(err)
	}
	if st := h.state(t); st.plan != "paid" || st.paused || !periodEnd.Equal(h.now.AddDate(0, 1, 0)) {
		t.Fatalf("renewed: %+v, period ends %v", st, periodEnd)
	}
	if got := h.balance(t); got != 10_000 {
		t.Errorf("balance %d after a month on the paid plan, want only the pack", got)
	}
	var grants int
	if err := h.db.QueryRow(ctx, `SELECT count(*) FROM credit_transactions WHERE account_id = $1 AND type = $2`,
		h.acc, credits.TypeGrant).Scan(&grants); err != nil || grants != 0 {
		t.Errorf("%d free grants while paid (%v)", grants, err)
	}

	// A renewal fails: sending pauses with its own code.
	h.set(h.now.Add(time.Hour))
	if got := h.send(t, h.subBody(subEvent{id: "evt_2", status: "past_due"})); got != OutcomeSubscription {
		t.Fatalf("past due: %s", got)
	}
	if st := h.state(t); st.plan != "paid" || !st.paused || st.pauseCode != SubscriptionPastDue {
		t.Fatalf("past due: %+v", st)
	}
	// Stripe retries and it goes through: the pause lifts.
	h.set(h.now.Add(time.Hour))
	h.send(t, h.subBody(subEvent{id: "evt_3", status: "active"}))
	if st := h.state(t); st.paused || st.pauseCode != "" {
		t.Fatalf("paid again: %+v", st)
	}
	var unpauses int
	if err := h.db.QueryRow(ctx, `SELECT count(*) FROM admin_actions WHERE account_id = $1 AND action = 'unpause' AND actor = 'automatic'`,
		h.acc).Scan(&unpauses); err != nil || unpauses != 1 {
		t.Errorf("%d automatic unpauses (%v)", unpauses, err)
	}

	// Cancelled in the portal: still paid to the period's end.
	h.set(h.now.Add(time.Hour))
	h.send(t, h.subBody(subEvent{id: "evt_4", status: "active", cancelAtEnd: true}))
	if st := h.state(t); st.plan != "paid" || !st.cancelAtEnd {
		t.Fatalf("cancelling: %+v", st)
	}
	// The period ends: free again, and bought credits stay.
	h.set(h.now.Add(time.Hour))
	h.send(t, h.subBody(subEvent{id: "evt_5", typ: "customer.subscription.deleted", status: "canceled", cancelAtEnd: true}))
	if st := h.state(t); st.plan != "free" || st.status != "canceled" {
		t.Fatalf("ended: %+v", st)
	}
	var left int64
	if err := h.db.QueryRow(ctx, `
		SELECT l.amount + COALESCE((SELECT sum(c.amount) FROM credit_allocations c WHERE c.lot_id = l.id), 0)
		FROM credit_transactions l WHERE l.account_id = $1 AND l.ref = 'stripe:cs_1'`, h.acc).Scan(&left); err != nil || left != 10_000 {
		t.Errorf("%d bought credits left after ending (%v)", left, err)
	}
}

func TestSubscriptionEndingWhilePastDueLiftsThePause(t *testing.T) {
	h := newHarness(t)
	h.subscribe(t, "past_due")
	if st := h.state(t); !st.paused {
		t.Fatalf("not paused: %+v", st)
	}
	h.set(h.now.Add(time.Hour))
	h.send(t, h.subBody(subEvent{id: "evt_end", typ: "customer.subscription.deleted", status: "canceled"}))
	if st := h.state(t); st.plan != "free" || st.paused {
		t.Fatalf("after ending: %+v", st)
	}
}

func TestSubscriptionKeepsOtherPauses(t *testing.T) {
	h := newHarness(t)
	if _, err := h.db.Exec(context.Background(), `UPDATE accounts SET sending_paused_at = now(), sending_paused_reason = 'complaints'
		WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	h.subscribe(t, "past_due")
	h.set(h.now.Add(time.Hour))
	h.send(t, h.subBody(subEvent{id: "evt_ok", status: "active"}))
	if st := h.state(t); !st.paused || st.pauseCode != "" {
		t.Fatalf("someone else's pause was changed: %+v", st)
	}
}

func TestSubscriptionEventsOutOfOrder(t *testing.T) {
	h := newHarness(t)
	h.subscribe(t, "active")
	// An older event arriving late changes nothing.
	if got := h.send(t, h.subBody(subEvent{id: "evt_old", status: "past_due", created: h.now.Add(-time.Minute)})); got != OutcomeStale {
		t.Errorf("old event: %s", got)
	}
	if st := h.state(t); st.paused || st.status != "active" {
		t.Errorf("after an old event: %+v", st)
	}
	// The same event twice is a duplicate.
	body := h.subBody(subEvent{id: "evt_twice", status: "active", units: 2})
	h.send(t, body)
	if got := h.send(t, body); got != OutcomeDuplicate {
		t.Errorf("redelivered: %s", got)
	}
	if st := h.state(t); st.units != 2 {
		t.Errorf("units %d", st.units)
	}
}

func TestSubscriptionMismatches(t *testing.T) {
	h := newHarness(t)
	h.subscribe(t, "active")

	// A second live subscription for the same account isn't applied.
	h.set(h.now.Add(time.Hour))
	if got := h.send(t, h.subBody(subEvent{id: "evt_two", sub: "sub_2", status: "active", interval: Yearly})); got != OutcomeMismatch {
		t.Errorf("second live subscription: %s", got)
	}
	// An old subscription ending changes nothing about the current one.
	if got := h.send(t, h.subBody(subEvent{id: "evt_gone", typ: "customer.subscription.deleted", sub: "sub_0", status: "canceled"})); got != OutcomeStale {
		t.Errorf("old subscription ending: %s", got)
	}
	if st := h.state(t); st.sub != "sub_1" || st.interval != Monthly || st.plan != "paid" {
		t.Errorf("current subscription changed: %+v", st)
	}
	// No metadata: found by its id; unknown id: a mismatch.
	h.set(h.now.Add(time.Hour))
	if got := h.send(t, h.subBody(subEvent{id: "evt_nometa", status: "active", units: 3, noMetadata: true})); got != OutcomeSubscription {
		t.Errorf("by id: %s", got)
	}
	if got := h.send(t, h.subBody(subEvent{id: "evt_unknown", sub: "sub_x", status: "active", noMetadata: true})); got != OutcomeMismatch {
		t.Errorf("unknown subscription: %s", got)
	}
	if got := h.send(t, h.subBody(subEvent{id: "evt_noacc", sub: "sub_y", status: "active", account: "00000000-0000-0000-0000-000000000000"})); got != OutcomeMismatch {
		t.Errorf("unknown account: %s", got)
	}
	// Not an account id: a mismatch, not an error Stripe would retry.
	if got := h.send(t, h.subBody(subEvent{id: "evt_badacc", sub: "sub_z", status: "active", account: "acc_nope"})); got != OutcomeMismatch {
		t.Errorf("not an account id: %s", got)
	}
}

func TestSubscriptionOnAClosedAccount(t *testing.T) {
	h := newHarness(t)
	if _, err := h.db.Exec(context.Background(), `UPDATE accounts SET status = 'closed' WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	h.subscribe(t, "active")
	if st := h.state(t); st.plan != "free" || st.status != "active" {
		t.Errorf("closed account: %+v", st)
	}
}

func TestSubscriptionCheckoutSessionIsIgnored(t *testing.T) {
	h := newHarness(t)
	b, _ := json.Marshal(map[string]any{
		"id": "evt_cs", "type": "checkout.session.completed",
		"data": map[string]any{"object": map[string]any{
			"id": "cs_sub", "object": "checkout.session", "mode": "subscription", "payment_status": "paid",
			"client_reference_id": h.acc, "amount_subtotal": 500, "currency": "usd",
			"metadata": map[string]string{"account_id": h.acc},
		}},
	})
	before := h.balance(t)
	if got := h.send(t, b); got != OutcomeIgnored {
		t.Errorf("outcome %s", got)
	}
	if h.balance(t) != before {
		t.Error("a subscription checkout added credits")
	}
}

func (h *harness) set(at time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.now = at
}
