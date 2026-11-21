package signups

import (
	"context"
	"errors"
	"testing"
)

// TestRejectedSignupFormClosed: a form whose account's sign-up review was
// rejected takes no signups once the suspension that came with the
// rejection is lifted (MG-106), and stores nothing and sends nothing;
// approving the sign-up opens it. In test mode it takes its test
// addresses, as while the review waits.
func TestRejectedSignupFormClosed(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	// As `mailgoose admin reject-signup` then `unsuspend` leave it.
	if _, err := h.db.Exec(ctx, `UPDATE accounts SET review = 'rejected', review_at = now() WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	if _, err := h.submit(h.post("ann@example.com")); !errors.Is(err, ErrFormClosed) {
		t.Fatalf("rejected, unsuspended: %v", err)
	}
	if n := h.contactCount(t); n != 0 || len(h.fake.Sent()) != 0 {
		t.Errorf("refused signup made %d contacts and sent %d emails", n, len(h.fake.Sent()))
	}

	for _, q := range []string{
		`UPDATE accounts SET sending_mode = 'test' WHERE id = $1`,
		`INSERT INTO test_addresses (account_id, email, confirmed_at, created_at) VALUES ($1, 'qa@example.com', now(), now())`,
	} {
		if _, err := h.db.Exec(ctx, q, h.acc); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if _, err := h.submit(h.post("qa@example.com")); err != nil {
		t.Errorf("test mode, rejected, test address: %v", err)
	}

	if _, err := h.db.Exec(ctx, `UPDATE accounts SET sending_mode = 'full', review = 'approved' WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	if _, err := h.submit(h.post("ann@example.com")); err != nil {
		t.Fatalf("approved: %v", err)
	}
	if status, _ := h.contact(t, "ann@example.com"); status == "" {
		t.Error("no contact once approved")
	}
}
