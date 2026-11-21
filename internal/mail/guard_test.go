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

// TestGuardSharedDomain: mail on the shared sending domain (MG-85) goes only
// as its own account's slug, with Reply-To addresses that account verified.
func TestGuardSharedDomain(t *testing.T) {
	slugs := map[string]string{"acct-1": "pats-bakery", "acct-2": "other-shop"}
	verified := map[string]map[string]bool{"acct-1": {"pat@example.com": true}, "acct-2": {"sam@example.net": true}}
	calls := 0
	shared := func(_ context.Context, accountID, local string, replyTo []string) error {
		calls++
		if slugs[accountID] != strings.ToLower(local) {
			return errors.New("not the account's slug")
		}
		for _, r := range replyTo {
			if !verified[accountID][r] {
				return errors.New("unverified reply-to")
			}
		}
		return nil
	}
	refuse := func(context.Context, string, string) error { return errors.New("not verified") }
	msg := func(from, account string, replyTo ...string) Message {
		m := Message{From: Address{Email: from}, To: Address{Email: "to@example.com"}, Subject: "Hi", Text: "Hi",
			Kind: KindCampaign, AccountID: account}
		for _, r := range replyTo {
			m.ReplyTo = append(m.ReplyTo, Address{Email: r})
		}
		return m
	}
	guard := func() *Guard {
		return &Guard{Sender: &Fake{}, System: "mail.staging.mailgoose.app", Verified: refuse,
			Shared: "shared.staging.mailgoose.app", SharedSender: shared}
	}

	tests := []struct {
		name string
		m    Message
		ok   bool
	}{
		{"own slug, verified reply-to", msg("pats-bakery@shared.staging.mailgoose.app", "acct-1", "pat@example.com"), true},
		{"own slug any case", msg("Pats-Bakery@SHARED.staging.mailgoose.app", "acct-1", "Pat@Example.com "), true},
		{"own slug, no reply-to", msg("pats-bakery@shared.staging.mailgoose.app", "acct-1"), false},
		{"another account's slug", msg("other-shop@shared.staging.mailgoose.app", "acct-1", "pat@example.com"), false},
		{"another account's slug and its reply-to", msg("other-shop@shared.staging.mailgoose.app", "acct-1", "sam@example.net"), false},
		{"reply-to the account hasn't verified", msg("pats-bakery@shared.staging.mailgoose.app", "acct-1", "pat@example.com", "boss@example.org"), false},
		{"another account's verified reply-to", msg("pats-bakery@shared.staging.mailgoose.app", "acct-1", "sam@example.net"), false},
		{"no account", msg("pats-bakery@shared.staging.mailgoose.app", ""), false},
		{"subdomain of the shared domain", msg("pats-bakery@x.shared.staging.mailgoose.app", "acct-1"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &Fake{}
			g := guard()
			g.Sender = fake
			_, err := g.Send(context.Background(), tt.m)
			if tt.ok && (err != nil || len(fake.Sent()) != 1) {
				t.Fatalf("not sent: %v", err)
			}
			if !tt.ok && (!errors.Is(err, ErrNotThisEnvironment) || len(fake.Sent()) != 0) {
				t.Fatalf("err = %v, sent %d; want a refusal", err, len(fake.Sent()))
			}
		})
	}

	t.Run("off without a shared domain", func(t *testing.T) {
		g := guard()
		g.Shared = ""
		if err := g.Check(context.Background(), msg("pats-bakery@shared.staging.mailgoose.app", "acct-1", "pat@example.com")); !errors.Is(err, ErrNotThisEnvironment) {
			t.Fatalf("err = %v, want a refusal", err)
		}
	})

	t.Run("a remembered pass never lets another slug or reply-to through", func(t *testing.T) {
		calls = 0
		g := guard()
		ctx := context.Background()
		good := msg("pats-bakery@shared.staging.mailgoose.app", "acct-1", "pat@example.com")
		for range 3 {
			if err := g.Check(ctx, good); err != nil {
				t.Fatal(err)
			}
		}
		if calls != 1 {
			t.Fatalf("SharedSender called %d times within the TTL, want 1", calls)
		}
		for _, bad := range []Message{
			msg("other-shop@shared.staging.mailgoose.app", "acct-1", "pat@example.com"),
			msg("pats-bakery@shared.staging.mailgoose.app", "acct-1", "pat@example.com", "boss@example.org"),
			msg("pats-bakery@shared.staging.mailgoose.app", "acct-1", "boss@example.org"),
			msg("pats-bakery@shared.staging.mailgoose.app", "acct-2", "pat@example.com"),
		} {
			if err := g.Check(ctx, bad); err == nil {
				t.Errorf("%s from %s, reply-to %v passed after a good pass", bad.AccountID, bad.From.Email, bad.ReplyTo)
			}
		}
		// The same addresses in another order and case are the same pass.
		if err := g.Check(ctx, msg("PATS-BAKERY@shared.staging.mailgoose.app", "acct-1", "Pat@Example.com")); err != nil {
			t.Fatal(err)
		}
	})
}
