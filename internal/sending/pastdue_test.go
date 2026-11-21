package sending

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/billing"
)

// A sending pause and the plan's past-due pause on one account (MG-105):
// the payment going through lifts only a pause still coded as past due.

const pastDueSecret = "whsec_mg105_test"

// subscription applies a Stripe subscription event for the harness's
// account through billing's webhook, at the harness's clock, as Stripe
// would send it when the plan's payment fails (past_due) or goes through
// (active). Each call needs its own event id and a later clock.
func (h *harness) subscription(t *testing.T, eventID, status string) {
	t.Helper()
	ctx := context.Background()
	// Billing applies a plan only to a signed-up account.
	if _, err := h.db.Exec(ctx, `UPDATE accounts SET status = 'active' WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"id": eventID, "type": "customer.subscription.updated", "created": h.now.Unix(),
		"data": map[string]any{"object": map[string]any{
			"id": "sub_1", "object": "subscription", "customer": "cus_1", "status": status,
			"metadata": map[string]string{"account_id": h.acc},
			"items": map[string]any{"data": []map[string]any{{
				"id": "si_1", "quantity": 1, "current_period_end": h.now.AddDate(0, 1, 0).Unix(),
				"price": map[string]any{"currency": "usd", "recurring": map[string]any{"interval": billing.Monthly}},
			}}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := &billing.Service{DB: h.db, WebhookSecret: pastDueSecret, Now: func() time.Time { return h.now }}
	out, err := svc.HandleWebhook(ctx, body, billing.Sign(body, pastDueSecret, h.now))
	if err != nil || out != billing.OutcomeSubscription {
		t.Fatalf("subscription %s: %s, %v", status, out, err)
	}
}

type pauseState struct {
	paused       bool
	reason, code string
}

func (h *harness) pauseState(t *testing.T) pauseState {
	t.Helper()
	var st pauseState
	var reason, code *string
	if err := h.db.QueryRow(context.Background(), `
		SELECT sending_paused_at IS NOT NULL, sending_paused_reason, sending_paused_code FROM accounts WHERE id = $1`,
		h.acc).Scan(&st.paused, &reason, &code); err != nil {
		t.Fatal(err)
	}
	st.reason, st.code = trimmed(reason), trimmed(code)
	return st
}

// automaticUnpauses counts the pauses billing lifted.
func (h *harness) automaticUnpauses(t *testing.T) int {
	t.Helper()
	var n int
	if err := h.db.QueryRow(context.Background(), `
		SELECT count(*) FROM admin_actions WHERE account_id = $1 AND action = 'unpause' AND actor = 'automatic'`,
		h.acc).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// sendingPause is one of the automatic pauses for an account's sending:
// setup readies the account, and pause makes the pause and returns it, with
// a part of the reason it gives.
type sendingPause struct {
	name  string
	setup func(t *testing.T) *harness
	pause func(t *testing.T, h *harness) *AutoPause
	want  string
}

func sendingPauses() []sendingPause {
	campaign := func(t *testing.T) *harness {
		h := launchedHarness(t, 999)
		h.markSent(t, h.now)
		return h
	}
	return []sendingPause{
		{"complaints", campaign, func(t *testing.T, h *harness) *AutoPause {
			h.eventsFor(t, "complaint", "abuse", 4, h.now)
			return h.check(t)
		}, `of the people sent "Bread week" marked it as spam`},
		{"hard bounces", campaign, func(t *testing.T, h *harness) *AutoPause {
			h.eventsFor(t, "bounce", "permanent/General", 40, h.now)
			return h.check(t)
		}, `of the addresses "Bread week" went to don't exist`},
		{"account level", func(t *testing.T) *harness { return newHarness(t) }, func(t *testing.T, h *harness) *AutoPause {
			// Two campaigns of 300, each too small to be judged alone.
			var last string
			for i := range 2 {
				last = h.sentCampaign(t, h.acc, 300, h.now.AddDate(0, 0, -2+i))
				h.reported(t, last, "campaign", "complaint", "abuse", 0, 1, h.now)
			}
			return h.checkOn(t, last).Pause
		}, "of the people sent this account's campaigns marked them as spam"},
		{"third throttle", func(t *testing.T) *harness {
			h := launchedHarness(t, 2999)
			h.markSent(t, h.now)
			for _, ago := range []int{20, 5} {
				at := h.now.AddDate(0, 0, -ago)
				if _, err := h.db.Exec(context.Background(), `
					INSERT INTO account_throttles (account_id, reason, created_at, until) VALUES ($1, 'earlier', $2, $3)`,
					h.acc, at, at.Add(ThrottleFor)); err != nil {
					t.Fatal(err)
				}
			}
			return h
		}, func(t *testing.T, h *harness) *AutoPause {
			h.eventsFor(t, "complaint", "abuse", 2, h.now)
			return h.checkHealth(t).Pause
		}, "the third campaign to do so in 30 days"},
	}
}

// TestSendingPauseTakesOverPastDue: past due first, then a sending pause.
// The sending pause takes over, with its own reason, and stays after the
// payment goes through.
func TestSendingPauseTakesOverPastDue(t *testing.T) {
	for _, c := range sendingPauses() {
		t.Run(c.name, func(t *testing.T) {
			h := c.setup(t)
			h.subscription(t, "evt_past_due", "past_due")
			if st := h.pauseState(t); !st.paused || st.code != SubscriptionPastDue {
				t.Fatalf("past due: %+v", st)
			}
			h.now = h.now.Add(time.Hour)
			ap := c.pause(t, h)
			if ap == nil {
				t.Fatal("not paused")
			}
			st := h.pauseState(t)
			if !st.paused || st.code != "" || st.reason != ap.Reason || !strings.Contains(st.reason, c.want) {
				t.Fatalf("after the sending pause: %+v, want its own reason (%q)", st, c.want)
			}
			h.now = h.now.Add(time.Hour)
			h.subscription(t, "evt_paid", "active")
			if after := h.pauseState(t); after != st {
				t.Errorf("after the payment: %+v, want %+v", after, st)
			}
			if n := h.automaticUnpauses(t); n != 0 {
				t.Errorf("%d automatic unpauses", n)
			}
		})
	}
}

// TestPastDueAfterSendingPause: a sending pause first, then past due. The
// sending pause and its reason stay, past due and after the payment.
func TestPastDueAfterSendingPause(t *testing.T) {
	for _, c := range sendingPauses() {
		t.Run(c.name, func(t *testing.T) {
			h := c.setup(t)
			h.subscription(t, "evt_active", "active")
			h.now = h.now.Add(time.Hour)
			if ap := c.pause(t, h); ap == nil {
				t.Fatal("not paused")
			}
			st := h.pauseState(t)
			if !st.paused || st.code != "" || !strings.Contains(st.reason, c.want) {
				t.Fatalf("sending pause: %+v", st)
			}
			h.now = h.now.Add(time.Hour)
			h.subscription(t, "evt_past_due", "past_due")
			if after := h.pauseState(t); after != st {
				t.Errorf("past due: %+v, want %+v", after, st)
			}
			h.now = h.now.Add(time.Hour)
			h.subscription(t, "evt_paid", "active")
			if after := h.pauseState(t); after != st {
				t.Errorf("after the payment: %+v, want %+v", after, st)
			}
			if n := h.automaticUnpauses(t); n != 0 {
				t.Errorf("%d automatic unpauses", n)
			}
		})
	}
}

// TestPaymentLiftsPastDueAlone: a past-due pause with nothing on top is
// still lifted by the payment.
func TestPaymentLiftsPastDueAlone(t *testing.T) {
	h := launchedHarness(t, 999)
	h.subscription(t, "evt_past_due", "past_due")
	if st := h.pauseState(t); !st.paused || st.code != SubscriptionPastDue {
		t.Fatalf("past due: %+v", st)
	}
	h.now = h.now.Add(time.Hour)
	h.subscription(t, "evt_paid", "active")
	if st := h.pauseState(t); st.paused || st.code != "" || st.reason != "" {
		t.Errorf("after the payment: %+v", st)
	}
	if n := h.automaticUnpauses(t); n != 1 {
		t.Errorf("%d automatic unpauses", n)
	}
}

// TestSecondSendingPauseKeepsTheFirst: once a sending pause has taken over
// the past-due one, a later sending pause keeps its reason, as on any
// paused account.
func TestSecondSendingPauseKeepsTheFirst(t *testing.T) {
	h := launchedHarness(t, 999)
	h.markSent(t, h.now)
	h.subscription(t, "evt_past_due", "past_due")
	h.now = h.now.Add(time.Hour)
	h.eventsFor(t, "complaint", "abuse", 4, h.now)
	if ap := h.check(t); ap == nil {
		t.Fatal("not paused")
	}
	first := h.pauseState(t)
	// An account-level check on another campaign finds it paused.
	c := h.sentCampaign(t, h.acc, 300, h.now.AddDate(0, 0, -1))
	h.reported(t, c, "campaign", "bounce", "permanent/General", 0, 100, h.now)
	if hl := h.checkOn(t, c); hl.Pause != nil {
		t.Errorf("paused again: %+v", hl.Pause)
	}
	if st := h.pauseState(t); st != first {
		t.Errorf("%+v, want %+v", st, first)
	}
}

// TestLaunchShowsPastDueUnderAnotherPause: while another pause has taken
// over, the launch still says the payment is owed, without promising that
// paying resumes sending; once paid, only the pause is left.
func TestLaunchShowsPastDueUnderAnotherPause(t *testing.T) {
	h := readyHarness(t)
	ctx := context.Background()
	if _, err := h.db.Exec(ctx, `UPDATE accounts SET sending_paused_at = now(), sending_paused_reason = 'Too many complaints.',
		subscription_status = 'past_due' WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	_, err := h.launch("")
	if got := problemCodes(err); got != AccountPaused+","+SubscriptionPastDue {
		t.Fatalf("problems %s", got)
	}
	var nr *NotReadyError
	if !errors.As(err, &nr) || !strings.Contains(nr.Problems[0].Message, "Too many complaints.") ||
		!strings.Contains(nr.Problems[1].Message, "payment didn't go through") ||
		strings.Contains(nr.Problems[1].Hint, "resumes") || !strings.Contains(nr.Problems[1].Hint, "mailgoose plan manage") {
		t.Errorf("%+v", nr)
	}
	if _, err := h.db.Exec(ctx, `UPDATE accounts SET subscription_status = 'active' WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	if _, err := h.launch(""); problemCodes(err) != AccountPaused {
		t.Errorf("paid: %s", problemCodes(err))
	}
}

// TestUnsafeLinkTakesOverPastDue: an unsafe link at launch on an account
// paused for its payment pauses it for the link, logs and alerts it, and
// the payment doesn't lift it.
func TestUnsafeLinkTakesOverPastDue(t *testing.T) {
	h := newHarness(t)
	f, a := withLink(t, h, phishing)
	h.buy(t, 10)
	if _, err := h.test(); err != nil {
		t.Fatal(err)
	}
	relist(h, f, phishing)
	h.subscription(t, "evt_past_due", "past_due")
	h.now = h.now.Add(time.Hour)

	_, err := h.launch("")
	if got := problemCodes(err); got != UnsafeLink+","+SubscriptionPastDue {
		t.Fatalf("problems %s", got)
	}
	st := h.pauseState(t)
	if !st.paused || st.code != "" || !strings.Contains(st.reason, phishing) {
		t.Fatalf("after the launch: %+v", st)
	}
	var logged int
	if err := h.db.QueryRow(context.Background(), `
		SELECT count(*) FROM admin_actions WHERE account_id = $1 AND action = 'auto_pause' AND target_id = $2 AND reason = $3`,
		h.acc, h.campaign.ID, st.reason).Scan(&logged); err != nil {
		t.Fatal(err)
	}
	if logged != 1 || len(a.subjects) != 1 {
		t.Errorf("%d unsafe link pauses logged, alerts %v", logged, a.subjects)
	}
	h.now = h.now.Add(time.Hour)
	h.subscription(t, "evt_paid", "active")
	if after := h.pauseState(t); after != st {
		t.Errorf("after the payment: %+v, want %+v", after, st)
	}
}
