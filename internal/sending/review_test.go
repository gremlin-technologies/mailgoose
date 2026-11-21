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
