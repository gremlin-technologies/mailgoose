package sending

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/gremlin-technologies/mailgoose/internal/auth"
	"github.com/gremlin-technologies/mailgoose/internal/mail"
)

// TestWaitingAccountTestsButCantLaunch: an account waiting for its sign-up
// review (MG-66) can send tests to its own addresses, but not launch, until
// it's approved.
func TestWaitingAccountTestsButCantLaunch(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.db.Exec(ctx, `UPDATE accounts SET review = 'waiting', review_at = now() WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	h.buy(t, 10)
	if _, err := h.test(); err != nil {
		t.Fatalf("test send while waiting: %v", err)
	}
	_, err := h.launch("")
	var nr *NotReadyError
	if !errors.As(err, &nr) || problemCodes(err) != AccountInReview {
		t.Fatalf("launch while waiting: %v (%s)", err, problemCodes(err))
	}
	if _, err := h.db.Exec(ctx, `UPDATE accounts SET review = 'approved' WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	if _, err := h.launch(h.summary(t).Code); err != nil {
		t.Fatalf("launch once approved: %v", err)
	}
}

// TestRejectedUnsuspendedAccountCantLaunch: a rejected sign-up whose
// suspension was lifted (MG-106) still can't launch, at the summary or at
// the confirm, and the refusal says why; approving the sign-up lets it.
func TestRejectedUnsuspendedAccountCantLaunch(t *testing.T) {
	h := readyHarness(t)
	ctx := context.Background()
	// A code issued before the rejection doesn't get past it either.
	code := h.summary(t).Code
	// As `mailgoose admin reject-signup` then `unsuspend` leave it.
	if _, err := h.db.Exec(ctx, `UPDATE accounts SET review = 'rejected', review_at = now() WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"", code} {
		_, err := h.launch(c)
		var nr *NotReadyError
		if !errors.As(err, &nr) || problemCodes(err) != SignupRejected {
			t.Fatalf("launch with code %q while rejected: %v (%s)", c, err, problemCodes(err))
		}
		if p := nr.Problems[0]; !strings.Contains(p.Message, "sign-up review was rejected") || p.Hint == "" {
			t.Errorf("problem = %+v", p)
		}
	}
	if n := h.counts(t); len(n) != 0 {
		t.Errorf("rows queued while rejected: %v", n)
	}
	// Suspended as well, it's the suspension that's reported, alone.
	if _, err := h.db.Exec(ctx, `UPDATE accounts SET suspended_at = now(), suspended_reason = 'Throwaway address.', suspended_by = 'test'
		WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	if _, err := h.launch(""); problemCodes(err) != AccountSuspended {
		t.Errorf("rejected and suspended: %s", problemCodes(err))
	}
	if _, err := h.db.Exec(ctx, `UPDATE accounts SET suspended_at = NULL, suspended_reason = NULL, suspended_by = NULL,
		review = 'approved' WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	if _, err := h.launch(h.summary(t).Code); err != nil {
		t.Fatalf("launch once approved: %v", err)
	}
	if n := h.counts(t); n["queued"] != 1 {
		t.Errorf("launched rows: %v", n)
	}
}

// TestTestModeRejected: a rejected test-mode account, like a waiting one,
// may still launch to its test addresses (MG-106).
func TestTestModeRejected(t *testing.T) {
	h := readyHarness(t)
	if _, err := h.db.Exec(context.Background(), `UPDATE accounts SET review = 'rejected', review_at = now() WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	h.testMode(t)
	h.testAddress(t, "sam@patsbakery.com", true)
	if _, err := h.launch(h.summary(t).Code); err != nil {
		t.Fatalf("test mode, rejected, to test addresses: %v", err)
	}
}

func TestSignupWaitingAlert(t *testing.T) {
	fake := &mail.Fake{}
	a := &OpsAlert{Mail: &mail.Guard{Sender: fake, System: "mail.mailgoose.test"}, From: mail.Address{Name: "mailgoose", Email: "alerts@mail.mailgoose.test"}, To: "ops@example.com",
		Log: slog.New(slog.DiscardHandler)}
	a.SignupWaiting(context.Background(), auth.Waiting{AccountID: "acc-1", AccountName: "Pat's Bakery", Email: "pat@mailinator.com",
		Flags: []string{auth.FlagDisposableDomain, auth.FlagIPBurst}})
	m, ok := fake.Last()
	if !ok || m.Message.To.Email != "ops@example.com" || m.Message.Subject != "mailgoose: Pat's Bakery is waiting for review" ||
		!strings.Contains(m.Message.Text, "Flags: disposable_domain, ip_burst") ||
		!strings.Contains(m.Message.Text, "mailgoose admin approve-signup acc-1") ||
		!strings.Contains(m.Message.Text, "pat@mailinator.com") {
		t.Errorf("alert = %+v", m.Message)
	}
	a.SignupWaiting(context.Background(), auth.Waiting{AccountID: "acc-2", AccountName: "Lee's Shop", Email: "lee@shop.example"})
	if m, _ := fake.Last(); !strings.Contains(m.Message.Text, "Flags: none (every new account is reviewed)") {
		t.Errorf("unflagged alert = %s", m.Message.Text)
	}
}
