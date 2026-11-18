package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/tracking"
)

// trackingFixture is one sent copy of a campaign and its tracked addresses.
type trackingFixture struct {
	db    *pgxpool.Pool
	h     http.Handler
	rcpt  int64
	links *tracking.Links
}

func newTrackingFixture(tb testing.TB) *trackingFixture {
	tb.Helper()
	srv := authServerTB(tb)
	v, _ := testDBs.Load(tb)
	f := &trackingFixture{db: v.(*pgxpool.Pool)}
	ctx := context.Background()
	var acc, contact, campaign string
	if err := f.db.QueryRow(ctx, `INSERT INTO accounts (name) VALUES ('Bakery') RETURNING id`).Scan(&acc); err != nil {
		tb.Fatal(err)
	}
	if err := f.db.QueryRow(ctx, `INSERT INTO contacts (account_id, email, status, source) VALUES ($1, 'ann@example.com', 'subscribed', 'api') RETURNING id`, acc).Scan(&contact); err != nil {
		tb.Fatal(err)
	}
	if err := f.db.QueryRow(ctx, `INSERT INTO campaigns (account_id, name, all_contacts, status) VALUES ($1, 'Bread', true, 'sent') RETURNING id`, acc).Scan(&campaign); err != nil {
		tb.Fatal(err)
	}
	if err := f.db.QueryRow(ctx, `INSERT INTO campaign_recipients (account_id, campaign_id, contact_id, status) VALUES ($1, $2, $3, 'sent') RETURNING id`, acc, campaign, contact).Scan(&f.rcpt); err != nil {
		tb.Fatal(err)
	}
	g, err := srv.Unsubscribe.Signer(ctx, acc)
	if err != nil {
		tb.Fatal(err)
	}
	if f.links, err = tracking.NewLinks(g, "", campaign); err != nil {
		tb.Fatal(err)
	}
	f.h = srv.Handler()
	return f
}

func (f *trackingFixture) get(path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func (f *trackingFixture) count(t *testing.T, typ string) int {
	t.Helper()
	var n int
	if err := f.db.QueryRow(context.Background(), `SELECT count(*) FROM engagement_events WHERE recipient_id = $1 AND type = $2`, f.rcpt, typ).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestTrackedOpen(t *testing.T) {
	f := newTrackingFixture(t)
	rec := f.get(f.links.Open(f.rcpt))
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/gif" || !slices.Equal(rec.Body.Bytes(), pixel) {
		t.Fatalf("open: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
		t.Error("the pixel can be cached")
	}
	if n := f.count(t, tracking.TypeOpen); n != 1 {
		t.Errorf("%d opens", n)
	}
	// A bad token still gets the pixel, so the mail app shows no broken
	// image, but nothing is recorded.
	rec = f.get("/t/o/t1nonsense")
	if rec.Code != http.StatusOK || !slices.Equal(rec.Body.Bytes(), pixel) {
		t.Errorf("bad open token: %d", rec.Code)
	}
	if n := f.count(t, tracking.TypeOpen); n != 1 {
		t.Errorf("%d opens after a bad token", n)
	}
}

func TestTrackedClick(t *testing.T) {
	f := newTrackingFixture(t)
	target := "https://example.com/shop?a=1&b=two"
	link := f.links.Click(f.rcpt, 1, target)
	rec := f.get(link)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != target {
		t.Fatalf("click: %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Error("no Referrer-Policy")
	}
	if f.count(t, tracking.TypeClick) != 1 || f.count(t, tracking.TypeOpen) != 1 {
		t.Errorf("a click should record a click and an open")
	}

	// Changed links get an error page and never a redirect.
	tok := strings.TrimPrefix(link, tracking.ClickPrefix)
	for name, path := range map[string]string{
		"one char off": tracking.ClickPrefix + tok[:len(tok)-1] + flipChar(tok[len(tok)-1]),
		"cut":          tracking.ClickPrefix + tok[:len(tok)-4],
		"garbage":      tracking.ClickPrefix + "https%3A%2F%2Fevil.example",
		"open token":   strings.Replace(f.links.Open(f.rcpt), tracking.OpenPrefix, tracking.ClickPrefix, 1),
		"no token":     "/t/c/",
	} {
		rec := f.get(path)
		if rec.Code != http.StatusBadRequest || rec.Header().Get("Location") != "" {
			t.Errorf("%s: %d to %q", name, rec.Code, rec.Header().Get("Location"))
		}
	}
	if n := f.count(t, tracking.TypeClick); n != 1 {
		t.Errorf("%d clicks after bad links", n)
	}
}

func TestTrackingWithNoDatabase(t *testing.T) {
	h := serverWith(nil)(t).Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/t/o/t1abc", nil))
	if rec.Code != http.StatusOK || !slices.Equal(rec.Body.Bytes(), pixel) {
		t.Errorf("open: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/t/c/t1abc", nil))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Location") != "" {
		t.Errorf("click: %d", rec.Code)
	}
}

// TestClickLatency checks REP-3's p95 under 200 ms for the redirect,
// recording included, against the test database.
func TestClickLatency(t *testing.T) {
	f := newTrackingFixture(t)
	const n = 400
	took := make([]time.Duration, n)
	for i := range n {
		link := f.links.Click(f.rcpt, i%50+1, "https://example.com/p/"+strings.Repeat("x", i%40))
		start := time.Now()
		rec := f.get(link)
		took[i] = time.Since(start)
		if rec.Code != http.StatusFound {
			t.Fatalf("click %d: %d", i, rec.Code)
		}
	}
	slices.Sort(took)
	p95 := took[n*95/100]
	t.Logf("click p50 %v, p95 %v, max %v", took[n/2], p95, took[n-1])
	if p95 >= 200*time.Millisecond {
		t.Errorf("click p95 %v, want under 200ms", p95)
	}
}

// BenchmarkTrackedClick times one click: check, record and redirect.
func BenchmarkTrackedClick(b *testing.B) {
	f := newTrackingFixture(b)
	link := f.links.Click(f.rcpt, 1, "https://example.com/shop")
	b.ResetTimer()
	for b.Loop() {
		if rec := f.get(link); rec.Code != http.StatusFound {
			b.Fatal(rec.Code)
		}
	}
}

func flipChar(c byte) string {
	if c == 'A' {
		return "B"
	}
	return "A"
}
