package api

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/sending"
	"github.com/gremlin-technologies/mailgoose/internal/unsubscribe"
)

// unsubscribeFixture is an account with two subscribed contacts and a sent
// campaign, and a token for each contact.
type unsubscribeFixture struct {
	srv           *Server
	db            *pgxpool.Pool
	h             http.Handler
	pat, jo       string
	patTok, joTok string
	campaign      string
	logs          *bytes.Buffer
}

func newUnsubscribeFixture(t *testing.T) *unsubscribeFixture {
	t.Helper()
	srv := authServer(t)
	v, _ := testDBs.Load(t)
	f := &unsubscribeFixture{srv: srv, db: v.(*pgxpool.Pool), logs: &bytes.Buffer{}}
	srv.Log = slog.New(slog.NewTextHandler(f.logs, nil))
	ctx := context.Background()
	var acc string
	if err := f.db.QueryRow(ctx, `INSERT INTO accounts (name) VALUES ('Pat''s Bakery') RETURNING id`).Scan(&acc); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		email string
		id    *string
	}{{"pat@example.com", &f.pat}, {"jo@example.com", &f.jo}} {
		if err := f.db.QueryRow(ctx, `INSERT INTO contacts (account_id, email, status, source) VALUES ($1, $2, 'subscribed', 'api') RETURNING id`,
			acc, c.email).Scan(c.id); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.db.QueryRow(ctx, `INSERT INTO campaigns (account_id, name, all_contacts, status) VALUES ($1, 'Bread', true, 'sent') RETURNING id`, acc).Scan(&f.campaign); err != nil {
		t.Fatal(err)
	}
	g, err := srv.Unsubscribe.Signer(ctx, acc)
	if err != nil {
		t.Fatal(err)
	}
	f.patTok, _ = g.Token(f.pat, f.campaign)
	f.joTok, _ = g.Token(f.jo, f.campaign)
	f.h = srv.Handler()
	return f
}

func (f *unsubscribeFixture) do(t *testing.T, method, path, body string, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range header {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, r)
	return rec
}

func (f *unsubscribeFixture) status(t *testing.T, id string) string {
	t.Helper()
	var s string
	if err := f.db.QueryRow(context.Background(), `SELECT status FROM contacts WHERE id = $1`, id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func (f *unsubscribeFixture) records(t *testing.T, contact string) []string {
	t.Helper()
	rows, err := f.db.Query(context.Background(), `SELECT source || ' ' || campaign_id FROM unsubscribes WHERE contact_id = $1`, contact)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

func expectPage(t *testing.T, rec *httptest.ResponseRecorder, status int, wants ...string) {
	t.Helper()
	if rec.Code != status {
		t.Errorf("status %d, want %d: %s", rec.Code, status, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("content type %q", ct)
	}
	for k, v := range map[string]string{"Cache-Control": "no-store", "Referrer-Policy": "no-referrer", "X-Robots-Tag": "noindex"} {
		if rec.Header().Get(k) != v {
			t.Errorf("%s = %q", k, rec.Header().Get(k))
		}
	}
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Errorf("CSP %q", rec.Header().Get("Content-Security-Policy"))
	}
	for _, w := range wants {
		if !strings.Contains(rec.Body.String(), w) {
			t.Errorf("page lacks %q:\n%s", w, rec.Body)
		}
	}
}

func TestUnsubscribePage(t *testing.T) {
	f := newUnsubscribeFixture(t)
	path := unsubscribe.Path(f.patTok)

	// Opening the link asks; it doesn't unsubscribe, so a link checker
	// that fetches it changes nothing.
	for _, m := range []string{http.MethodGet, http.MethodHead} {
		rec := f.do(t, m, path, "", nil)
		if m == http.MethodGet {
			expectPage(t, rec, http.StatusOK, "Unsubscribe from Pat&#39;s Bakery?", "pat@example.com will stop getting emails", `<form method="post"><button type="submit">Unsubscribe</button></form>`)
		} else if rec.Code != http.StatusOK {
			t.Errorf("HEAD %d", rec.Code)
		}
	}
	if f.status(t, f.pat) != "subscribed" {
		t.Fatal("opening the page unsubscribed")
	}

	// One click on the button, with no sign-in.
	rec := f.do(t, http.MethodPost, path, "", nil)
	expectPage(t, rec, http.StatusOK, "You&#39;re unsubscribed", "pat@example.com won&#39;t get any more emails from Pat&#39;s Bakery.")
	if f.status(t, f.pat) != "unsubscribed" {
		t.Error("not unsubscribed")
	}
	if got := f.records(t, f.pat); len(got) != 1 || got[0] != "link "+f.campaign {
		t.Errorf("records %v", got)
	}

	// Again, and the page afterwards: the same answer, one record.
	expectPage(t, f.do(t, http.MethodPost, path, "", nil), http.StatusOK, "You&#39;re unsubscribed")
	expectPage(t, f.do(t, http.MethodGet, path, "", nil), http.StatusOK, "You&#39;re unsubscribed")
	if got := f.records(t, f.pat); len(got) != 1 {
		t.Errorf("records %v", got)
	}

	// The token stays out of the logs.
	if strings.Contains(f.logs.String(), f.patTok) || !strings.Contains(f.logs.String(), "path=/u/[token]") {
		t.Errorf("logs:\n%s", f.logs)
	}
}

func TestOneClickUnsubscribe(t *testing.T) {
	f := newUnsubscribeFixture(t)
	// A mail provider's POST: no cookie, no sign-in, perhaps an Origin of
	// its own. The API's cross-origin check and its low rate limit don't
	// apply.
	f.srv.RateLimitPerMinute = 1
	f.h = f.srv.Handler()
	hdr := map[string]string{"Origin": "https://mail.example", "Sec-Fetch-Site": "cross-site"}
	for range 3 {
		rec := f.do(t, http.MethodPost, unsubscribe.Path(f.joTok), "List-Unsubscribe=One-Click", hdr)
		if rec.Code != http.StatusOK {
			t.Fatalf("one-click: %d %s", rec.Code, rec.Body)
		}
	}
	if f.status(t, f.jo) != "unsubscribed" || f.status(t, f.pat) != "subscribed" {
		t.Error("wrong contact changed")
	}
	if got := f.records(t, f.jo); len(got) != 1 || got[0] != "one_click "+f.campaign {
		t.Errorf("records %v", got)
	}
}

func TestUnsubscribeBadLinks(t *testing.T) {
	f := newUnsubscribeFixture(t)
	tampered := []byte(f.patTok)
	tampered[40] ^= 'A' ^ 'B'
	if tampered[40] == f.patTok[40] {
		t.Fatal("not tampered")
	}
	for _, path := range []string{
		"/u/" + string(tampered),
		"/u/" + f.patTok[:len(f.patTok)-3],
		"/u/u0" + f.patTok[2:],
		"/u/nonsense",
		"/u/",
	} {
		for _, m := range []string{http.MethodGet, http.MethodPost} {
			expectPage(t, f.do(t, m, path, "List-Unsubscribe=One-Click", nil), http.StatusBadRequest,
				"This unsubscribe link doesn&#39;t work", "Nothing has been changed.")
		}
	}
	if f.status(t, f.pat) != "subscribed" || len(f.records(t, f.pat)) != 0 {
		t.Error("a bad link changed something")
	}

	// A contact that's been deleted.
	if _, err := f.db.Exec(context.Background(), `DELETE FROM contacts WHERE id = $1`, f.jo); err != nil {
		t.Fatal(err)
	}
	expectPage(t, f.do(t, http.MethodPost, unsubscribe.Path(f.joTok), "", nil), http.StatusNotFound, "You&#39;re not on this list")

	rec := f.do(t, http.MethodPut, unsubscribe.Path(f.patTok), "", nil)
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD, POST" {
		t.Errorf("PUT: %d %v", rec.Code, rec.Header())
	}
}

func TestUnsubscribePagesWithWebAppAndNoDatabase(t *testing.T) {
	// With the web app served too, /u/ still reaches the unsubscribe page.
	f := newUnsubscribeFixture(t)
	f.srv.Web = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	f.h = f.srv.Handler()
	expectPage(t, f.do(t, http.MethodGet, unsubscribe.Path(f.patTok), "", nil), http.StatusOK, "Unsubscribe from")

	// Without a database the page says so.
	h := newServer(nil).Handler()
	for _, m := range []string{http.MethodGet, http.MethodPost} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(m, unsubscribe.Path(f.patTok), nil))
		expectPage(t, rec, http.StatusServiceUnavailable, "Unsubscribing isn&#39;t working right now")
	}
}

// The recipient pages' limit is per IP, whatever Authorization header a
// client sends, and a recipient who hits it gets a page, not JSON. A
// limited POST is logged.
func TestUnsubscribePagesLimit(t *testing.T) {
	f := newUnsubscribeFixture(t)
	// A stopped clock, so no tokens come back while the test runs.
	at := time.Now()
	f.srv.Now = func() time.Time { return at }
	f.h = f.srv.Handler()
	for i := range recipientPagesPerMinute {
		rec := f.do(t, http.MethodGet, unsubscribe.PathPrefix+"not-a-token", "", map[string]string{"Authorization": "Bearer x" + strconv.Itoa(i)})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("request %d: %d", i, rec.Code)
		}
	}
	rec := f.do(t, http.MethodPost, unsubscribe.Path(f.joTok), "List-Unsubscribe=One-Click", map[string]string{"Authorization": "Bearer fresh"})
	expectPage(t, rec, http.StatusTooManyRequests, "Too many tries just now", "Nothing has been changed.")
	if rec.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After")
	}
	if f.status(t, f.jo) != "subscribed" {
		t.Error("a limited POST unsubscribed")
	}
	if !strings.Contains(f.logs.String(), "unsubscribe post rate limited") || !strings.Contains(f.logs.String(), "one_click=true") {
		t.Errorf("logs:\n%s", f.logs)
	}

	// A limited POST reads no more than a recipient form's cap.
	body := &countingReader{r: strings.NewReader("List-Unsubscribe=One-Click&x=" + strings.Repeat("a", 1<<20))}
	r := httptest.NewRequest(http.MethodPost, unsubscribe.Path(f.joTok), body)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	f.h.ServeHTTP(rec, r)
	if rec.Code != http.StatusTooManyRequests || body.n > maxRecipientFormBytes+512 {
		t.Errorf("status %d after reading %d bytes", rec.Code, body.n)
	}
}

type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

func TestTestSendUnsubscribeLink(t *testing.T) {
	f := newUnsubscribeFixture(t)
	if sending.TestUnsubscribePath != "/u/"+testToken {
		t.Fatalf("test path %s", sending.TestUnsubscribePath)
	}
	for _, m := range []string{http.MethodGet, http.MethodPost} {
		expectPage(t, f.do(t, m, sending.TestUnsubscribePath, "List-Unsubscribe=One-Click", nil), http.StatusOK, "This was a test email", "Nothing has been changed.")
	}
	if f.status(t, f.pat) != "subscribed" || f.status(t, f.jo) != "subscribed" {
		t.Error("the test link changed someone")
	}
}
