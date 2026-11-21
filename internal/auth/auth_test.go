package auth

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/dbtest"
	mgmail "github.com/gremlin-technologies/mailgoose/internal/mail"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

type harness struct {
	svc  *Service
	db   *pgxpool.Pool
	mail *mgmail.Fake
	clk  *clock
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db := dbtest.New(t)
	h := &harness{db: db, mail: &mgmail.Fake{}, clk: &clock{t: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)}}
	h.svc = &Service{
		DB: db,
		// Behind the real guard, as in mailgoosed, so codes must pass it (MG-90).
		Mail: &mgmail.Guard{Sender: h.mail, System: "mail.test.example"},
		From: mgmail.Address{Name: "mailgoose", Email: "signin@mail.test.example"},
		Now:  h.clk.now,
	}
	return h
}

var codeRE = regexp.MustCompile(`\b\d{6}\b`)

// lastCode returns the code in the newest email to addr.
func (h *harness) lastCode(t *testing.T, addr string) string {
	t.Helper()
	sent := h.mail.Sent()
	for i := len(sent) - 1; i >= 0; i-- {
		m := sent[i].Message
		if m.To.Email == addr {
			code := codeRE.FindString(m.Text)
			if code == "" {
				t.Fatalf("no code in %q", m.Text)
			}
			return code
		}
	}
	t.Fatalf("no email to %s", addr)
	return ""
}

func (h *harness) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := h.db.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func signUp(email string) SignUpInput {
	return SignUpInput{Email: email, AccountName: "Pat's Bakery", AcceptedTerms: true}
}

func TestSignUpCreatesPendingFreeAccountAndEmailsCode(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := h.svc.SignUp(ctx, signUp("pat@example.com")); err != nil {
		t.Fatal(err)
	}

	var status, plan, name string
	var terms *time.Time
	if err := h.db.QueryRow(ctx, `SELECT a.status, a.plan, a.name, a.terms_accepted_at FROM accounts a JOIN users u ON u.account_id = a.id WHERE u.email = 'pat@example.com'`).
		Scan(&status, &plan, &name, &terms); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || plan != "free" || name != "Pat's Bakery" || terms == nil {
		t.Errorf("account = %s %s %q terms %v", status, plan, name, terms)
	}

	sent := h.mail.Sent()
	if len(sent) != 1 {
		t.Fatalf("sent %d emails, want 1", len(sent))
	}
	m := sent[0].Message
	if m.From.Email != "signin@mail.test.example" || m.Kind != mgmail.KindSystem || !strings.Contains(m.Text, "finish signing up") {
		t.Errorf("email = %+v", m)
	}
	code := h.lastCode(t, "pat@example.com")
	if !strings.Contains(m.Subject, code) || !strings.Contains(m.HTML, code) {
		t.Error("the code should be in the subject and the HTML part")
	}

	// Only a hash is stored.
	var hash []byte
	if err := h.db.QueryRow(ctx, `SELECT code_hash FROM auth_codes`).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if len(hash) != argonKeyLen || strings.Contains(string(hash), code) {
		t.Errorf("stored code_hash %x doesn't look like a hash", hash)
	}
}

func TestSignUpValidation(t *testing.T) {
	h := newHarness(t)
	tests := []struct {
		name string
		in   SignUpInput
		code string
	}{
		{"terms not accepted", SignUpInput{Email: "pat@example.com", AccountName: "Pat"}, "terms_not_accepted"},
		{"bad email", SignUpInput{Email: "pat", AccountName: "Pat", AcceptedTerms: true}, "invalid_email"},
		{"display name", SignUpInput{Email: "Pat <pat@example.com>", AccountName: "Pat", AcceptedTerms: true}, "invalid_email"},
		{"no dot in domain", SignUpInput{Email: "pat@localhost", AccountName: "Pat", AcceptedTerms: true}, "invalid_email"},
		{"empty name", SignUpInput{Email: "pat@example.com", AccountName: "  ", AcceptedTerms: true}, "invalid_account_name"},
		{"long name", SignUpInput{Email: "pat@example.com", AccountName: strings.Repeat("x", 101), AcceptedTerms: true}, "invalid_account_name"},
		// MG-98: names that could pass for someone else on the shared domain.
		{"brand name", SignUpInput{Email: "pat@example.com", AccountName: "PayPal Support", AcceptedTerms: true}, "account_name_not_allowed"},
		{"lookalike brand", SignUpInput{Email: "pat@example.com", AccountName: "Pаy-Pa1", AcceptedTerms: true}, "account_name_not_allowed"},
		{"payment term", SignUpInput{Email: "pat@example.com", AccountName: "Billing Department", AcceptedTerms: true}, "account_name_not_allowed"},
		{"name with @", SignUpInput{Email: "pat@example.com", AccountName: "pat@example.com", AcceptedTerms: true}, "account_name_not_allowed"},
		{"domain-shaped name", SignUpInput{Email: "pat@example.com", AccountName: "patsbakery.com", AcceptedTerms: true}, "account_name_not_allowed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := h.svc.SignUp(context.Background(), tt.in)
			var ve *ValidationError
			if !errors.As(err, &ve) || ve.Code != tt.code {
				t.Fatalf("err = %v, want %s", err, tt.code)
			}
			if ve.Code == "account_name_not_allowed" && (ve.Field != "account_name" || !strings.Contains(ve.Message, "shared domain")) {
				t.Errorf("refusal = %+v; want it on account_name, saying why", ve)
			}
		})
	}
	if n := h.count(t, `SELECT count(*) FROM accounts`); n != 0 {
		t.Errorf("%d accounts created by invalid sign-ups", n)
	}
	if len(h.mail.Sent()) != 0 {
		t.Error("invalid sign-ups sent email")
	}
}

func TestVerifyActivatesAndStartsSession(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := h.svc.SignUp(ctx, signUp("pat@example.com")); err != nil {
		t.Fatal(err)
	}
	code := h.lastCode(t, "pat@example.com")

	sess, err := h.svc.Verify(ctx, "PAT@example.com", code, SessionMeta{UserAgent: "test", IP: "192.0.2.1"})
	if err != nil {
		t.Fatal(err)
	}
	if sess.Account.Status != "active" || sess.Account.Plan != "free" || sess.User.Email != "pat@example.com" {
		t.Errorf("session = %+v", sess)
	}
	if len(sess.Token) < 40 || !sess.ExpiresAt.Equal(h.clk.t.Add(SessionTTL)) {
		t.Errorf("token %q expires %v", sess.Token, sess.ExpiresAt)
	}
	if n := h.count(t, `SELECT count(*) FROM sessions WHERE token_hash = $1`, TokenHash(sess.Token)); n != 1 {
		t.Error("session not stored by token hash")
	}
	if n := h.count(t, `SELECT count(*) FROM accounts WHERE status = 'active' AND activated_at IS NOT NULL`); n != 1 {
		t.Error("account not activated")
	}

	if _, err := h.svc.Verify(ctx, "pat@example.com", code, SessionMeta{}); !errors.Is(err, ErrInvalidCode) {
		t.Errorf("reused code: err = %v, want ErrInvalidCode", err)
	}
}

func TestVerifyRejectsExpiredWrongAndLockedCodes(t *testing.T) {
	ctx := context.Background()

	t.Run("expired", func(t *testing.T) {
		h := newHarness(t)
		if err := h.svc.SignUp(ctx, signUp("pat@example.com")); err != nil {
			t.Fatal(err)
		}
		code := h.lastCode(t, "pat@example.com")
		h.clk.advance(CodeTTL + time.Second)
		if _, err := h.svc.Verify(ctx, "pat@example.com", code, SessionMeta{}); !errors.Is(err, ErrInvalidCode) {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("locked after wrong tries", func(t *testing.T) {
		h := newHarness(t)
		if err := h.svc.SignUp(ctx, signUp("pat@example.com")); err != nil {
			t.Fatal(err)
		}
		code := h.lastCode(t, "pat@example.com")
		wrong := "000000"
		if code == wrong {
			wrong = "111111"
		}
		for range MaxCodeAttempts {
			if _, err := h.svc.Verify(ctx, "pat@example.com", wrong, SessionMeta{}); !errors.Is(err, ErrInvalidCode) {
				t.Fatalf("wrong code: err = %v", err)
			}
		}
		if _, err := h.svc.Verify(ctx, "pat@example.com", code, SessionMeta{}); !errors.Is(err, ErrInvalidCode) {
			t.Errorf("right code after %d wrong tries: err = %v, want ErrInvalidCode", MaxCodeAttempts, err)
		}
		if n := h.count(t, `SELECT count(*) FROM accounts WHERE status = 'pending'`); n != 1 {
			t.Error("account should still be pending")
		}
	})

	t.Run("unknown address and bad format", func(t *testing.T) {
		h := newHarness(t)
		for _, c := range []string{"123456", "12345", "abcdef"} {
			if _, err := h.svc.Verify(ctx, "nobody@example.com", c, SessionMeta{}); !errors.Is(err, ErrInvalidCode) {
				t.Errorf("%s: err = %v", c, err)
			}
		}
	})
}

func TestSignUpAgainWithActiveAddressSendsSignInCode(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := h.svc.SignUp(ctx, signUp("pat@example.com")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Verify(ctx, "pat@example.com", h.lastCode(t, "pat@example.com"), SessionMeta{}); err != nil {
		t.Fatal(err)
	}
	h.clk.advance(2 * time.Minute)

	in := signUp("Pat@Example.com")
	in.AccountName = "Someone Else"
	if err := h.svc.SignUp(ctx, in); err != nil {
		t.Fatalf("second sign-up should look the same as the first: %v", err)
	}
	if n := h.count(t, `SELECT count(*) FROM accounts`); n != 1 {
		t.Errorf("%d accounts, want 1", n)
	}
	if n := h.count(t, `SELECT count(*) FROM accounts WHERE name = 'Pat''s Bakery'`); n != 1 {
		t.Error("an active account's name must not change")
	}
	last, _ := h.mail.Last()
	if last.Message.To.Email != "pat@example.com" || !strings.Contains(last.Message.Text, "already has an account") {
		t.Errorf("email = %+v", last.Message)
	}
	if _, err := h.svc.Verify(ctx, "pat@example.com", h.lastCode(t, "pat@example.com"), SessionMeta{}); err != nil {
		t.Errorf("sign-in code should start a session: %v", err)
	}
}

func TestSignUpResendRules(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := h.svc.SignUp(ctx, signUp("pat@example.com")); err != nil {
		t.Fatal(err)
	}
	first := h.lastCode(t, "pat@example.com")

	// Within ResendInterval: same answer, no new email.
	h.clk.advance(10 * time.Second)
	if err := h.svc.SignUp(ctx, signUp("pat@example.com")); err != nil {
		t.Fatal(err)
	}
	if n := len(h.mail.Sent()); n != 1 {
		t.Fatalf("sent %d emails, want 1", n)
	}

	// After it: a new code replaces the old one, and the pending account
	// takes the newer name.
	h.clk.advance(ResendInterval)
	in := signUp("pat@example.com")
	in.AccountName = "Pat's Better Bakery"
	if err := h.svc.SignUp(ctx, in); err != nil {
		t.Fatal(err)
	}
	second := h.lastCode(t, "pat@example.com")
	if len(h.mail.Sent()) != 2 {
		t.Fatalf("sent %d emails, want 2", len(h.mail.Sent()))
	}
	if first != second {
		if _, err := h.svc.Verify(ctx, "pat@example.com", first, SessionMeta{}); !errors.Is(err, ErrInvalidCode) {
			t.Errorf("replaced code: err = %v", err)
		}
	}
	sess, err := h.svc.Verify(ctx, "pat@example.com", second, SessionMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if sess.Account.Name != "Pat's Better Bakery" {
		t.Errorf("name = %q", sess.Account.Name)
	}
}

func TestSignUpMailFailureLetsRetry(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.mail.Err = errors.New("ses down")
	if err := h.svc.SignUp(ctx, signUp("pat@example.com")); err == nil {
		t.Fatal("expected an error when the email can't be sent")
	}
	if n := h.count(t, `SELECT count(*) FROM auth_codes`); n != 0 {
		t.Errorf("%d unsent codes kept", n)
	}
	h.mail.Err = nil
	if err := h.svc.SignUp(ctx, signUp("pat@example.com")); err != nil {
		t.Fatal(err)
	}
	if len(h.mail.Sent()) != 1 {
		t.Error("retry right after a failed send should email a code")
	}
}
