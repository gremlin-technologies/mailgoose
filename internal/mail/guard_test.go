package mail

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestGuard(t *testing.T) {
	errRefused := errors.New("not verified")
	verified := map[string]string{"acct-1": "acme.com"}
	calls := 0
	check := func(_ context.Context, accountID, from string) error {
		calls++
		domain := from[strings.LastIndexByte(from, '@')+1:]
		if d, ok := verified[accountID]; ok && (domain == d || strings.HasSuffix(domain, "."+d)) {
			return nil
		}
		return errRefused
	}
	msg := func(from, account string) Message {
		return Message{From: Address{Email: from}, To: Address{Email: "to@example.com"}, Subject: "Hi", Text: "Hi",
			Kind: KindCampaign, AccountID: account}
	}

	tests := []struct {
		name string
		m    Message
		ok   bool
	}{
		{"system mail", msg("signin@mail.staging.mailgoose.app", ""), true},
		{"system domain any case, with an account", msg("alerts@MAIL.staging.mailgoose.app", "acct-1"), true},
		{"subdomain of the system domain", msg("x@evil.mail.staging.mailgoose.app", ""), false},
		{"the other environment's system domain", msg("signin@mail.mailgoose.app", ""), false},
		{"verified domain of the account", msg("news@acme.com", "acct-1"), true},
		{"subdomain of a verified domain", msg("news@send.acme.com", "acct-1"), true},
		{"domain the account hasn't verified", msg("news@other.com", "acct-1"), false},
		{"another account's domain", msg("news@acme.com", "acct-2"), false},
		{"customer domain with no account", msg("news@acme.com", ""), false},
		{"no at sign", msg("acme.com", "acct-1"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &Fake{}
			g := &Guard{Sender: fake, System: "mail.staging.mailgoose.app", Verified: check}
			_, err := g.Send(context.Background(), tt.m)
			if tt.ok && (err != nil || len(fake.Sent()) != 1) {
				t.Fatalf("not sent: %v", err)
			}
			if !tt.ok {
				if !errors.Is(err, ErrNotThisEnvironment) {
					t.Fatalf("err = %v, want ErrNotThisEnvironment", err)
				}
				if n := len(fake.Sent()); n != 0 {
					t.Fatalf("sent %d messages after a refusal", n)
				}
			}
		})
	}

	t.Run("passes are remembered for the TTL, refusals never", func(t *testing.T) {
		calls = 0
		now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
		g := &Guard{Sender: &Fake{}, System: "mail.staging.mailgoose.app", Verified: check, TTL: time.Minute,
			Now: func() time.Time { return now }}
		ctx := context.Background()
		for range 3 {
			if err := g.Check(ctx, msg("news@acme.com", "acct-1")); err != nil {
				t.Fatal(err)
			}
		}
		if calls != 1 {
			t.Fatalf("Verified called %d times within the TTL, want 1", calls)
		}
		now = now.Add(61 * time.Second)
		if err := g.Check(ctx, msg("news@acme.com", "acct-1")); err != nil {
			t.Fatal(err)
		}
		if calls != 2 {
			t.Fatalf("Verified called %d times after the TTL, want 2", calls)
		}
		for range 2 {
			if err := g.Check(ctx, msg("news@other.com", "acct-1")); err == nil {
				t.Fatal("unverified domain passed")
			}
		}
		if calls != 4 {
			t.Fatalf("Verified called %d times, want 4: a refusal must not be remembered", calls)
		}
	})

	t.Run("a cancelled context isn't reported as a refusal", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		g := &Guard{Sender: &Fake{}, Verified: func(ctx context.Context, _, _ string) error { return ctx.Err() }}
		err := g.Check(ctx, msg("news@acme.com", "acct-1"))
		if !errors.Is(err, context.Canceled) || errors.Is(err, ErrNotThisEnvironment) {
			t.Fatalf("err = %v, want context.Canceled only", err)
		}
	})
}
