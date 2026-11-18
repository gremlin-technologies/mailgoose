package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// activeUser signs pat@example.com up and in, and returns the session.
func activeUser(t *testing.T, h *harness, email string) *Session {
	t.Helper()
	ctx := context.Background()
	if err := h.svc.SignUp(ctx, signUp(email)); err != nil {
		t.Fatal(err)
	}
	sess, err := h.svc.Verify(ctx, email, h.lastCode(t, email), SessionMeta{})
	if err != nil {
		t.Fatal(err)
	}
	h.clk.advance(ResendInterval)
	return sess
}

func TestRequestSignIn(t *testing.T) {
	ctx := context.Background()

	t.Run("active account gets a code that works once", func(t *testing.T) {
		h := newHarness(t)
		activeUser(t, h, "pat@example.com")
		if err := h.svc.RequestSignIn(ctx, "Pat@Example.com", "192.0.2.1"); err != nil {
			t.Fatal(err)
		}
		last, _ := h.mail.Last()
		if !strings.Contains(last.Message.Text, "already has an account") && !strings.Contains(last.Message.Text, "sign in") {
			t.Errorf("email = %q", last.Message.Text)
		}
		code := h.lastCode(t, "pat@example.com")
		if _, err := h.svc.Verify(ctx, "pat@example.com", code, SessionMeta{}); err != nil {
			t.Fatalf("sign-in code: %v", err)
		}
		if _, err := h.svc.Verify(ctx, "pat@example.com", code, SessionMeta{}); !errors.Is(err, ErrInvalidCode) {
			t.Errorf("reused sign-in code: err = %v", err)
		}
	})

	t.Run("code expires after 10 minutes", func(t *testing.T) {
		h := newHarness(t)
		activeUser(t, h, "pat@example.com")
		if err := h.svc.RequestSignIn(ctx, "pat@example.com", ""); err != nil {
			t.Fatal(err)
		}
		code := h.lastCode(t, "pat@example.com")
		h.clk.advance(CodeTTL)
		if _, err := h.svc.Verify(ctx, "pat@example.com", code, SessionMeta{}); !errors.Is(err, ErrInvalidCode) {
			t.Errorf("expired code: err = %v", err)
		}
	})

	t.Run("unknown address gets nothing", func(t *testing.T) {
		h := newHarness(t)
		if err := h.svc.RequestSignIn(ctx, "nobody@example.com", ""); err != nil {
			t.Fatal(err)
		}
		if len(h.mail.Sent()) != 0 {
			t.Error("emailed an address with no account")
		}
	})

	t.Run("pending account gets its sign-up code", func(t *testing.T) {
		h := newHarness(t)
		if err := h.svc.SignUp(ctx, signUp("pat@example.com")); err != nil {
			t.Fatal(err)
		}
		h.clk.advance(ResendInterval)
		if err := h.svc.RequestSignIn(ctx, "pat@example.com", ""); err != nil {
			t.Fatal(err)
		}
		last, _ := h.mail.Last()
		if !strings.Contains(last.Message.Text, "finish signing up") {
			t.Errorf("email = %q", last.Message.Text)
		}
	})
}

func TestCodeRequestLimits(t *testing.T) {
	ctx := context.Background()

	t.Run("per address, counted for unknown addresses too", func(t *testing.T) {
		h := newHarness(t)
		for i := range MaxCodeRequestsPerEmail {
			if err := h.svc.RequestSignIn(ctx, "nobody@example.com", fmt.Sprintf("192.0.2.%d", i)); err != nil {
				t.Fatalf("request %d: %v", i+1, err)
			}
			h.clk.advance(time.Second)
		}
		err := h.svc.RequestSignIn(ctx, "NOBODY@example.com", "198.51.100.1")
		var rl *RateLimitError
		if !errors.As(err, &rl) || rl.RetryAfter <= 0 || rl.RetryAfter > CodeRequestWindow {
			t.Fatalf("err = %v, want a RateLimitError", err)
		}
		// Sign-up counts against the same limit.
		if err := h.svc.SignUp(ctx, SignUpInput{Email: "nobody@example.com", AccountName: "N", AcceptedTerms: true, IP: "198.51.100.2"}); !errors.As(err, &rl) {
			t.Errorf("sign-up over the limit: err = %v", err)
		}
		h.clk.advance(CodeRequestWindow)
		if err := h.svc.RequestSignIn(ctx, "nobody@example.com", ""); err != nil {
			t.Errorf("after the window: %v", err)
		}
	})

	t.Run("per IP", func(t *testing.T) {
		h := newHarness(t)
		for i := range MaxCodeRequestsPerIP {
			if err := h.svc.RequestSignIn(ctx, fmt.Sprintf("p%d@example.com", i), "192.0.2.7"); err != nil {
				t.Fatalf("request %d: %v", i+1, err)
			}
		}
		var rl *RateLimitError
		if err := h.svc.RequestSignIn(ctx, "another@example.com", "192.0.2.7"); !errors.As(err, &rl) {
			t.Fatalf("err = %v, want a RateLimitError", err)
		}
		if err := h.svc.RequestSignIn(ctx, "another@example.com", "192.0.2.8"); err != nil {
			t.Errorf("another IP: %v", err)
		}
	})
}

func TestSessions(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	first := activeUser(t, h, "pat@example.com")
	if err := h.svc.RequestSignIn(ctx, "pat@example.com", ""); err != nil {
		t.Fatal(err)
	}
	second, err := h.svc.Verify(ctx, "pat@example.com", h.lastCode(t, "pat@example.com"), SessionMeta{})
	if err != nil {
		t.Fatal(err)
	}
	other := activeUser(t, h, "sam@example.com")

	cur, err := h.svc.SessionByToken(ctx, first.Token)
	if err != nil || cur.User.Email != "pat@example.com" || cur.Account.Status != "active" {
		t.Fatalf("SessionByToken = %+v, %v", cur, err)
	}
	if _, err := h.svc.SessionByToken(ctx, "not-a-token"); !errors.Is(err, ErrNoSession) {
		t.Errorf("unknown token: err = %v", err)
	}

	// Signing out one session leaves the other.
	if err := h.svc.SignOut(ctx, cur.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.SessionByToken(ctx, first.Token); !errors.Is(err, ErrNoSession) {
		t.Errorf("signed-out session: err = %v", err)
	}
	cur2, err := h.svc.SessionByToken(ctx, second.Token)
	if err != nil {
		t.Fatalf("second session should still work: %v", err)
	}

	// Everywhere ends all of this user's sessions and nobody else's.
	if err := h.svc.SignOutEverywhere(ctx, cur2.User.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.SessionByToken(ctx, second.Token); !errors.Is(err, ErrNoSession) {
		t.Errorf("after sign out everywhere: err = %v", err)
	}
	if _, err := h.svc.SessionByToken(ctx, other.Token); err != nil {
		t.Errorf("another user's session ended: %v", err)
	}

	// Sessions end after SessionTTL.
	h.clk.advance(SessionTTL)
	if _, err := h.svc.SessionByToken(ctx, other.Token); !errors.Is(err, ErrNoSession) {
		t.Errorf("expired session: err = %v", err)
	}
}
