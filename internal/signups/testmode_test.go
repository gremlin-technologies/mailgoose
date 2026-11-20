package signups

import (
	"context"
	"errors"
	"testing"
)

// TestTestModeForms: in test mode (MG-80) a form takes signups only from
// confirmed test addresses and the owner's own address, also while the
// account waits for its sign-up review.
func TestTestModeForms(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for _, q := range []string{
		`UPDATE accounts SET sending_mode = 'test' WHERE id = $1`,
		`INSERT INTO users (account_id, email) VALUES ($1, 'Pat@Home.example')`,
		`INSERT INTO test_addresses (account_id, email, confirmed_at, created_at) VALUES ($1, 'qa@example.com', now(), now())`,
		`INSERT INTO test_addresses (account_id, email, created_at) VALUES ($1, 'pending@example.com', now())`,
	} {
		if _, err := h.db.Exec(ctx, q, h.acc); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for _, email := range []string{"ann@example.com", "pending@example.com"} {
		if _, err := h.submit(h.post(email)); !errors.Is(err, ErrTestMode) {
			t.Errorf("%s: %v", email, err)
		}
		var n int
		if err := h.db.QueryRow(ctx, `SELECT count(*) FROM contacts WHERE account_id = $1 AND email = $2`, h.acc, email).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s: contact made (%d, %v)", email, n, err)
		}
	}
	if len(h.fake.Sent()) != 0 {
		t.Errorf("refused signups sent mail: %d", len(h.fake.Sent()))
	}
	if _, err := h.submit(h.post("QA@example.com")); err != nil {
		t.Errorf("confirmed test address: %v", err)
	}
	if _, err := h.db.Exec(ctx, `UPDATE accounts SET review = 'waiting', review_at = now()`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.submit(h.post("pat@home.example")); err != nil {
		t.Errorf("owner, waiting for review: %v", err)
	}
	if _, err := h.submit(h.post("ann@example.com")); !errors.Is(err, ErrTestMode) {
		t.Errorf("waiting for review, not a test address: %v", err)
	}
}
