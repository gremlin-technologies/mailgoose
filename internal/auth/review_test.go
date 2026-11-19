package auth

import (
	"context"
	"slices"
	"testing"
	"time"
)

// reviewOf is the account's review state and flags, for the user at email.
func (h *harness) reviewOf(t *testing.T, email string) (review *string, flags []string) {
	t.Helper()
	if err := h.db.QueryRow(context.Background(), `
		SELECT a.review, a.review_flags FROM accounts a JOIN users u ON u.account_id = a.id WHERE u.email = $1`,
		email).Scan(&review, &flags); err != nil {
		t.Fatal(err)
	}
	return review, flags
}

func (h *harness) signUpFrom(t *testing.T, email, ip string) {
	t.Helper()
	in := signUp(email)
	in.IP = ip
	if err := h.svc.SignUp(context.Background(), in); err != nil {
		t.Fatalf("sign up %s: %v", email, err)
	}
}

// suspendAccountOf suspends the account of the user at email.
func (h *harness) suspendAccountOf(t *testing.T, email string) {
	t.Helper()
	if _, err := h.db.Exec(context.Background(), `
		UPDATE accounts SET suspended_at = now(), suspended_reason = 'Spam.', suspended_by = 'test'
		WHERE id = (SELECT account_id FROM users WHERE email = $1)`, email); err != nil {
		t.Fatal(err)
	}
}

// TestSignupFlags checks each of the review's flags (MG-66).
func TestSignupFlags(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, h *harness)
		email string
		ip    string
		want  []string
	}{
		{name: "clean", email: "pat@patsbakery.example", ip: "192.0.2.1", want: []string{}},
		{name: "disposable domain", email: "pat@mailinator.com", ip: "192.0.2.1", want: []string{FlagDisposableDomain}},
		{name: "disposable subdomain", email: "pat@x.mailinator.com", want: []string{FlagDisposableDomain}},
		{name: "fourth from one IP in a day", setup: func(t *testing.T, h *harness) {
			for _, e := range []string{"a@one.example", "b@two.example", "c@three.example"} {
				h.signUpFrom(t, e, "192.0.2.9")
			}
		}, email: "d@four.example", ip: "192.0.2.9", want: []string{FlagIPBurst}},
		{name: "third from one IP is fine", setup: func(t *testing.T, h *harness) {
			for _, e := range []string{"a@one.example", "b@two.example"} {
				h.signUpFrom(t, e, "192.0.2.9")
			}
		}, email: "d@four.example", ip: "192.0.2.9", want: []string{}},
		{name: "three from one IP a day ago", setup: func(t *testing.T, h *harness) {
			for _, e := range []string{"a@one.example", "b@two.example", "c@three.example"} {
				h.signUpFrom(t, e, "192.0.2.9")
			}
			h.clk.advance(SignupIPWindow + time.Minute)
		}, email: "d@four.example", ip: "192.0.2.9", want: []string{}},
		{name: "IP of a suspended account", setup: func(t *testing.T, h *harness) {
			h.signUpFrom(t, "spam@spammer.example", "198.51.100.7")
			h.suspendAccountOf(t, "spam@spammer.example")
			h.clk.advance(40 * 24 * time.Hour)
		}, email: "pat@patsbakery.example", ip: "198.51.100.7", want: []string{FlagSuspendedIP}},
		{name: "company domain of a suspended account", setup: func(t *testing.T, h *harness) {
			h.signUpFrom(t, "spam@Spammer.example", "198.51.100.7")
			h.suspendAccountOf(t, "spam@Spammer.example")
		}, email: "other@spammer.example", ip: "192.0.2.1", want: []string{FlagSuspendedDomain}},
		{name: "free-mail domain of a suspended account", setup: func(t *testing.T, h *harness) {
			h.signUpFrom(t, "spam@gmail.com", "198.51.100.7")
			h.suspendAccountOf(t, "spam@gmail.com")
		}, email: "pat@gmail.com", ip: "192.0.2.1", want: []string{}},
		{name: "several", setup: func(t *testing.T, h *harness) {
			h.signUpFrom(t, "spam@yopmail.com", "198.51.100.7")
			h.suspendAccountOf(t, "spam@yopmail.com")
		}, email: "again@yopmail.com", ip: "198.51.100.7", want: []string{FlagDisposableDomain, FlagSuspendedIP, FlagSuspendedDomain}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.svc.SignupReview = ReviewFlagged
			if c.setup != nil {
				c.setup(t, h)
			}
			h.signUpFrom(t, c.email, c.ip)
			review, flags := h.reviewOf(t, c.email)
			if !slices.Equal(flags, c.want) {
				t.Errorf("flags %v, want %v", flags, c.want)
			}
			if waiting := review != nil && *review == "waiting"; waiting != (len(c.want) > 0) {
				t.Errorf("review %v with flags %v", review, flags)
			}
		})
	}
}

// TestSignupReviewSetting checks all, flagged and off (MG-66), and that a
// waiting account says so once it's signed in, and the operators hear of
// it then.
func TestSignupReviewSetting(t *testing.T) {
	for _, c := range []struct {
		setting               string
		cleanWaits, flagWaits bool
	}{
		{"", true, true}, // the default is all
		{ReviewAll, true, true},
		{ReviewFlagged, false, true},
		{ReviewOff, false, false},
	} {
		t.Run("setting "+c.setting, func(t *testing.T) {
			h := newHarness(t)
			h.svc.SignupReview = c.setting
			var heard []Waiting
			h.svc.OnWaiting = func(_ context.Context, w Waiting) { heard = append(heard, w) }
			ctx := context.Background()
			for _, x := range []struct {
				email string
				wait  bool
			}{{"pat@patsbakery.example", c.cleanWaits}, {"pat@mailinator.com", c.flagWaits}} {
				h.signUpFrom(t, x.email, "192.0.2.1")
				review, _ := h.reviewOf(t, x.email)
				if waiting := review != nil && *review == "waiting"; waiting != x.wait {
					t.Errorf("%s: review %v, want waiting %v", x.email, review, x.wait)
				}
				if len(heard) != 0 {
					t.Errorf("%s: operators told before the address was confirmed", x.email)
				}
				sess, err := h.svc.Verify(ctx, x.email, h.lastCode(t, x.email), SessionMeta{})
				if err != nil {
					t.Fatal(err)
				}
				if sess.Account.InReview != x.wait {
					t.Errorf("%s: InReview %v", x.email, sess.Account.InReview)
				}
				if x.wait {
					if len(heard) != 1 || heard[0].AccountID != sess.Account.ID || heard[0].Email != x.email {
						t.Errorf("%s: heard %+v", x.email, heard)
					}
				} else if len(heard) != 0 {
					t.Errorf("%s: heard %+v for an account that isn't waiting", x.email, heard)
				}
				heard = nil
				// Signing in again tells no one again.
				h.clk.advance(ResendInterval)
				if err := h.svc.RequestSignIn(ctx, x.email, ""); err != nil {
					t.Fatal(err)
				}
				if _, err := h.svc.Verify(ctx, x.email, h.lastCode(t, x.email), SessionMeta{}); err != nil {
					t.Fatal(err)
				}
				if len(heard) != 0 {
					t.Errorf("%s: heard again on a later sign-in", x.email)
				}
			}
		})
	}
}

// TestPruneSignupIPs: sign-up IPs go after 90 days (MG-66).
func TestPruneSignupIPs(t *testing.T) {
	h := newHarness(t)
	h.svc.SignupReview = ReviewOff
	h.signUpFrom(t, "old@one.example", "192.0.2.1")
	h.clk.advance(SignupIPRetention - time.Hour)
	h.signUpFrom(t, "new@two.example", "192.0.2.2")
	h.signUpFrom(t, "none@three.example", "")
	n, err := PruneSignupIPs(context.Background(), h.db, h.clk.now().Add(2*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	if got := h.count(t, `SELECT count(*) FROM signup_ips WHERE ip = '192.0.2.2'`); got != 1 ||
		h.count(t, `SELECT count(*) FROM signup_ips`) != 1 {
		t.Errorf("left %d", h.count(t, `SELECT count(*) FROM signup_ips`))
	}
}
