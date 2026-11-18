package auth

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"
)

var secretRE = regexp.MustCompile(`^mg_(test|live|cli)_[a-z2-7]{32}$`)

func TestCreateKey(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	sess := activeUser(t, h, "pat@example.com")

	k, err := h.svc.CreateKey(ctx, sess.Account.ID, sess.User.ID, "  laptop  ", ScopeDraft)
	if err != nil {
		t.Fatal(err)
	}
	if !secretRE.MatchString(k.Secret) || k.Secret[:8] != "mg_test_" {
		t.Errorf("secret = %q", k.Secret)
	}
	if k.Name != "laptop" || k.Scope != ScopeDraft || k.Hint != "mg_test_…"+k.Secret[len(k.Secret)-4:] || k.ID == "" {
		t.Errorf("key = %+v", k.Key)
	}
	if n := h.count(t, `SELECT count(*) FROM api_keys WHERE secret_hash = $1`, TokenHash(k.Secret)); n != 1 {
		t.Error("the secret's hash isn't stored")
	}
	if n := h.count(t, `SELECT count(*) FROM api_keys WHERE secret_hint = $1 OR name = $1`, k.Secret); n != 0 {
		t.Error("the secret itself is stored")
	}

	h.svc.LiveKeys = true
	live, err := h.svc.CreateKey(ctx, sess.Account.ID, sess.User.ID, "prod", ScopeFull)
	if err != nil || live.Secret[:8] != "mg_live_" {
		t.Errorf("live key = %v, %v", live, err)
	}

	for name, tc := range map[string]struct{ name, scope, code string }{
		"no name":     {"   ", ScopeFull, "invalid_name"},
		"long name":   {string(make([]byte, 101)), ScopeFull, "invalid_name"},
		"wrong scope": {"x", "admin", "invalid_scope"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := h.svc.CreateKey(ctx, sess.Account.ID, sess.User.ID, tc.name, tc.scope)
			var ve *ValidationError
			if !errors.As(err, &ve) || ve.Code != tc.code {
				t.Errorf("err = %v, want %s", err, tc.code)
			}
		})
	}
}

func TestKeyCap(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	sess := activeUser(t, h, "pat@example.com")
	var last *NewKey
	for i := range MaxKeysPerAccount {
		k, err := h.svc.CreateKey(ctx, sess.Account.ID, sess.User.ID, "k", ScopeFull)
		if err != nil {
			t.Fatalf("key %d: %v", i+1, err)
		}
		last = k
	}
	var ve *ValidationError
	if _, err := h.svc.CreateKey(ctx, sess.Account.ID, sess.User.ID, "one more", ScopeFull); !errors.As(err, &ve) || ve.Code != "too_many_keys" {
		t.Fatalf("over the cap: err = %v", err)
	}
	// CLI tokens don't count, and revoking frees a place.
	if _, err := h.svc.CreateCLIToken(ctx, sess.Account.ID, sess.User.ID, "cli"); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.RevokeKey(ctx, sess.Account.ID, last.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.CreateKey(ctx, sess.Account.ID, sess.User.ID, "one more", ScopeFull); err != nil {
		t.Errorf("after revoking one: %v", err)
	}
}

func TestListKeys(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	sess := activeUser(t, h, "pat@example.com")
	other := activeUser(t, h, "sam@example.com")
	var ids []string
	for _, name := range []string{"a", "b", "c"} {
		k, err := h.svc.CreateKey(ctx, sess.Account.ID, sess.User.ID, name, ScopeFull)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, k.ID)
		h.clk.advance(time.Second)
	}
	if _, err := h.svc.CreateKey(ctx, other.Account.ID, other.User.ID, "theirs", ScopeFull); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.CreateCLIToken(ctx, sess.Account.ID, sess.User.ID, "cli"); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.RevokeKey(ctx, sess.Account.ID, ids[1]); err != nil {
		t.Fatal(err)
	}

	page, err := h.svc.ListKeys(ctx, sess.Account.ID, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 || page[0].Name != "a" || page[1].Name != "c" {
		t.Fatalf("list = %+v, want a and c (revoked, CLI and other accounts' keys left out)", page)
	}
	next, err := h.svc.ListKeys(ctx, sess.Account.ID, page[0].ID, 10)
	if err != nil || len(next) != 1 || next[0].Name != "c" {
		t.Errorf("after a: %+v, %v", next, err)
	}
	// A cursor from another account's key doesn't leak anything.
	theirs, _ := h.svc.ListKeys(ctx, other.Account.ID, "", 10)
	if got, err := h.svc.ListKeys(ctx, sess.Account.ID, theirs[0].ID, 10); err != nil || len(got) != 0 {
		t.Errorf("foreign cursor: %+v, %v", got, err)
	}
}

func TestRevokeKey(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	sess := activeUser(t, h, "pat@example.com")
	other := activeUser(t, h, "sam@example.com")
	k, err := h.svc.CreateKey(ctx, sess.Account.ID, sess.User.ID, "k", ScopeFull)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.svc.RevokeKey(ctx, other.Account.ID, k.ID); !errors.Is(err, ErrKeyNotFound) {
		t.Errorf("revoke from another account: err = %v", err)
	}
	if _, err := h.svc.Authenticate(ctx, k.Secret); err != nil {
		t.Fatalf("before revoking: %v", err)
	}
	if err := h.svc.RevokeKey(ctx, sess.Account.ID, k.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Authenticate(ctx, k.Secret); !errors.Is(err, ErrInvalidKey) {
		t.Errorf("after revoking: err = %v", err)
	}
	if err := h.svc.RevokeKey(ctx, sess.Account.ID, k.ID); !errors.Is(err, ErrKeyNotFound) {
		t.Errorf("revoke twice: err = %v", err)
	}
}

func TestAuthenticate(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	sess := activeUser(t, h, "pat@example.com")
	k, err := h.svc.CreateKey(ctx, sess.Account.ID, sess.User.ID, "k", ScopeDraft)
	if err != nil {
		t.Fatal(err)
	}
	p, err := h.svc.Authenticate(ctx, k.Secret)
	if err != nil {
		t.Fatal(err)
	}
	if p.KeyID != k.ID || p.Kind != KindAPIKey || p.Scope != ScopeDraft || p.Account.ID != sess.Account.ID || p.UserID != sess.User.ID {
		t.Errorf("principal = %+v", p)
	}

	lastUsed := func() time.Time {
		keys, err := h.svc.ListKeys(ctx, sess.Account.ID, "", 10)
		if err != nil || len(keys) != 1 || keys[0].LastUsedAt == nil {
			t.Fatalf("keys = %+v, %v", keys, err)
		}
		return keys[0].LastUsedAt.UTC()
	}
	first := lastUsed()
	if !first.Equal(h.clk.now()) {
		t.Errorf("last used = %v, want %v", first, h.clk.now())
	}
	h.clk.advance(30 * time.Second)
	if _, err := h.svc.Authenticate(ctx, k.Secret); err != nil {
		t.Fatal(err)
	}
	if got := lastUsed(); !got.Equal(first) {
		t.Errorf("last used moved within a minute: %v", got)
	}
	h.clk.advance(time.Minute)
	if _, err := h.svc.Authenticate(ctx, k.Secret); err != nil {
		t.Fatal(err)
	}
	if got := lastUsed(); !got.Equal(h.clk.now()) {
		t.Errorf("last used = %v, want %v", got, h.clk.now())
	}

	cli, err := h.svc.CreateCLIToken(ctx, sess.Account.ID, sess.User.ID, "mailgoose CLI on laptop")
	if err != nil || !secretRE.MatchString(cli.Secret) || cli.Secret[:7] != "mg_cli_" {
		t.Fatalf("CLI token = %+v, %v", cli, err)
	}
	if p, err := h.svc.Authenticate(ctx, cli.Secret); err != nil || p.Kind != KindCLIToken || p.Scope != ScopeFull {
		t.Errorf("CLI token principal = %+v, %v", p, err)
	}

	for _, bad := range []string{"", "mg_test_", "nope", k.Secret + "x", "mg_live_" + k.Secret[8:]} {
		if _, err := h.svc.Authenticate(ctx, bad); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("Authenticate(%q): err = %v", bad, err)
		}
	}
}
