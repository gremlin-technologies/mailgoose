package tracking

import (
	"context"
	"encoding/base64"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/dbtest"
	"github.com/gremlin-technologies/mailgoose/internal/unsubscribe"
)

type harness struct {
	db        *pgxpool.Pool
	svc       *Service
	acc, camp string
	rcpt      int64
	links     *Links
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	db := dbtest.New(t)
	h := &harness{db: db, svc: &Service{DB: db, Unsubscribe: &unsubscribe.Service{DB: db}}}
	var contact string
	for _, q := range []struct {
		sql  string
		args []any
		dest any
	}{
		{`INSERT INTO accounts (name) VALUES ('Bakery') RETURNING id`, nil, &h.acc},
	} {
		if err := db.QueryRow(ctx, q.sql, q.args...).Scan(q.dest); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.QueryRow(ctx, `INSERT INTO contacts (account_id, email, status, source) VALUES ($1, 'ann@example.com', 'subscribed', 'api') RETURNING id`, h.acc).Scan(&contact); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `INSERT INTO campaigns (account_id, name, all_contacts, status) VALUES ($1, 'Bread', true, 'sent') RETURNING id`, h.acc).Scan(&h.camp); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `INSERT INTO campaign_recipients (account_id, campaign_id, contact_id, status) VALUES ($1, $2, $3, 'sent') RETURNING id`, h.acc, h.camp, contact).Scan(&h.rcpt); err != nil {
		t.Fatal(err)
	}
	g, err := h.svc.Unsubscribe.Signer(ctx, h.acc)
	if err != nil {
		t.Fatal(err)
	}
	if h.links, err = NewLinks(g, "https://app.test/", h.camp); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *harness) events(t *testing.T) []string {
	t.Helper()
	rows, err := h.db.Query(context.Background(),
		`SELECT type || ' ' || coalesce(link::text, '-') || ' ' || coalesce(url, '-') FROM engagement_events WHERE recipient_id = $1 ORDER BY id`, h.rcpt)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func token(addr, prefix string) string {
	_, tok, _ := strings.Cut(addr, prefix)
	return tok
}

func TestRoundTrip(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	open := h.links.Open(h.rcpt)
	if !strings.HasPrefix(open, "https://app.test/t/o/") {
		t.Fatalf("open address %s", open)
	}
	hit, err := h.svc.Check(ctx, token(open, OpenPrefix), false)
	if err != nil {
		t.Fatal(err)
	}
	if hit.AccountID != h.acc || hit.CampaignID != h.camp || hit.RecipientID != h.rcpt || hit.Link != 0 || hit.URL != "" {
		t.Errorf("open hit %+v", hit)
	}

	target := "https://example.com/shop?a=1&b=two#top"
	click := h.links.Click(h.rcpt, 3, target)
	hit, err = h.svc.Check(ctx, token(click, ClickPrefix), true)
	if err != nil {
		t.Fatal(err)
	}
	if hit.Link != 3 || hit.URL != target || hit.RecipientID != h.rcpt {
		t.Errorf("click hit %+v", hit)
	}

	// An open token isn't a click token, and the other way round.
	if _, err := h.svc.Check(ctx, token(open, OpenPrefix), true); !errors.Is(err, ErrBadToken) {
		t.Errorf("open token as click: %v", err)
	}
	if _, err := h.svc.Check(ctx, token(click, ClickPrefix), false); !errors.Is(err, ErrBadToken) {
		t.Errorf("click token as open: %v", err)
	}
}

func TestUntrackedLinks(t *testing.T) {
	h := newHarness(t)
	for _, target := range []string{
		"mailto:ann@example.com", "tel:+15550100", "#top", "/relative", "javascript:alert(1)",
		"https://example.com/" + strings.Repeat("x", MaxURL),
	} {
		if got := h.links.Click(h.rcpt, 1, target); got != target {
			t.Errorf("%.40s was rewritten to %s", target, got)
		}
	}
	if got := h.links.Click(h.rcpt, 0, "https://example.com"); got != "https://example.com" {
		t.Errorf("link 0 was rewritten")
	}
}

func TestTamperedTokens(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	click := token(h.links.Click(h.rcpt, 1, "https://example.com/a"), ClickPrefix)
	evil := token(h.links.Click(h.rcpt, 1, "https://evil.example/a"), ClickPrefix)

	flip := []byte(click)
	i := len(flip) / 2
	if flip[i] == 'A' {
		flip[i] = 'B'
	} else {
		flip[i] = 'A'
	}
	for name, tok := range map[string]string{
		"empty":           "",
		"no prefix":       click[2:],
		"not base64":      "t1!!!!",
		"short":           click[:20],
		"one char off":    string(flip),
		"cut":             click[:len(click)-2],
		"spliced":         splice(t, evil, click),
		"unknown account": "t1" + strings.Repeat("A", 100),
	} {
		if _, err := h.svc.Check(ctx, tok, true); !errors.Is(err, ErrBadToken) {
			t.Errorf("%s: %v", name, err)
		}
	}

	// Another account's key doesn't verify this account's links.
	var other string
	if err := h.db.QueryRow(ctx, `INSERT INTO accounts (name) VALUES ('Other') RETURNING id`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	g, err := h.svc.Unsubscribe.Signer(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	forged, _ := NewLinks(g, "https://app.test", h.camp)
	fake := token(forged.Click(h.rcpt, 1, "https://evil.example"), ClickPrefix)
	hit, err := h.svc.Check(ctx, fake, true)
	if err != nil {
		t.Fatalf("a token signed by its own account checks out: %v", err)
	}
	if hit.AccountID != other {
		t.Fatalf("hit for %s", hit.AccountID)
	}
	if err := h.svc.Record(ctx, hit); err != nil {
		t.Fatal(err)
	}
	if got := h.events(t); len(got) != 0 {
		t.Errorf("another account's token recorded %v on this account's send", got)
	}
}

func TestRecord(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	click := &Hit{AccountID: h.acc, CampaignID: h.camp, RecipientID: h.rcpt, Link: 2, URL: "https://example.com/b"}
	if err := h.svc.Record(ctx, click); err != nil {
		t.Fatal(err)
	}
	// The pixel was blocked, so the click counts as an open too; later
	// opens and clicks add their own rows.
	if err := h.svc.Record(ctx, click); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Record(ctx, &Hit{AccountID: h.acc, CampaignID: h.camp, RecipientID: h.rcpt}); err != nil {
		t.Fatal(err)
	}
	want := []string{"click 2 https://example.com/b", "click 2 https://example.com/b", "open - -", "open - -"}
	got := h.events(t)
	slices.Sort(got)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("events %v, want %v", got, want)
	}

	// A send record that's gone records nothing.
	if err := h.svc.Record(ctx, &Hit{AccountID: h.acc, CampaignID: h.camp, RecipientID: h.rcpt + 1000}); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = h.db.QueryRow(ctx, `SELECT count(*) FROM engagement_events`).Scan(&n)
	if n != len(want) {
		t.Errorf("%d events, want %d", n, len(want))
	}
}

// A click keeps only the link's site and path: merge tags filled into the
// query or an address in the path aren't stored (from the MG-37 review).
func TestStoredURL(t *testing.T) {
	for raw, want := range map[string]string{
		"https://shop.example/menu":                         "https://shop.example/menu",
		"https://shop.example/menu?e=ann%40example.com#top": "https://shop.example/menu",
		"https://ann:pw@shop.example/a b":                   "https://shop.example/a%20b",
		"https://shop.example/u/ann@example.com":            "https://shop.example",
		"https://shop.example/u/ann%40example.com/x":        "https://shop.example",
		"http://shop.example":                               "http://shop.example",
	} {
		if got := StoredURL(raw); got != want {
			t.Errorf("%s: %q, want %q", raw, got, want)
		}
	}
	h := newHarness(t)
	ctx := context.Background()
	hit, err := h.svc.Check(ctx, token(h.links.Click(h.rcpt, 1, "https://shop.example/?e=ann@example.com"), ClickPrefix), true)
	if err != nil {
		t.Fatal(err)
	}
	if hit.URL != "https://shop.example/?e=ann@example.com" {
		t.Errorf("the click goes to %q", hit.URL)
	}
	if err := h.svc.Record(ctx, hit); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = h.db.QueryRow(ctx, `SELECT count(*) FROM engagement_events WHERE url LIKE '%ann%'`).Scan(&n)
	if n != 0 {
		t.Errorf("%d stored rows hold the address", n)
	}
}

// A token naming an account with no key is refused without reading the
// database again for a minute (from the MG-37 review).
func TestKeylessAccountRemembered(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 19, 8, 0, 0, 0, time.UTC)
	h.svc.Now = func() time.Time { return now }
	var acc string
	if err := h.db.QueryRow(ctx, `INSERT INTO accounts (name) VALUES ('No links yet') RETURNING id`).Scan(&acc); err != nil {
		t.Fatal(err)
	}
	g, err := h.svc.Unsubscribe.Signer(ctx, acc)
	if err != nil {
		t.Fatal(err)
	}
	links, _ := NewLinks(g, "https://app.test", h.camp)
	tok := token(links.Open(h.rcpt), OpenPrefix)
	var key []byte
	if err := h.db.QueryRow(ctx, `DELETE FROM unsubscribe_keys WHERE account_id = $1 RETURNING key`, acc).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Check(ctx, tok, false); !errors.Is(err, ErrBadToken) {
		t.Fatalf("keyless: %v", err)
	}
	// The key comes back, but for a minute the miss is answered from
	// memory, without a lookup.
	if _, err := h.db.Exec(ctx, `INSERT INTO unsubscribe_keys (account_id, key) VALUES ($1, $2)`, acc, key); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Check(ctx, tok, false); !errors.Is(err, ErrBadToken) {
		t.Errorf("remembered: %v", err)
	}
	now = now.Add(missTTL + time.Second)
	if _, err := h.svc.Check(ctx, tok, false); err != nil {
		t.Errorf("after a minute: %v", err)
	}
}

// splice puts sig's signature on body's contents: a changed destination
// with a real signature.
func splice(t *testing.T, body, sig string) string {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(body[2:])
	if err != nil {
		t.Fatal(err)
	}
	g, err := base64.RawURLEncoding.DecodeString(sig[2:])
	if err != nil {
		t.Fatal(err)
	}
	out := append(b[:len(b)-macLen:len(b)-macLen], g[len(g)-macLen:]...)
	return tokenPrefix + base64.RawURLEncoding.EncodeToString(out)
}

func TestClickTarget(t *testing.T) {
	// A token as Links.Click makes one; the signature isn't checked.
	body := make([]byte, headerLen, headerLen+64)
	body = append(body, "https://shop.example/sale?x=1"...)
	body = append(body, make([]byte, macLen)...)
	tracked := "https://app.example" + ClickPrefix + encode(body)
	if got, ok := ClickTarget(tracked); !ok || got != "https://shop.example/sale?x=1" {
		t.Errorf("tracked: %q %v", got, ok)
	}
	for _, link := range []string{"https://shop.example/sale", "https://app.example/t/c/nope", "https://app.example/t/o/t1AAAA",
		"https://app.example" + ClickPrefix + encode(make([]byte, headerLen+macLen)), "::"} {
		if got, ok := ClickTarget(link); ok {
			t.Errorf("%s: %q", link, got)
		}
	}
}
