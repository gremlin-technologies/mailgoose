package auth

import (
	"context"
	"testing"
)

// TestNewAccountSendingMode: new accounts start in test mode unless the
// server's setting says full sending (MG-80), and every way of reading the
// signed-in account says which.
func TestNewAccountSendingMode(t *testing.T) {
	for _, c := range []struct {
		setting, want string
	}{
		{"", SendingTest}, // the default is test mode
		{SendingTest, SendingTest},
		{SendingFull, SendingFull},
	} {
		t.Run("setting "+c.setting, func(t *testing.T) {
			h := newHarness(t)
			h.svc.NewAccountSending = c.setting
			ctx := context.Background()
			if err := h.svc.SignUp(ctx, signUp("pat@example.com")); err != nil {
				t.Fatal(err)
			}
			sess, err := h.svc.Verify(ctx, "pat@example.com", h.lastCode(t, "pat@example.com"), SessionMeta{})
			if err != nil {
				t.Fatal(err)
			}
			if sess.Account.SendingMode != c.want {
				t.Errorf("signed in: mode %q, want %q", sess.Account.SendingMode, c.want)
			}
			cur, err := h.svc.SessionByToken(ctx, sess.Token)
			if err != nil {
				t.Fatal(err)
			}
			if cur.Account.SendingMode != c.want {
				t.Errorf("session: mode %q, want %q", cur.Account.SendingMode, c.want)
			}
			acc, _, err := h.svc.Identity(ctx, sess.Account.ID, sess.User.ID)
			if err != nil {
				t.Fatal(err)
			}
			if acc.SendingMode != c.want {
				t.Errorf("identity: mode %q, want %q", acc.SendingMode, c.want)
			}
		})
	}
}

// TestOlderAccountsKeepFullSending: an account made without the sign-up
// (as every account before MG-80 was) has full sending.
func TestOlderAccountsKeepFullSending(t *testing.T) {
	h := newHarness(t)
	if n := h.count(t, `WITH a AS (INSERT INTO accounts (name, status) VALUES ('Old', 'active') RETURNING sending_mode)
		SELECT count(*) FROM a WHERE sending_mode = 'full'`); n != 1 {
		t.Fatal("an account made without a mode isn't on full sending")
	}
}
