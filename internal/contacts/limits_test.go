package contacts

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fill adds n subscribed contacts to account straight in the database.
func (h *harness) fill(t *testing.T, account string, n int) {
	t.Helper()
	if _, err := h.svc.DB.Exec(context.Background(), `
		INSERT INTO contacts (account_id, email, status, source)
		SELECT $1, 'filler' || g || '@example.com', 'subscribed', 'api' FROM generate_series(1, $2) g`,
		account, n); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) setPlan(t *testing.T, account, plan string) {
	t.Helper()
	if _, err := h.svc.DB.Exec(context.Background(), `UPDATE accounts SET plan = $2 WHERE id = $1`, account, plan); err != nil {
		t.Fatal(err)
	}
}

func TestCountedContacts(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.add(t, "a@example.com", StatusSubscribed)
	h.add(t, "b@example.com", StatusUnconfirmed)
	h.add(t, "c@example.com", StatusUnsubscribed)
	gone := h.add(t, "d@example.com", StatusSubscribed)
	if _, err := h.svc.DB.Exec(ctx, `UPDATE contacts SET status = 'bounced' WHERE id = $1`, gone.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Create(ctx, h.acc2, CreateInput{Email: "a@example.com"}); err != nil {
		t.Fatal(err)
	}
	n, err := h.svc.CountedContacts(ctx, h.acc)
	if err != nil || n != 2 {
		t.Errorf("counted = %d, %v; want 2 (subscribed and unconfirmed only)", n, err)
	}
	for status, want := range map[string]bool{StatusSubscribed: true, StatusUnconfirmed: true, StatusUnsubscribed: false, StatusBounced: false, StatusComplained: false} {
		if Counted(status) != want {
			t.Errorf("Counted(%s) = %v", status, !want)
		}
	}
}

func TestFreeAccountContactLimit(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.fill(t, h.acc, FreeContactLimit-1)

	// The 500th fits.
	h.add(t, "last@example.com", StatusUnconfirmed)

	// The 501st doesn't, subscribed or unconfirmed.
	for _, status := range []string{StatusSubscribed, StatusUnconfirmed} {
		_, err := h.svc.Create(ctx, h.acc, CreateInput{Email: "over@example.com", Status: status})
		var le *LimitError
		if !errors.As(err, &le) || le.Limit != FreeContactLimit || le.Count != FreeContactLimit {
			t.Errorf("%s over the limit: err = %v", status, err)
		}
	}
	// An unsubscribed contact doesn't count, so it still fits.
	h.add(t, "gone@example.com", StatusUnsubscribed)
	// A suppressed address comes back unsubscribed, so it fits too.
	if err := h.svc.Delete(ctx, h.acc, h.add(t, "old@example.com", StatusUnsubscribed).ID); err != nil {
		t.Fatal(err)
	}
	if c, err := h.svc.Create(ctx, h.acc, CreateInput{Email: "old@example.com"}); err != nil || c.Status != StatusUnsubscribed {
		t.Errorf("suppressed address at the limit: %+v, %v", c, err)
	}
	// Other accounts aren't affected.
	if _, err := h.svc.Create(ctx, h.acc2, CreateInput{Email: "over@example.com"}); err != nil {
		t.Errorf("other account: %v", err)
	}

	// Unsubscribing one makes room for one.
	last, err := h.svc.List(ctx, h.acc, Filter{Email: "last@example.com"}, nil, 1)
	if err != nil || len(last) != 1 {
		t.Fatalf("find last: %v, %v", last, err)
	}
	if _, err := h.svc.Update(ctx, h.acc, last[0].ID, UpdateInput{Status: set(StatusUnsubscribed)}); err != nil {
		t.Fatal(err)
	}
	h.add(t, "over@example.com", StatusSubscribed)
	if n, _ := h.svc.CountedContacts(ctx, h.acc); n != FreeContactLimit {
		t.Errorf("counted = %d", n)
	}
}

func TestPaidAccountHasNoContactCap(t *testing.T) {
	h := newHarness(t)
	h.setPlan(t, h.acc, "paid")
	h.fill(t, h.acc, FreeContactLimit)
	h.add(t, "more@example.com", StatusSubscribed)
	if n, err := h.svc.CountedContacts(context.Background(), h.acc); err != nil || n != FreeContactLimit+1 {
		t.Errorf("counted = %d, %v", n, err)
	}
}

// Concurrent adds can't both take the last place.
func TestContactLimitHoldsUnderConcurrentCreates(t *testing.T) {
	h := newHarness(t)
	h.fill(t, h.acc, FreeContactLimit-1)
	// The harness clock isn't safe for concurrent use; this one is.
	svc := &Service{DB: h.svc.DB, Now: func() time.Time { return h.now }}
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = svc.Create(context.Background(), h.acc, CreateInput{Email: "racer" + string(rune('a'+i)) + "@example.com"})
		}()
	}
	wg.Wait()
	ok, limited := 0, 0
	for _, err := range errs {
		var le *LimitError
		switch {
		case err == nil:
			ok++
		case errors.As(err, &le):
			limited++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok != 1 || limited != len(errs)-1 {
		t.Errorf("%d created and %d limited; want 1 and %d", ok, limited, len(errs)-1)
	}
}
