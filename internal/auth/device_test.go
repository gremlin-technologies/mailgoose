package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestUserCodeFormat(t *testing.T) {
	for i := 0; i < 200; i++ {
		raw, err := newUserCode()
		if err != nil {
			t.Fatal(err)
		}
		shown := FormatUserCode(raw)
		if len(shown) != 9 || shown[4] != '-' {
			t.Fatalf("FormatUserCode(%q) = %q", raw, shown)
		}
		for _, r := range raw {
			if !strings.ContainsRune(userCodeLetters, r) {
				t.Fatalf("code %q has %q, outside the alphabet", raw, r)
			}
		}
		if got, ok := NormalizeUserCode(strings.ToLower(shown)); !ok || got != raw {
			t.Fatalf("NormalizeUserCode(%q) = %q, %v", strings.ToLower(shown), got, ok)
		}
	}
	for _, bad := range []string{"", "BCDF-GHJ", "BCDF-GHJKL", "BCDF-GHJA", "BCDF-GH1K"} {
		if _, ok := NormalizeUserCode(bad); ok {
			t.Errorf("NormalizeUserCode(%q) accepted", bad)
		}
	}
}

// deviceSetup signs up an active account and returns its session.
func deviceSetup(t *testing.T) (*harness, *Session) {
	t.Helper()
	h := newHarness(t)
	ctx := context.Background()
	if err := h.svc.SignUp(ctx, signUp("pat@example.com")); err != nil {
		t.Fatal(err)
	}
	sess, err := h.svc.Verify(ctx, "pat@example.com", h.lastCode(t, "pat@example.com"), SessionMeta{})
	if err != nil {
		t.Fatal(err)
	}
	return h, sess
}

func TestDeviceLoginGivesTokenOnce(t *testing.T) {
	h, sess := deviceSetup(t)
	ctx := context.Background()
	start, err := h.svc.StartDeviceLogin(ctx, "  pat-laptop \n")
	if err != nil {
		t.Fatal(err)
	}
	if !start.ExpiresAt.Equal(h.clk.t.Add(DeviceLoginTTL)) || start.Interval != DevicePollInterval {
		t.Errorf("start = %+v", start)
	}

	if _, err := h.svc.PollDeviceLogin(ctx, start.DeviceCode); !errors.Is(err, ErrDeviceLoginPending) {
		t.Fatalf("first poll: %v", err)
	}
	// Polling again right away is too fast.
	if _, err := h.svc.PollDeviceLogin(ctx, start.DeviceCode); !errors.Is(err, ErrDeviceLoginSlowDown) {
		t.Fatalf("fast poll: %v", err)
	}

	pending, err := h.svc.PendingDeviceLogin(ctx, strings.ToLower(start.UserCode))
	if err != nil || pending.ClientName != "pat-laptop" {
		t.Fatalf("pending = %+v, %v", pending, err)
	}
	if err := h.svc.DecideDeviceLogin(ctx, start.UserCode, sess.Account.ID, sess.User.ID, true); err != nil {
		t.Fatal(err)
	}
	// Once decided, the code no longer awaits approval.
	if err := h.svc.DecideDeviceLogin(ctx, start.UserCode, sess.Account.ID, sess.User.ID, false); !errors.Is(err, ErrDeviceLoginNotFound) {
		t.Errorf("second decision: %v", err)
	}

	h.clk.advance(DevicePollInterval)
	tok, err := h.svc.PollDeviceLogin(ctx, start.DeviceCode)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok.Token.Secret, "mg_cli_") || tok.Account.ID != sess.Account.ID || tok.User.Email != "pat@example.com" {
		t.Errorf("token = %+v", tok)
	}
	p, err := h.svc.Authenticate(ctx, tok.Token.Secret)
	if err != nil || p.Kind != KindCLIToken || p.Scope != ScopeFull {
		t.Fatalf("authenticate: %+v, %v", p, err)
	}

	// The token is given out once.
	h.clk.advance(DevicePollInterval)
	if _, err := h.svc.PollDeviceLogin(ctx, start.DeviceCode); !errors.Is(err, ErrDeviceLoginExpired) {
		t.Errorf("poll after token: %v", err)
	}

	if err := h.svc.RevokeCLIToken(ctx, sess.Account.ID, tok.Token.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Authenticate(ctx, tok.Token.Secret); err == nil {
		t.Error("revoked token still authenticates")
	}
}

func TestDeviceLoginDeniedExpiredUnknown(t *testing.T) {
	h, sess := deviceSetup(t)
	ctx := context.Background()

	denied, err := h.svc.StartDeviceLogin(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.svc.DecideDeviceLogin(ctx, denied.UserCode, sess.Account.ID, sess.User.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.PollDeviceLogin(ctx, denied.DeviceCode); !errors.Is(err, ErrDeviceLoginDenied) {
		t.Errorf("denied poll: %v", err)
	}

	expiring, err := h.svc.StartDeviceLogin(ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	h.clk.advance(DeviceLoginTTL)
	if _, err := h.svc.PendingDeviceLogin(ctx, expiring.UserCode); !errors.Is(err, ErrDeviceLoginNotFound) {
		t.Errorf("expired code on approval page: %v", err)
	}
	if err := h.svc.DecideDeviceLogin(ctx, expiring.UserCode, sess.Account.ID, sess.User.ID, true); !errors.Is(err, ErrDeviceLoginNotFound) {
		t.Errorf("approving an expired code: %v", err)
	}
	if _, err := h.svc.PollDeviceLogin(ctx, expiring.DeviceCode); !errors.Is(err, ErrDeviceLoginExpired) {
		t.Errorf("expired poll: %v", err)
	}
	if _, err := h.svc.PollDeviceLogin(ctx, "nonsense"); !errors.Is(err, ErrDeviceLoginExpired) {
		t.Errorf("unknown device code: %v", err)
	}

	// A day after expiry, the next start clears old requests.
	h.clk.advance(25 * time.Hour)
	if _, err := h.svc.StartDeviceLogin(ctx, "c"); err != nil {
		t.Fatal(err)
	}
	if n := h.count(t, `SELECT count(*) FROM device_logins`); n != 1 {
		t.Errorf("%d device logins left, want 1", n)
	}
}

// A login approved before the account was suspended gives no token (MG-65),
// and a suspended account's sessions and keys say so.
func TestDeviceLoginSuspendedAccount(t *testing.T) {
	h, sess := deviceSetup(t)
	ctx := context.Background()
	start, err := h.svc.StartDeviceLogin(ctx, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.svc.DecideDeviceLogin(ctx, start.UserCode, sess.Account.ID, sess.User.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(ctx, `UPDATE accounts SET suspended_at = now(), suspended_reason = 'Spam.', suspended_by = 'test'`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.PollDeviceLogin(ctx, start.DeviceCode); !errors.Is(err, ErrDeviceLoginDenied) {
		t.Errorf("poll: %v", err)
	}
	cur, err := h.svc.SessionByToken(ctx, sess.Token)
	if err != nil || cur.Account.Suspension == nil || cur.Account.Suspension.Reason != "Spam." {
		t.Errorf("session: %+v, %v", cur, err)
	}
}
