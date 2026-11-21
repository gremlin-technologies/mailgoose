package admin

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/billing"
)

// A person's pause on an account paused for its plan's payment (MG-105).

const pastDueSecret = "whsec_mg105_test"

// subscription applies a Stripe subscription event for the harness's
// account through billing's webhook, at the harness's clock. Each call
// needs its own event id and a later clock.
func (h *harness) subscription(t *testing.T, eventID, status string) {
	t.Helper()
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
	out, err := svc.HandleWebhook(context.Background(), body, billing.Sign(body, pastDueSecret, h.now))
	if err != nil || out != billing.OutcomeSubscription {
		t.Fatalf("subscription %s: %s, %v", status, out, err)
	}
}

func (h *harness) pauseCode(t *testing.T) string {
	t.Helper()
	var code *string
	if err := h.db.QueryRow(context.Background(), `SELECT sending_paused_code FROM accounts WHERE id = $1`, h.acc).Scan(&code); err != nil {
		t.Fatal(err)
	}
	return deref(code)
}

// TestAdminPauseOutlastsPayment: staff pausing an account that's paused
// for its payment takes the pause over, and the payment going through
// doesn't lift it; only staff do.
func TestAdminPauseOutlastsPayment(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.subscription(t, "evt_past_due", "past_due")
	if h.pauseCode(t) != billing.SubscriptionPastDue {
		t.Fatalf("past due: code %q", h.pauseCode(t))
	}
	h.now = h.now.Add(time.Hour)
	const why = "Checking a complaint"
	a, err := h.svc.Pause(ctx, h.staff, h.acc, why)
	if err != nil {
		t.Fatal(err)
	}
	if a.PausedAt == nil || deref(a.PauseReason) != why || h.pauseCode(t) != "" {
		t.Fatalf("after the pause: %+v, code %q", a.AccountSummary, h.pauseCode(t))
	}
	h.now = h.now.Add(time.Hour)
	h.subscription(t, "evt_paid", "active")
	if a, err = h.svc.GetAccount(ctx, h.acc); err != nil {
		t.Fatal(err)
	}
	if a.PausedAt == nil || deref(a.PauseReason) != why {
		t.Fatalf("the payment lifted a person's pause: %+v", a.AccountSummary)
	}
	if l := h.last(t); l.action != ActionPause || l.actor == "automatic" {
		t.Errorf("logged after the pause: %+v", l)
	}
	// Staff lift it, and with the payment made nothing takes over.
	h.now = h.now.Add(time.Hour)
	if a, err = h.svc.Unpause(ctx, h.staff, h.acc, "Complaint checked"); err != nil {
		t.Fatal(err)
	}
	if a.PausedAt != nil {
		t.Errorf("still paused: %+v", a.AccountSummary)
	}
}

// TestAdminPauseThenPastDue: staff pause first, then the payment goes past
// due and through again; staff's pause and reason stay throughout.
func TestAdminPauseThenPastDue(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.subscription(t, "evt_active", "active")
	const why = "Checking a complaint"
	if _, err := h.svc.Pause(ctx, h.staff, h.acc, why); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct{ id, status string }{{"evt_past_due", "past_due"}, {"evt_paid", "active"}} {
		h.now = h.now.Add(time.Hour)
		h.subscription(t, step.id, step.status)
		a, err := h.svc.GetAccount(ctx, h.acc)
		if err != nil {
			t.Fatal(err)
		}
		if a.PausedAt == nil || deref(a.PauseReason) != why || h.pauseCode(t) != "" {
			t.Errorf("%s: %+v, code %q", step.status, a.AccountSummary, h.pauseCode(t))
		}
	}
}
