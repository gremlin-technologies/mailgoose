package admin

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

// waiting puts the account in review with flags, as a sign-up does.
func (h *harness) waiting(t *testing.T, accountID string, flags ...string) {
	t.Helper()
	if flags == nil {
		flags = []string{}
	}
	if _, err := h.db.Exec(context.Background(), `UPDATE accounts SET review = 'waiting', review_at = $2, review_flags = $3 WHERE id = $1`,
		accountID, h.now, flags); err != nil {
		t.Fatal(err)
	}
}

// TestSignupReview lists, approves and rejects waiting accounts (MG-66).
func TestSignupReview(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	lee := h.account(t, "Lee's Shop", "lee@mailinator.com")
	h.waiting(t, h.acc)
	h.now = h.now.Add(time.Minute)
	h.waiting(t, lee, "disposable_domain", "ip_burst")

	list, err := h.svc.ListSignups(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != h.acc || list[1].ID != lee || !slices.Equal(list[1].ReviewFlags, []string{"disposable_domain", "ip_burst"}) ||
		list[0].ReviewFlags == nil || list[1].ReviewAt == nil || *list[1].OwnerEmail != "lee@mailinator.com" {
		t.Fatalf("waiting = %+v", list)
	}

	acc, err := h.svc.ApproveSignup(ctx, h.staff, h.acc, "Real bakery.")
	if err != nil {
		t.Fatal(err)
	}
	if deref(acc.Review) != "approved" || acc.SuspendedAt != nil {
		t.Errorf("approved = %+v", acc.AccountSummary)
	}
	if _, err := h.svc.ApproveSignup(ctx, h.staff, h.acc, "Again."); !errors.Is(err, ErrNotWaiting) {
		t.Errorf("approve again: %v", err)
	}
	if _, err := h.svc.RejectSignup(ctx, h.staff, h.acc, "Changed my mind."); !errors.Is(err, ErrNotWaiting) {
		t.Errorf("reject once approved: %v", err)
	}

	acc, err = h.svc.RejectSignup(ctx, h.staff, lee, "Throwaway address.")
	if err != nil {
		t.Fatal(err)
	}
	if deref(acc.Review) != "rejected" || acc.SuspendedAt == nil || deref(acc.SuspendedReason) != "Throwaway address." ||
		deref(acc.SuspendedBy) != h.staff.Email {
		t.Errorf("rejected = %+v", acc.AccountSummary)
	}
	if list, _ := h.svc.ListSignups(ctx, 10); len(list) != 0 {
		t.Errorf("still waiting: %+v", list)
	}

	var got []string
	for _, l := range h.log(t) {
		if l.action == ActionApproveSignup || l.action == ActionRejectSignup || l.action == ActionSuspend {
			got = append(got, l.action+":"+l.reason)
		}
	}
	if want := []string{"approve_signup:Real bakery.", "reject_signup:Throwaway address.", "suspend:Throwaway address."}; !slices.Equal(got, want) {
		t.Errorf("logged %v, want %v", got, want)
	}

	if _, err := h.svc.ApproveSignup(ctx, h.staff, h.acc, " "); err == nil {
		t.Error("approved without a reason")
	}
	if _, err := h.svc.ApproveSignup(ctx, h.staff, "00000000-0000-0000-0000-000000000000", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
}

// TestRejectSuspendedSignup: rejecting an account that's suspended
// already marks it rejected and leaves the suspension as it was.
func TestRejectSuspendedSignup(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.waiting(t, h.acc)
	if _, err := h.svc.Suspend(ctx, h.staff, h.acc, "Phishing."); err != nil {
		t.Fatal(err)
	}
	acc, err := h.svc.RejectSignup(ctx, h.staff, h.acc, "Throwaway address.")
	if err != nil {
		t.Fatal(err)
	}
	if deref(acc.Review) != "rejected" || deref(acc.SuspendedReason) != "Phishing." {
		t.Errorf("= %+v", acc.AccountSummary)
	}
}
