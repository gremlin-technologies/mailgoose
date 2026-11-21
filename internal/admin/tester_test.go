package admin

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/tester"
)

// TestTesterGrantExtendRevoke: an admin makes an approved account a tester
// with full sending, extends it keeping its start, and revokes it into test
// mode, each logged (MG-86).
func TestTesterGrantExtendRevoke(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.db.Exec(ctx, `UPDATE accounts SET sending_mode = 'test' WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}

	acc, err := h.svc.GrantTester(ctx, h.staff, h.acc, nil, 0, "Invited to test mailgoose.")
	if err != nil {
		t.Fatal(err)
	}
	st := acc.Tester
	if st == nil || st.Credits != tester.Credits || st.Used != 0 || st.Left != tester.Credits || st.Ended ||
		!st.StartedAt.Equal(h.now) || !st.Until.Equal(h.now.AddDate(0, 0, tester.DefaultDays)) {
		t.Fatalf("granted %+v", st)
	}
	if acc.SendingMode != "full" {
		t.Errorf("sending mode %q", acc.SendingMode)
	}

	// Extending keeps the start, so credits already used stay used.
	start := h.now
	h.now = h.now.AddDate(0, 0, 10)
	if _, err := h.db.Exec(ctx, `INSERT INTO test_sends (account_id, sent_at) VALUES ($1, $2)`, h.acc, h.now); err != nil {
		t.Fatal(err)
	}
	until := h.now.AddDate(0, 2, 0)
	acc, err = h.svc.GrantTester(ctx, h.staff, h.acc, &until, 800, "More time.")
	if err != nil {
		t.Fatal(err)
	}
	if st := acc.Tester; st == nil || !st.StartedAt.Equal(start) || !st.Until.Equal(until) || st.Credits != 800 || st.Used != 1 {
		t.Fatalf("extended %+v", st)
	}

	acc, err = h.svc.RevokeTester(ctx, h.staff, h.acc, "Testing finished.")
	if err != nil {
		t.Fatal(err)
	}
	if acc.Tester != nil || acc.SendingMode != "test" {
		t.Errorf("revoked: tester %+v, mode %q", acc.Tester, acc.SendingMode)
	}
	if _, err := h.svc.RevokeTester(ctx, h.staff, h.acc, "Again."); !errors.Is(err, ErrNotTester) {
		t.Errorf("revoke again: %v", err)
	}

	// A new grant starts afresh.
	acc, err = h.svc.GrantTester(ctx, h.staff, h.acc, nil, 0, "Back again.")
	if err != nil {
		t.Fatal(err)
	}
	if st := acc.Tester; st == nil || !st.StartedAt.Equal(h.now) || st.Used != 1 {
		// The test send at h.now counts: it's at the new start.
		t.Errorf("granted again %+v", st)
	}

	var got []string
	for _, l := range h.log(t) {
		if l.action == ActionGrantTester || l.action == ActionRevokeTester {
			got = append(got, l.action+":"+l.reason)
		}
	}
	want := []string{"grant_tester:Invited to test mailgoose.", "grant_tester:More time.", "revoke_tester:Testing finished.", "grant_tester:Back again."}
	if len(got) != len(want) {
		t.Fatalf("logged %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("logged %v, want %v", got, want)
			break
		}
	}
}

// TestTesterGrantRefused: bad ends and credit counts, accounts not approved
// or suspended, and unknown accounts are refused.
func TestTesterGrantRefused(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	past := h.now.Add(-time.Minute)
	far := h.now.AddDate(0, 0, tester.MaxDays+1)
	for name, c := range map[string]struct {
		until   *time.Time
		credits int
		field   string
	}{
		"past":       {&past, 0, "until"},
		"too far":    {&far, 0, "until"},
		"no credits": {nil, -1, "credits"},
		"too many":   {nil, tester.MaxCredits + 1, "credits"},
	} {
		_, err := h.svc.GrantTester(ctx, h.staff, h.acc, c.until, c.credits, "x")
		var ie *InputError
		if !errors.As(err, &ie) || ie.Field != c.field {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := h.svc.GrantTester(ctx, h.staff, h.acc, nil, 0, " "); err == nil {
		t.Error("granted without a reason")
	}
	for _, review := range []string{"waiting", "rejected"} {
		if _, err := h.db.Exec(ctx, `UPDATE accounts SET review = $2, review_at = now() WHERE id = $1`, h.acc, review); err != nil {
			t.Fatal(err)
		}
		if _, err := h.svc.GrantTester(ctx, h.staff, h.acc, nil, 0, "x"); !errors.Is(err, ErrNotApproved) {
			t.Errorf("review %s: %v", review, err)
		}
	}
	if _, err := h.db.Exec(ctx, `UPDATE accounts SET review = 'approved', suspended_at = now(), suspended_reason = 'Spam.', suspended_by = 'admin' WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.GrantTester(ctx, h.staff, h.acc, nil, 0, "x"); !errors.Is(err, ErrSuspended) {
		t.Errorf("suspended: %v", err)
	}
	unknown := "00000000-0000-0000-0000-000000000000"
	if _, err := h.svc.GrantTester(ctx, h.staff, unknown, nil, 0, "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown grant: %v", err)
	}
	if _, err := h.svc.RevokeTester(ctx, h.staff, unknown, "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown revoke: %v", err)
	}
}

// TestEndedTesterInAdminViews: the account view and the find results say
// when a tester's period ends, so an ended one shows as not sending
// (MG-107); a revoked one has no end.
func TestEndedTesterInAdminViews(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	until := h.now.AddDate(0, 0, 1)
	if _, err := h.svc.GrantTester(ctx, h.staff, h.acc, &until, 0, "Invited to test mailgoose."); err != nil {
		t.Fatal(err)
	}
	h.now = h.now.AddDate(0, 0, 2)
	acc, err := h.svc.GetAccount(ctx, h.acc)
	if err != nil {
		t.Fatal(err)
	}
	if acc.Tester == nil || !acc.Tester.Ended || acc.TesterUntil == nil || !acc.TesterUntil.Equal(until) {
		t.Errorf("account: tester %+v, until %v", acc.Tester, acc.TesterUntil)
	}
	found, err := h.svc.FindAccounts(ctx, "patsbakery", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].TesterUntil == nil || !found[0].TesterUntil.Equal(until) {
		t.Errorf("find = %+v", found)
	}
	if acc, err = h.svc.RevokeTester(ctx, h.staff, h.acc, "Testing finished."); err != nil {
		t.Fatal(err)
	}
	if acc.TesterUntil != nil {
		t.Errorf("revoked, until %v", acc.TesterUntil)
	}
}
