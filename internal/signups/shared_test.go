package signups

import (
	"context"
	"testing"

	"github.com/gremlin-technologies/mailgoose/internal/domains"
	"github.com/gremlin-technologies/mailgoose/internal/mail"
	"github.com/gremlin-technologies/mailgoose/internal/senders"
)

// TestSharedSendingConfirmations: with no verified domain, a form's
// confirmation email goes from the account's slug on the shared domain with
// replies to its verified From address, and only with shared sending on and
// the From verified (MG-85).
func TestSharedSendingConfirmations(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.db.Exec(ctx, `DELETE FROM domains WHERE account_id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(ctx, `INSERT INTO users (account_id, email) VALUES ($1, 'pat@example.com')`, h.acc); err != nil {
		t.Fatal(err)
	}

	// Shared sending off: no verified From, as before.
	if probs, err := h.svc.Problems(ctx, h.form); err != nil || len(probs) != 1 || probs[0].Code != "no_verified_from" {
		t.Fatalf("problems with shared off: %+v %v", probs, err)
	}

	h.svc.Senders = &senders.Resolver{DB: h.db, Domains: &domains.Service{DB: h.db}, Shared: "shared.test.example"}
	// Shared on, but hello@patsbakery.com is neither the owner's address nor
	// a confirmed sender address.
	if probs, err := h.svc.Problems(ctx, h.form); err != nil || len(probs) != 1 || probs[0].Code != "no_verified_from" {
		t.Fatalf("problems with an unverified From: %+v %v", probs, err)
	}

	if _, err := h.db.Exec(ctx, `UPDATE accounts SET from_email = 'pat@example.com' WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	if probs, err := h.svc.Problems(ctx, h.form); err != nil || len(probs) != 0 {
		t.Fatalf("problems with the owner's From: %+v %v", probs, err)
	}
	if _, err := h.submit(h.post("ann@example.com")); err != nil {
		t.Fatal(err)
	}
	sent, ok := h.fake.Last()
	if !ok {
		t.Fatal("no confirmation sent")
	}
	m := sent.Message
	want := mail.Address{Name: "Pat's Bakery", Email: "pats-bakery@shared.test.example"}
	if m.Kind != mail.KindConfirm || m.From != want || len(m.ReplyTo) != 1 || m.ReplyTo[0].Email != "pat@example.com" {
		t.Errorf("message from %+v reply-to %+v", m.From, m.ReplyTo)
	}
}
