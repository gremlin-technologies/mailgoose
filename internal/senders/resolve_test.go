package senders

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gremlin-technologies/mailgoose/internal/domains"
	"github.com/gremlin-technologies/mailgoose/internal/mail"
)

// confirmed adds email as a confirmed sender address of the harness's
// account.
func (h *harness) confirmed(t *testing.T, email string) {
	t.Helper()
	if _, err := h.db.Exec(context.Background(), `
		INSERT INTO sender_addresses (account_id, email, confirmed_at, created_at) VALUES ($1, $2, now(), now())`, h.acc, email); err != nil {
		t.Fatal(err)
	}
}

// verifiedDomain gives the harness's account a verified domain.
func (h *harness) verifiedDomain(t *testing.T, name string) {
	t.Helper()
	if _, err := h.db.Exec(context.Background(), `
		INSERT INTO domains (account_id, name, mail_from, created_at, check_until, status) VALUES ($1, $2, 'bounce', now(), now(), 'verified')`,
		h.acc, name); err != nil {
		t.Fatal(err)
	}
}

// TestResolve: a verified domain's From goes as asked; a verified sender's
// goes from the account's slug on the shared domain, with replies to it;
// anything else is refused. With shared sending off, only a verified
// domain's From goes (MG-85).
func TestResolve(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.verifiedDomain(t, "patsbakery.com")
	h.confirmed(t, "pat@example.com")
	r := &Resolver{DB: h.db, Domains: &domains.Service{DB: h.db}, Shared: "shared.mailgoose.test"}
	off := &Resolver{DB: h.db, Domains: &domains.Service{DB: h.db}}

	var slug string
	if err := h.db.QueryRow(ctx, `SELECT sending_slug FROM accounts WHERE id = $1`, h.acc).Scan(&slug); err != nil || slug != "pats-bakery" {
		t.Fatalf("slug = %q, %v", slug, err)
	}
	shared := "pats-bakery@shared.mailgoose.test"

	// A verified domain wins, with or without shared sending, and its
	// Reply-To isn't checked.
	for _, res := range []*Resolver{r, off} {
		s, err := res.Resolve(ctx, h.acc, mail.Address{Name: "News", Email: "news@patsbakery.com"}, "anyone@example.org")
		if err != nil || s.Shared || s.From.Email != "news@patsbakery.com" || s.From.Name != "News" ||
			len(s.ReplyTo) != 1 || s.ReplyTo[0].Email != "anyone@example.org" {
			t.Errorf("verified domain: %+v, %v", s, err)
		}
	}

	tests := []struct {
		name          string
		from, replyTo string
		fromName      string
		wantFrom      string
		wantName      string
		wantReplyTo   string
		wantErr       error
		wantReplyErr  bool
	}{
		{name: "owner's own address", from: "Owner@Shop.example", wantFrom: shared, wantName: "Pat's Bakery", wantReplyTo: "Owner@Shop.example"},
		// MG-98: the email's own From name is used only on a verified domain.
		{name: "confirmed sender, own From name", from: "pat@example.com", fromName: "Pat", wantFrom: shared, wantName: "Pat's Bakery", wantReplyTo: "pat@example.com"},
		{name: "From name of a brand", from: "pat@example.com", fromName: "PayPal Support", wantFrom: shared, wantName: "Pat's Bakery", wantReplyTo: "pat@example.com"},
		{name: "Reply-To another verified sender", from: "pat@example.com", replyTo: "owner@shop.example", wantFrom: shared, wantName: "Pat's Bakery", wantReplyTo: "owner@shop.example"},
		{name: "Reply-To on a verified domain", from: "pat@example.com", replyTo: "help@patsbakery.com", wantFrom: shared, wantName: "Pat's Bakery", wantReplyTo: "help@patsbakery.com"},
		{name: "Reply-To the From itself", from: "pat@example.com", replyTo: "PAT@example.com", wantFrom: shared, wantName: "Pat's Bakery", wantReplyTo: "PAT@example.com"},
		{name: "Reply-To nobody verified", from: "pat@example.com", replyTo: "boss@example.org", wantReplyErr: true},
		{name: "unverified From", from: "stranger@example.net", wantErr: domains.ErrFromNotVerified},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := r.Resolve(ctx, h.acc, mail.Address{Name: tt.fromName, Email: tt.from}, tt.replyTo)
			var rerr *ReplyToError
			switch {
			case tt.wantReplyErr:
				if !errors.As(err, &rerr) || rerr.Address != tt.replyTo {
					t.Fatalf("err = %v, want a ReplyToError", err)
				}
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
				if !s.Shared || s.From.Email != tt.wantFrom || s.From.Name != tt.wantName || s.Address != tt.from ||
					len(s.ReplyTo) != 1 || s.ReplyTo[0].Email != tt.wantReplyTo {
					t.Errorf("sender = %+v", s)
				}
			}
			// With shared sending off, none of these go.
			if _, err := off.Resolve(ctx, h.acc, mail.Address{Email: tt.from}, tt.replyTo); !errors.Is(err, domains.ErrFromNotVerified) {
				t.Errorf("shared sending off: %v", err)
			}
		})
	}

	// A pending sender address doesn't count.
	if _, err := h.db.Exec(ctx, `INSERT INTO sender_addresses (account_id, email, created_at) VALUES ($1, 'pending@example.com', now())`, h.acc); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(ctx, h.acc, mail.Address{Email: "pending@example.com"}, ""); !errors.Is(err, domains.ErrFromNotVerified) {
		t.Errorf("pending sender: %v", err)
	}

	// The wording names both fixes only when shared sending is on.
	if h := r.UnverifiedHint("x@example.net"); !strings.Contains(h, "sender-addresses add x@example.net") || !strings.Contains(h, "domain add example.net") {
		t.Errorf("hint on: %s", h)
	}
	if h := off.UnverifiedHint("x@example.net"); strings.Contains(h, "sender-addresses") || !strings.Contains(h, "domain add example.net") {
		t.Errorf("hint off: %s", h)
	}
	// With it on, the owner's sign-in address is named as a fix, and the
	// API routes beside the CLI commands; details carry the From and
	// whether shared sending is on, for a client's own wording (MG-100).
	if h := r.UnverifiedHint("x@example.net"); !strings.Contains(h, "sign-in address, which works as it is") ||
		!strings.Contains(h, "POST /v1/sender-addresses") || !strings.Contains(h, "POST /v1/domains") {
		t.Errorf("hint on: %s", h)
	}
	if h := off.UnverifiedHint("x@example.net"); strings.Contains(h, "sign-in") || !strings.Contains(h, "POST /v1/domains") {
		t.Errorf("hint off: %s", h)
	}
	if d := r.UnverifiedDetails("x@example.net"); d["from_email"] != "x@example.net" || d["shared_sending"] != true {
		t.Errorf("details on: %v", d)
	}
	if d := off.UnverifiedDetails("x@example.net"); d["from_email"] != "x@example.net" || d["shared_sending"] != false {
		t.Errorf("details off: %v", d)
	}
	if r.SharedAddress("pats-bakery") != shared || off.SharedAddress("pats-bakery") != "" {
		t.Error("SharedAddress")
	}
}

// TestCheckShared: the guard's check passes only the account's own slug
// with Reply-To addresses it verified.
func TestCheckShared(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.confirmed(t, "pat@example.com")
	h.verifiedDomain(t, "patsbakery.com")
	r := &Resolver{DB: h.db, Domains: &domains.Service{DB: h.db}, Shared: "shared.mailgoose.test"}
	var other string
	if err := h.db.QueryRow(ctx, `INSERT INTO accounts (name, status) VALUES ('Other Shop', 'active') RETURNING id`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(ctx, `INSERT INTO users (account_id, email) VALUES ($1, 'sam@other.example')`, other); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		account, local string
		replyTo        []string
		ok             bool
	}{
		{h.acc, "pats-bakery", []string{"pat@example.com"}, true},
		{h.acc, "PATS-BAKERY", []string{"owner@shop.example", "help@patsbakery.com"}, true},
		{h.acc, "pats-bakery", nil, false},
		{h.acc, "other-shop", []string{"pat@example.com"}, false},
		{other, "pats-bakery", []string{"sam@other.example"}, false},
		{h.acc, "pats-bakery", []string{"pat@example.com", "sam@other.example"}, false},
		{"00000000-0000-4000-8000-000000000000", "pats-bakery", nil, false},
	} {
		err := r.CheckShared(ctx, tt.account, tt.local, tt.replyTo)
		if (err == nil) != tt.ok {
			t.Errorf("%s %s %v: err = %v, want ok %v", tt.account, tt.local, tt.replyTo, err, tt.ok)
		}
	}
}

// TestSendingSlug: every account gets a slug from its name when it's made,
// unique and never reserved, and keeps it when renamed.
func TestSendingSlug(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	slug := func(name string) string {
		t.Helper()
		var s string
		if err := h.db.QueryRow(ctx, `INSERT INTO accounts (name) VALUES ($1) RETURNING sending_slug`, name).Scan(&s); err != nil {
			t.Fatal(err)
		}
		if len(s) < 3 || len(s) > 40 || strings.Trim(s, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" {
			t.Fatalf("%q: slug %q isn't 3 to 40 of [a-z0-9-]", name, s)
		}
		return s
	}
	for name, want := range map[string]string{"Café Olé": "cafe-ole", "  Acme, Inc.  ": "acme-inc", "Garden & Co": "garden-co"} {
		if got := slug(name); got != want {
			t.Errorf("%q: slug %q, want %q", name, got, want)
		}
	}
	// Pat's Bakery is the harness's; a second one gets a suffix.
	if got := slug("Pat’s Bakery"); !strings.HasPrefix(got, "pats-bakery-") || len(got) != len("pats-bakery-")+4 {
		t.Errorf("taken: %q", got)
	}
	for _, reserved := range []string{"Postmaster", "abuse", "No-Reply", "mailgoose", "Support", "Info"} {
		got := slug(reserved)
		base := strings.ToLower(reserved)
		if got == base || !strings.HasPrefix(got, base+"-") {
			t.Errorf("reserved %q: slug %q", reserved, got)
		}
	}
	if got := slug("Al"); !strings.HasPrefix(got, "al-") {
		t.Errorf("too short: %q", got)
	}
	if got := slug("!!!"); got != "account" && !strings.HasPrefix(got, "account-") {
		t.Errorf("no letters: %q", got)
	}
	if got := slug(strings.Repeat("goose ", 20)); len(got) > 32 || strings.HasSuffix(got, "-") {
		t.Errorf("long: %q", got)
	}

	// Renaming keeps it.
	if _, err := h.db.Exec(ctx, `UPDATE accounts SET name = 'Something Else' WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	var s string
	if err := h.db.QueryRow(ctx, `SELECT sending_slug FROM accounts WHERE id = $1`, h.acc).Scan(&s); err != nil || s != "pats-bakery" {
		t.Errorf("after a rename: %q, %v", s, err)
	}
	// The database refuses a bad or repeated one.
	for _, bad := range []string{"Pats", "pats-bakery", "ab", "pats_bakery"} {
		if _, err := h.db.Exec(ctx, `INSERT INTO accounts (name, sending_slug) VALUES ('X', $1)`, bad); err == nil {
			t.Errorf("slug %q accepted", bad)
		}
	}
}
