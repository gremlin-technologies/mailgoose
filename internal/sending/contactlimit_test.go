package sending

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gremlin-technologies/mailgoose/internal/campaigns"
	"github.com/gremlin-technologies/mailgoose/internal/contacts"
)

// fillContacts adds n more subscribed contacts straight into the database.
func (h *harness) fillContacts(t *testing.T, n int) {
	t.Helper()
	if _, err := h.db.Exec(context.Background(), `
		INSERT INTO contacts (account_id, email, status, source)
		SELECT $1, 'filler' || g || '@example.com', 'subscribed', 'api' FROM generate_series(1, $2) g`, h.acc, n); err != nil {
		t.Fatal(err)
	}
}

// A free account with more than its contact limit, which only a signup
// form can cause, can't launch (MG-120). Test sends still work, and a paid
// account or one back under the limit launches.
func TestOverContactLimitRefusesALaunch(t *testing.T) {
	ctx := context.Background()
	h := readyHarness(t)
	h.fillContacts(t, contacts.FreeContactLimit+5)

	// The campaign also needs more credits than the account has; the
	// contact limit is listed too.
	_, err := h.launch("")
	var nr *NotReadyError
	if !errors.As(err, &nr) || !strings.Contains(problemCodes(err), OverContactLimit) {
		t.Fatalf("launch over the limit: %v", err)
	}
	var p Problem
	for _, q := range nr.Problems {
		if q.Code == OverContactLimit {
			p = q
		}
	}
	if !strings.Contains(p.Message, "1,00") || !strings.Contains(p.Message, "over the free plan's 1,000") ||
		!strings.Contains(p.Hint, "Upgrade") || !strings.Contains(p.Hint, "delete or unsubscribe") ||
		strings.Contains(p.Hint, "mailgoose ") {
		t.Errorf("problem = %+v", p)
	}
	if p.Details["limit"] != contacts.FreeContactLimit {
		t.Errorf("details = %v", p.Details)
	}
	// A test send still works.
	if _, err := h.test(); err != nil {
		t.Errorf("test send: %v", err)
	}

	// A paid account isn't held by it.
	if _, err := h.db.Exec(ctx, `UPDATE accounts SET plan = 'paid' WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	if _, err := h.launch(""); strings.Contains(problemCodes(err), OverContactLimit) {
		t.Errorf("a paid account was refused: %v", err)
	}
}

// A campaign already sending is held at its next batch while its free
// account is over the limit, and goes on by itself once it's back at it, or
// upgrades (MG-120).
func TestOverContactLimitHoldsASendingCampaign(t *testing.T) {
	ctx := context.Background()
	h := launchedHarness(t, 4)
	p := h.pipeline(fastLimits)
	q := &recorded{}

	dispatch(t, p, q)
	first := q.take()
	if len(first) == 0 {
		t.Fatal("nothing batched while under the limit")
	}

	// A form takes the account past the limit.
	h.fillContacts(t, contacts.FreeContactLimit)
	// The batch already on the queue sends nothing and hands its rows back.
	for _, b := range first {
		if err := p.HandleBatch(ctx, b, 1); err != nil {
			t.Fatal(err)
		}
	}
	if len(h.fake.Sent()) != 0 {
		t.Errorf("sent %d copies while over the limit", len(h.fake.Sent()))
	}
	dispatch(t, p, q)
	if len(q.take()) != 0 || h.status(t) != campaigns.StatusSending {
		t.Errorf("batched while over the limit, status %s", h.status(t))
	}

	// Back at the limit: it goes on.
	if _, err := h.db.Exec(ctx, `DELETE FROM contacts WHERE account_id = $1 AND email LIKE 'filler%'`, h.acc); err != nil {
		t.Fatal(err)
	}
	h.fillContacts(t, contacts.FreeContactLimit-5)
	dispatch(t, p, q)
	for _, b := range q.take() {
		if err := p.HandleBatch(ctx, b, 1); err != nil {
			t.Fatal(err)
		}
	}
	if len(h.fake.Sent()) != 5 {
		t.Errorf("sent %d once back at the limit, want 5", len(h.fake.Sent()))
	}
}

func TestOverContactLimitHoldEndsOnUpgrade(t *testing.T) {
	ctx := context.Background()
	h := launchedHarness(t, 4)
	h.fillContacts(t, contacts.FreeContactLimit+10)
	p := h.pipeline(fastLimits)
	q := &recorded{}
	dispatch(t, p, q)
	if len(q.take()) != 0 {
		t.Fatal("batched while over the limit")
	}
	if _, err := h.db.Exec(ctx, `UPDATE accounts SET plan = 'paid' WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	dispatch(t, p, q)
	if len(q.take()) == 0 {
		t.Error("still held after upgrading")
	}
}

// The confirm step checks the limit again: a form can take the account over
// it between the summary and the code (MG-120).
func TestOverContactLimitRefusesTheConfirm(t *testing.T) {
	h := readyHarness(t)
	code := h.summary(t).Code
	h.fillContacts(t, contacts.FreeContactLimit)
	if _, err := h.launch(code); problemCodes(err) != OverContactLimit {
		t.Errorf("confirm over the limit: %v", err)
	}
}
