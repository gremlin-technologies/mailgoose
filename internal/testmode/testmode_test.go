package testmode

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/dbtest"
	"github.com/gremlin-technologies/mailgoose/internal/mail"
)

const baseURL = "https://app.mailgoose.test/"

type harness struct {
	db   *pgxpool.Pool
	svc  *Service
	fake *mail.Fake
	now  time.Time
	acc  string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	db := dbtest.New(t)
	h := &harness{db: db, fake: &mail.Fake{}, now: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	// Behind the real guard, as in mailgoosed, so confirm links must pass it (MG-90).
	h.svc = &Service{DB: db, Mail: &mail.Guard{Sender: h.fake, System: "mail.mailgoose.test"},
		From: mail.Address{Name: "mailgoose", Email: "signin@mail.mailgoose.test"},
		Now:  func() time.Time { return h.now }}
	if err := db.QueryRow(ctx, `INSERT INTO accounts (name, status, sending_mode) VALUES ('QA Shop', 'active', 'test') RETURNING id`).Scan(&h.acc); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO users (account_id, email) VALUES ($1, 'Owner@Shop.example')`, h.acc); err != nil {
		t.Fatal(err)
	}
	return h
}

var tokenInLink = regexp.MustCompile(`https://app\.mailgoose\.test/test-addresses/confirm\?token=(t1[A-Za-z0-9_-]{43})`)

// token is the token in the last confirm email, which must have gone to to.
func (h *harness) token(t *testing.T, to string) string {
	t.Helper()
	m, ok := h.fake.Last()
	if !ok || m.Message.To.Email != to {
		t.Fatalf("last email = %+v, want one to %s", m.Message, to)
	}
	got := tokenInLink.FindStringSubmatch(m.Message.Text)
	if got == nil || !strings.Contains(m.Message.HTML, got[0]) {
		t.Fatalf("no confirm link in %q", m.Message.Text)
	}
	return got[1]
}

func (h *harness) allows(t *testing.T, email string) bool {
	t.Helper()
	ok, err := Allows(context.Background(), h.db, h.acc, email)
	if err != nil {
		t.Fatal(err)
	}
	g, err := Load(context.Background(), h.db, h.acc)
	if err != nil {
		t.Fatal(err)
	}
	if g.Allows(email) != ok {
		t.Fatalf("Gate.Allows(%s) = %v, Allows = %v", email, g.Allows(email), ok)
	}
	return ok
}

// TestConfirm: an added address is pending until its emailed link is used;
// then it's allowed, ignoring case. The link works once more harmlessly,
// and not at all after 7 days or once replaced.
func TestConfirm(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	a, err := h.svc.Add(ctx, h.acc, " QA@Example.com ", baseURL)
	if err != nil {
		t.Fatal(err)
	}
	if a.Email != "qa@example.com" || a.Status != StatusPending || a.ConfirmSentAt == nil || !a.ConfirmExpiresAt.Equal(h.now.Add(ConfirmTTL)) {
		t.Errorf("added = %+v", a)
	}
	m, _ := h.fake.Last()
	if m.Message.Subject != "Confirm a test address on mailgoose" || m.Message.Kind != mail.KindSystem ||
		m.Message.From.Email != "signin@mail.mailgoose.test" {
		t.Errorf("email = %+v", m.Message)
	}
	tok := h.token(t, "qa@example.com")
	if h.allows(t, "qa@example.com") {
		t.Error("pending address allowed")
	}
	var stored []byte
	if err := h.db.QueryRow(ctx, `SELECT confirm_token_hash FROM test_addresses WHERE id = $1`, a.ID).Scan(&stored); err != nil ||
		strings.Contains(string(stored), tok) {
		t.Errorf("token not hashed: %v", err)
	}

	h.now = h.now.Add(time.Hour)
	got, err := h.svc.Confirm(ctx, tok)
	if err != nil {
		t.Fatal(err)
	}
	if got.Email != "qa@example.com" || got.AccountName != "QA Shop" || !got.ConfirmedAt.Equal(h.now) {
		t.Errorf("confirmed = %+v", got)
	}
	if !h.allows(t, "QA@EXAMPLE.COM") {
		t.Error("confirmed address not allowed")
	}
	again, err := h.svc.Confirm(ctx, tok)
	if err != nil || !again.ConfirmedAt.Equal(got.ConfirmedAt) {
		t.Errorf("confirm again = %+v, %v", again, err)
	}
	list, err := h.svc.List(ctx, h.acc)
	if err != nil || len(list) != 1 || list[0].Status != StatusConfirmed || list[0].ConfirmSentAt != nil {
		t.Errorf("list = %+v, %v", list, err)
	}
	if _, err := h.svc.Resend(ctx, h.acc, a.ID, baseURL); !errors.Is(err, ErrConfirmed) {
		t.Errorf("resend confirmed: %v", err)
	}

	// Expiry, and a resend replacing the link.
	b, err := h.svc.Add(ctx, h.acc, "late@example.com", baseURL)
	if err != nil {
		t.Fatal(err)
	}
	old := h.token(t, "late@example.com")
	h.now = h.now.Add(ConfirmTTL)
	if _, err := h.svc.Confirm(ctx, old); !errors.Is(err, ErrExpired) {
		t.Errorf("expired: %v", err)
	}
	if _, err := h.svc.Resend(ctx, h.acc, b.ID, baseURL); err != nil {
		t.Fatal(err)
	}
	fresh := h.token(t, "late@example.com")
	if _, err := h.svc.Confirm(ctx, old); !errors.Is(err, ErrTokenNotFound) {
		t.Errorf("replaced link: %v", err)
	}
	if _, err := h.svc.Confirm(ctx, fresh); err != nil {
		t.Errorf("fresh link: %v", err)
	}

	for _, bad := range []string{"", "t1short", "x" + fresh[1:]} {
		if _, err := h.svc.Confirm(ctx, bad); !errors.Is(err, ErrBadToken) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if _, err := h.svc.Confirm(ctx, "t1"+strings.Repeat("A", 43)); !errors.Is(err, ErrTokenNotFound) {
		t.Errorf("unknown token: %v", err)
	}
}

// TestLimitOfFive: five addresses, pending ones included; a removed one
// frees its slot. The owner's address takes none, and duplicates are
// refused ignoring case.
func TestLimitOfFive(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	var ids []string
	for _, e := range []string{"a@example.com", "b@example.com", "c@example.com", "d@example.com", "e@example.com"} {
		a, err := h.svc.Add(ctx, h.acc, e, baseURL)
		if err != nil {
			t.Fatalf("%s: %v", e, err)
		}
		ids = append(ids, a.ID)
	}
	var limit *LimitError
	if _, err := h.svc.Add(ctx, h.acc, "f@example.com", baseURL); !errors.As(err, &limit) || limit.Count != MaxAddresses {
		t.Errorf("sixth: %v", err)
	}
	if _, err := h.svc.Add(ctx, h.acc, "A@Example.com", baseURL); !errors.Is(err, ErrExists) {
		t.Errorf("duplicate: %v", err)
	}
	if _, err := h.svc.Add(ctx, h.acc, "owner@shop.example", baseURL); !errors.Is(err, ErrOwnerAddress) {
		t.Errorf("owner: %v", err)
	}
	var invalid *InvalidError
	if _, err := h.svc.Add(ctx, h.acc, "not an address", baseURL); !errors.As(err, &invalid) {
		t.Errorf("invalid: %v", err)
	}
	if !h.allows(t, "OWNER@shop.example") {
		t.Error("owner not allowed")
	}
	if err := h.svc.Remove(ctx, h.acc, ids[0]); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Remove(ctx, h.acc, ids[0]); !errors.Is(err, ErrNotFound) {
		t.Errorf("remove twice: %v", err)
	}
	if _, err := h.svc.Add(ctx, h.acc, "f@example.com", baseURL); err != nil {
		t.Errorf("after a remove: %v", err)
	}
	list, _ := h.svc.List(ctx, h.acc)
	if got := len(list); got != MaxAddresses {
		t.Errorf("listed %d", got)
	}

	// Another account's addresses are its own.
	var other string
	if err := h.db.QueryRow(ctx, `INSERT INTO accounts (name, status) VALUES ('Other', 'active') RETURNING id`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Remove(ctx, other, ids[1]); !errors.Is(err, ErrNotFound) {
		t.Errorf("other account's remove: %v", err)
	}
	if _, err := h.svc.Add(ctx, other, "a@example.com", baseURL); err != nil {
		t.Errorf("same address, other account: %v", err)
	}
}

// TestResend: at most once a minute; a failed send can be retried at once.
func TestResend(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	a, err := h.svc.Add(ctx, h.acc, "qa@example.com", baseURL)
	if err != nil {
		t.Fatal(err)
	}
	h.now = h.now.Add(20 * time.Second)
	var soon *TooSoonError
	if _, err := h.svc.Resend(ctx, h.acc, a.ID, baseURL); !errors.As(err, &soon) || soon.RetryAfter != 40*time.Second {
		t.Errorf("too soon: %v", err)
	}
	h.now = h.now.Add(40 * time.Second)
	h.fake.Err = errors.New("ses down")
	var send *SendError
	if _, err := h.svc.Resend(ctx, h.acc, a.ID, baseURL); !errors.As(err, &send) {
		t.Errorf("failing send: %v", err)
	}
	h.fake.Err = nil
	if _, err := h.svc.Resend(ctx, h.acc, a.ID, baseURL); err != nil {
		t.Errorf("retry after a failed send: %v", err)
	}
	if _, err := h.svc.Resend(ctx, h.acc, "00000000-0000-0000-0000-000000000000", baseURL); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
}

// TestGateFullSending: with full sending everyone is allowed.
func TestGateFullSending(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if !slices.Equal(mustLoad(t, h).Allowed(), []string{"owner@shop.example"}) {
		t.Errorf("allowed = %v", mustLoad(t, h).Allowed())
	}
	if _, err := h.db.Exec(ctx, `UPDATE accounts SET sending_mode = 'full' WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	if g := mustLoad(t, h); g.Test() || len(g.Allowed()) != 0 || !h.allows(t, "anyone@example.com") {
		t.Errorf("full sending gate = %+v", g)
	}
}

func mustLoad(t *testing.T, h *harness) *Gate {
	t.Helper()
	g, err := Load(context.Background(), h.db, h.acc)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// TestDailyEmailCap: an account sends at most DailyEmails confirm emails in
// any EmailWindow, adds and resends alike. Removing an address gives none
// back, so adding and removing in a loop stops at the cap.
func TestDailyEmailCap(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	start := h.now
	// Eight adds of one address, each removed at once, a minute apart.
	for i := range DailyEmails - 2 {
		a, err := h.svc.Add(ctx, h.acc, "loop@example.com", baseURL)
		if err != nil {
			t.Fatalf("add %d: %v", i+1, err)
		}
		if err := h.svc.Remove(ctx, h.acc, a.ID); err != nil {
			t.Fatal(err)
		}
		h.now = h.now.Add(time.Minute)
	}
	// The ninth is an add, the tenth a resend of it.
	kept, err := h.svc.Add(ctx, h.acc, "kept@example.com", baseURL)
	if err != nil {
		t.Fatalf("ninth: %v", err)
	}
	h.now = h.now.Add(time.Minute)
	if _, err := h.svc.Resend(ctx, h.acc, kept.ID, baseURL); err != nil {
		t.Fatalf("tenth (a resend): %v", err)
	}
	if got := len(h.fake.Sent()); got != DailyEmails {
		t.Fatalf("%d emails sent, want %d", got, DailyEmails)
	}

	// The eleventh is refused, an add or a resend, and sends nothing.
	h.now = h.now.Add(time.Minute)
	var capped *RateLimitError
	if _, err := h.svc.Add(ctx, h.acc, "loop@example.com", baseURL); !errors.As(err, &capped) {
		t.Fatalf("eleventh add: %v", err)
	}
	if want := start.Add(EmailWindow).Sub(h.now); capped.RetryAfter != want {
		t.Errorf("retry after %v, want %v (when the first email leaves the window)", capped.RetryAfter, want)
	}
	if _, err := h.svc.Resend(ctx, h.acc, kept.ID, baseURL); !errors.As(err, &capped) {
		t.Fatalf("eleventh as a resend: %v", err)
	}
	if got := len(h.fake.Sent()); got != DailyEmails {
		t.Errorf("%d emails sent after the cap, want %d", got, DailyEmails)
	}
	list, _ := h.svc.List(ctx, h.acc)
	if len(list) != 1 || list[0].Email != "kept@example.com" {
		t.Errorf("a refused add left a row: %+v", list)
	}

	// Removing doesn't give any back.
	if err := h.svc.Remove(ctx, h.acc, kept.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Add(ctx, h.acc, "kept@example.com", baseURL); !errors.As(err, &capped) {
		t.Fatalf("re-add after remove: %v", err)
	}

	// Another account has its own cap.
	var other string
	if err := h.db.QueryRow(ctx, `INSERT INTO accounts (name, status) VALUES ('Other', 'active') RETURNING id`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Add(ctx, other, "loop@example.com", baseURL); err != nil {
		t.Errorf("other account: %v", err)
	}

	// Once the first email is 24 hours old, one more may go, and the old
	// record is pruned.
	h.now = start.Add(EmailWindow)
	if _, err := h.svc.Add(ctx, h.acc, "kept@example.com", baseURL); err != nil {
		t.Fatalf("a day later: %v", err)
	}
	var n int
	if err := h.db.QueryRow(ctx, `SELECT count(*) FROM test_address_emails WHERE account_id = $1`, h.acc).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != DailyEmails {
		t.Errorf("%d emails on record, want %d (the oldest pruned, this one added)", n, DailyEmails)
	}
}
