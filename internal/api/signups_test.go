package api

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/contacts"
	"github.com/gremlin-technologies/mailgoose/internal/forms"
	"github.com/gremlin-technologies/mailgoose/internal/mail"
	"github.com/gremlin-technologies/mailgoose/internal/signups"
)

// signupFixture is an account that can send, with a signup form, and a
// handler whose rate limits run on a clock the test moves.
type signupFixture struct {
	srv  *Server
	h    http.Handler
	db   *pgxpool.Pool
	fake *mail.Fake
	acc  string
	form string
	key  []byte
	now  time.Time
	logs *bytes.Buffer
}

func newSignupFixture(t *testing.T, formJSON string) *signupFixture {
	t.Helper()
	srv := fullKey(authServer)(t)
	v, _ := testDBs.Load(t)
	o, _ := outboxes.Load(t)
	f := &signupFixture{srv: srv, db: v.(*pgxpool.Pool), fake: o.(*mail.Fake), now: time.Now(), logs: &bytes.Buffer{}}
	srv.Now = func() time.Time { return f.now }
	srv.Log = slog.New(slog.NewTextHandler(f.logs, nil))
	f.h = srv.Handler()
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, bearer(jsonRequest(http.MethodPost, "/v1/forms", formJSON))(t))
	if rec.Code != http.StatusCreated {
		t.Fatalf("add form: %d %s", rec.Code, rec.Body)
	}
	f.form = decode[formOut](t, rec).ID
	if err := f.db.QueryRow(t.Context(), `SELECT account_id, signing_key FROM forms WHERE id = $1`, f.form).Scan(&f.acc, &f.key); err != nil {
		t.Fatal(err)
	}
	return f
}

// canSend gives the account a From on a verified domain, so double opt-in
// forms can send confirmation emails.
func (f *signupFixture) canSend(t *testing.T) {
	t.Helper()
	for _, q := range []string{
		`UPDATE accounts SET name = 'Pat''s Bakery', from_email = 'hello@patsbakery.com' WHERE id = $1`,
		`INSERT INTO domains (account_id, name, mail_from, created_at, check_until, status) VALUES ($1, 'patsbakery.com', 'bounce', now(), now(), 'verified')`,
	} {
		if _, err := f.db.Exec(t.Context(), q, f.acc); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

// values is a person's post through the embed code: stamped, with the page
// open four seconds.
func (f *signupFixture) values(email string) url.Values {
	return url.Values{
		forms.FieldEmail:     {email},
		forms.FieldFirstName: {"Ann"},
		forms.StampField:     {forms.Stamp(f.key, f.form, time.Now().Add(-time.Hour))},
		forms.ElapsedField:   {"4000"},
	}
}

// post sends a form post from ip, as a plain HTML form unless the request
// is changed by with.
func (f *signupFixture) post(vals url.Values, ip string, with ...func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, forms.ActionPath(f.form), strings.NewReader(vals.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "https://patsbakery.com")
	r.RemoteAddr = ip + ":40000"
	for _, w := range with {
		w(r)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, r)
	return rec
}

func acceptJSON(r *http.Request) { r.Header.Set("Accept", "application/json") }

func (f *signupFixture) status(t *testing.T, email string) string {
	t.Helper()
	var s string
	if err := f.db.QueryRow(t.Context(), `SELECT status FROM contacts WHERE account_id = $1 AND email = $2`, f.acc, email).Scan(&s); err != nil {
		return "none"
	}
	return s
}

// confirmPath is the path of the confirm link in the last email.
func (f *signupFixture) confirmPath(t *testing.T) string {
	t.Helper()
	sent, ok := f.fake.Last()
	if !ok || sent.Message.Kind != mail.KindConfirm {
		t.Fatalf("no confirmation email: %+v", sent)
	}
	i := strings.Index(sent.Message.Text, signups.ConfirmPrefix)
	if i < 0 {
		t.Fatalf("no confirm link in %q", sent.Message.Text)
	}
	return strings.Fields(sent.Message.Text[i:])[0]
}

func (f *signupFixture) page(t *testing.T, method, path string, want int) string {
	t.Helper()
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	if rec.Code != want {
		t.Fatalf("%s %s: %d, want %d: %s", method, path, rec.Code, want, rec.Body)
	}
	return rec.Body.String()
}

func (f *signupFixture) getForm(t *testing.T) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, bearer(func(*testing.T) *http.Request {
		return httptest.NewRequest(http.MethodGet, "/v1/forms/"+f.form, nil)
	})(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("get form: %d %s", rec.Code, rec.Body)
	}
	return decode[map[string]any](t, rec)
}

func TestFormSignupWithDoubleOptIn(t *testing.T) {
	f := newSignupFixture(t, `{"name":"Footer","fields":[{"name":"email"},{"name":"first_name","label":"First name"}]}`)
	f.canSend(t)

	rec := f.post(f.values("ann@example.com"), "192.0.2.10", func(r *http.Request) {
		r.Header.Set("Referer", "https://patsbakery.com/news")
	})
	if rec.Code != http.StatusOK || rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("post: %d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Thanks!") || !strings.Contains(body, `href="https://patsbakery.com/news"`) {
		t.Fatalf("page: %s", body)
	}
	if s := f.status(t, "ann@example.com"); s != contacts.StatusUnconfirmed {
		t.Fatalf("status %s, want unconfirmed", s)
	}
	sent, _ := f.fake.Last()
	if sent.Message.From.Email != "hello@patsbakery.com" || sent.Message.To.Email != "ann@example.com" {
		t.Fatalf("confirmation: %+v", sent.Message)
	}
	path := f.confirmPath(t)

	// Opening the link (as a mail scanner would) changes nothing.
	if page := f.page(t, http.MethodGet, path, http.StatusOK); !strings.Contains(page, "Confirm my subscription") ||
		!strings.Contains(page, "ann@example.com") {
		t.Fatalf("confirm page: %s", page)
	}
	if s := f.status(t, "ann@example.com"); s != contacts.StatusUnconfirmed {
		t.Fatalf("status after opening the link: %s", s)
	}
	if page := f.page(t, http.MethodPost, path, http.StatusOK); !strings.Contains(page, "You&#39;re subscribed") {
		t.Fatalf("confirmed page: %s", page)
	}
	if s := f.status(t, "ann@example.com"); s != contacts.StatusSubscribed {
		t.Fatalf("status after confirming: %s", s)
	}
	// Using it again changes nothing and says so.
	f.page(t, http.MethodPost, path, http.StatusOK)
	if strings.Contains(f.logs.String(), strings.TrimPrefix(path, signups.ConfirmPrefix)) {
		t.Fatal("the confirm token is in the logs")
	}

	// form get counts both.
	got := f.getForm(t)
	if got["signups"] != 1.0 || got["confirmations"] != 1.0 {
		t.Fatalf("counts: %v %v", got["signups"], got["confirmations"])
	}
	if p, ok := got["problems"].([]any); !ok || len(p) != 0 {
		t.Fatalf("problems: %v", got["problems"])
	}

	// The data request and the export carry the consent record.
	var contactID string
	if err := f.db.QueryRow(t.Context(), `SELECT id FROM contacts WHERE email = 'ann@example.com'`).Scan(&contactID); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	f.h.ServeHTTP(rec, bearer(func(*testing.T) *http.Request {
		return httptest.NewRequest(http.MethodGet, "/v1/contacts/"+contactID+"/data", nil)
	})(t))
	data := decode[struct {
		Consents []struct {
			FormID      string         `json:"form_id"`
			IP          string         `json:"ip"`
			Wording     map[string]any `json:"wording"`
			CreatedAt   time.Time      `json:"created_at"`
			ConfirmedAt *time.Time     `json:"confirmed_at"`
		} `json:"consents"`
	}](t, rec)
	if len(data.Consents) != 1 {
		t.Fatalf("consents: %s", rec.Body)
	}
	c := data.Consents[0]
	if c.FormID != f.form || c.IP != "192.0.2.10" || c.Wording["form_name"] != "Footer" || c.Wording["confirm_subject"] == nil ||
		c.CreatedAt.IsZero() || c.ConfirmedAt == nil {
		t.Fatalf("consent: %+v", c)
	}
	rec = httptest.NewRecorder()
	f.h.ServeHTTP(rec, bearer(func(*testing.T) *http.Request {
		return httptest.NewRequest(http.MethodGet, "/v1/contacts/export", nil)
	})(t))
	recs, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil || len(recs) != 2 {
		t.Fatalf("export: %v %q", err, rec.Body)
	}
	col := map[string]string{}
	for i, h := range recs[0] {
		col[h] = recs[1][i]
	}
	if col["signup_form_id"] != f.form || col["signup_ip"] != "192.0.2.10" || col["signup_at"] == "" || col["signup_confirmed_at"] == "" {
		t.Fatalf("export row: %v", col)
	}
}

func TestFormSignupAnswers(t *testing.T) {
	f := newSignupFixture(t, `{"name":"Footer","double_opt_in":false,"success_message":"You're in."}`)

	// The embed script asks for JSON.
	rec := f.post(f.values("ann@example.com"), "192.0.2.10", acceptJSON)
	if rec.Code != http.StatusOK || decode[map[string]string](t, rec)["message"] != "You're in." {
		t.Fatalf("json: %d %s", rec.Code, rec.Body)
	}
	if s := f.status(t, "ann@example.com"); s != contacts.StatusSubscribed {
		t.Fatalf("status %s, want subscribed", s)
	}
	if len(f.fake.Sent()) != 0 {
		t.Fatal("a form without double opt-in sent an email")
	}

	// An agent can post JSON.
	r := jsonRequest(http.MethodPost, forms.ActionPath(f.form),
		`{"email":"bo@example.com","_mg_ts":"`+forms.Stamp(f.key, f.form, time.Now())+`"}`)(t)
	rec = httptest.NewRecorder()
	f.h.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK || f.status(t, "bo@example.com") != contacts.StatusSubscribed {
		t.Fatalf("json post: %d %s", rec.Code, rec.Body)
	}

	// With a redirect URL, a plain form is sent there and the script is
	// told where.
	if _, err := f.db.Exec(t.Context(), `UPDATE forms SET redirect_url = 'https://patsbakery.com/thanks' WHERE id = $1`, f.form); err != nil {
		t.Fatal(err)
	}
	rec = f.post(f.values("cy@example.com"), "192.0.2.10")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "https://patsbakery.com/thanks" {
		t.Fatalf("redirect: %d %v", rec.Code, rec.Header())
	}
	rec = f.post(f.values("di@example.com"), "192.0.2.10", acceptJSON)
	if decode[map[string]string](t, rec)["redirect_url"] != "https://patsbakery.com/thanks" {
		t.Fatalf("json redirect: %s", rec.Body)
	}

	// Preflight from any site.
	pre := httptest.NewRequest(http.MethodOptions, forms.ActionPath(f.form), nil)
	pre.Header.Set("Origin", "https://patsbakery.com")
	pre.Header.Set("Access-Control-Request-Method", "POST")
	rec = httptest.NewRecorder()
	f.h.ServeHTTP(rec, pre)
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "*" ||
		!strings.Contains(rec.Header().Get("Access-Control-Allow-Methods"), "POST") {
		t.Fatalf("preflight: %d %v", rec.Code, rec.Header())
	}

	// An existing subscriber gets the form's tags, no email, and the same
	// answer.
	rec = f.post(f.values("ann@example.com"), "192.0.2.11", acceptJSON)
	if rec.Code != http.StatusOK || len(f.fake.Sent()) != 0 {
		t.Fatalf("again: %d %s", rec.Code, rec.Body)
	}
	if got := f.getForm(t); got["signups"] != 5.0 {
		t.Fatalf("signups %v, want 5", got["signups"])
	}
}

func TestFormSignupRefusals(t *testing.T) {
	f := newSignupFixture(t, `{"name":"Footer","double_opt_in":false}`)
	errCode := func(rec *httptest.ResponseRecorder) string {
		return decode[ErrorBody](t, rec).Error.Code
	}

	bad := f.values("ann@example.com")
	bad.Set(forms.StampField, "123.nope")
	if rec := f.post(bad, "192.0.2.10", acceptJSON); rec.Code != http.StatusBadRequest || errCode(rec) != "form_code_incomplete" {
		t.Fatalf("bad stamp: %d %s", rec.Code, rec.Body)
	}
	rec := f.post(f.values("not an email"), "192.0.2.10", acceptJSON)
	if rec.Code != http.StatusUnprocessableEntity || errCode(rec) != "invalid_signup" ||
		decode[ErrorBody](t, rec).Error.Details["field"] != "email" || rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("bad email: %d %s", rec.Code, rec.Body)
	}
	// The same for a plain form is a page.
	if rec := f.post(f.values("not an email"), "192.0.2.10"); rec.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rec.Body.String(), "Please check what you entered") {
		t.Fatalf("bad email page: %d %s", rec.Code, rec.Body)
	}

	// Bots get the success answer and nothing happens.
	bot := f.values("bot@example.com")
	bot.Set(forms.HoneypotField, "http://spam.example")
	fast := f.values("fast@example.com")
	fast.Set(forms.ElapsedField, "800")
	for _, v := range []url.Values{bot, fast} {
		if rec := f.post(v, "192.0.2.10", acceptJSON); rec.Code != http.StatusOK {
			t.Fatalf("bot: %d %s", rec.Code, rec.Body)
		}
	}
	if f.status(t, "bot@example.com") != "none" || f.status(t, "fast@example.com") != "none" {
		t.Fatal("a bot's post added a contact")
	}

	for _, path := range []string{"/f/" + noSuchID, "/f/nope"} {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(f.values("x@example.com").Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		acceptJSON(r)
		rec := httptest.NewRecorder()
		f.h.ServeHTTP(rec, r)
		if rec.Code != http.StatusNotFound || errCode(rec) != "form_not_found" {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
	rec = httptest.NewRecorder()
	f.h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, forms.ActionPath(f.form), nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d", rec.Code)
	}

	// A double opt-in form with nothing to send from refuses signups and
	// says why in form get.
	if _, err := f.db.Exec(t.Context(), `UPDATE forms SET double_opt_in = true WHERE id = $1`, f.form); err != nil {
		t.Fatal(err)
	}
	if rec := f.post(f.values("eve@example.com"), "192.0.2.10", acceptJSON); rec.Code != http.StatusServiceUnavailable || errCode(rec) != "form_not_ready" {
		t.Fatalf("not ready: %d %s", rec.Code, rec.Body)
	}
	if p, _ := f.getForm(t)["problems"].([]any); len(p) != 1 || p[0].(map[string]any)["code"] != "no_verified_from" {
		t.Fatalf("problems: %v", p)
	}

	// The confirmation email failing to send is a 502.
	f.canSend(t)
	f.fake.Err = errSendFailed
	if rec := f.post(f.values("eve@example.com"), "192.0.2.10", acceptJSON); rec.Code != http.StatusBadGateway || errCode(rec) != "confirmation_failed" {
		t.Fatalf("send failed: %d %s", rec.Code, rec.Body)
	}
}

var errSendFailed = errors.New("the provider is down")

func TestFormSignupThrottled(t *testing.T) {
	f := newSignupFixture(t, `{"name":"Footer","double_opt_in":false}`)
	// 100 posts in a minute from one IP: the first 10 go through, the rest
	// are throttled.
	ok, throttled := 0, 0
	for i := range 100 {
		rec := f.post(f.values("p"+strconv.Itoa(i)+"@example.com"), "192.0.2.10", acceptJSON)
		switch rec.Code {
		case http.StatusOK:
			ok++
		case http.StatusTooManyRequests:
			throttled++
			if rec.Header().Get("Retry-After") == "" {
				t.Fatal("429 without Retry-After")
			}
		default:
			t.Fatalf("post %d: %d %s", i, rec.Code, rec.Body)
		}
		f.now = f.now.Add(600 * time.Millisecond)
	}
	// Over the minute the limit refills by 10.
	if ok > 21 || throttled < 79 {
		t.Fatalf("%d went through, %d throttled", ok, throttled)
	}
	var n int
	if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM contacts WHERE account_id = $1`, f.acc).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != ok {
		t.Fatalf("%d contacts for %d accepted posts", n, ok)
	}
	// Another IP isn't held up by it.
	if rec := f.post(f.values("other@example.com"), "198.51.100.7", acceptJSON); rec.Code != http.StatusOK {
		t.Fatalf("other IP: %d", rec.Code)
	}

	// One form takes at most 600 posts a minute from everyone. Bots' posts
	// count, so the test needn't add 600 contacts.
	f.now = f.now.Add(time.Hour)
	limited := false
	for i := range 620 {
		v := f.values("bot@example.com")
		v.Set(forms.HoneypotField, "x")
		ip := "10." + strconv.Itoa(i/250) + "." + strconv.Itoa(i%250) + ".1"
		if rec := f.post(v, ip, acceptJSON); rec.Code == http.StatusTooManyRequests {
			limited = i >= signupsPerFormPerMinute
			break
		}
	}
	if !limited {
		t.Fatal("the form wasn't limited after 600 posts")
	}
}

func TestFormSignupContactLimit(t *testing.T) {
	f := newSignupFixture(t, `{"name":"Footer","double_opt_in":false}`)
	f.canSend(t)
	// Bo unsubscribed, then the account fills up to the free limit.
	if _, err := f.db.Exec(t.Context(), `
		INSERT INTO contacts (account_id, email, status, source) VALUES ($1, 'bo@example.com', 'unsubscribed', 'api')`, f.acc); err != nil {
		t.Fatal(err)
	}
	// Bo signs up again: even without double opt-in, they come back only by
	// confirming.
	if rec := f.post(f.values("bo@example.com"), "192.0.2.10", acceptJSON); rec.Code != http.StatusOK {
		t.Fatalf("bo: %d %s", rec.Code, rec.Body)
	}
	if f.status(t, "bo@example.com") != contacts.StatusUnsubscribed {
		t.Fatal("bo was subscribed without confirming")
	}
	path := f.confirmPath(t)
	if _, err := f.db.Exec(t.Context(), `
		INSERT INTO contacts (account_id, email, status, source)
		SELECT $1, 'c' || n || '@example.com', 'subscribed', 'api' FROM generate_series(1, $2::int) n`,
		f.acc, contacts.FreeContactLimit); err != nil {
		t.Fatal(err)
	}

	rec := f.post(f.values("new@example.com"), "192.0.2.11", acceptJSON)
	if rec.Code != http.StatusConflict || decode[ErrorBody](t, rec).Error.Code != "list_full" {
		t.Fatalf("signup over the limit: %d %s", rec.Code, rec.Body)
	}
	if page := f.page(t, http.MethodPost, path, http.StatusConflict); !strings.Contains(page, "isn&#39;t taking new subscribers") {
		t.Fatalf("confirm over the limit: %s", page)
	}
	if f.status(t, "bo@example.com") != contacts.StatusUnsubscribed {
		t.Fatal("bo was subscribed over the limit")
	}
}

// TestFormSignupDailyCap: past its daily cap (MG-92) a form answers 429
// form_signup_limit, as JSON or as a page, with Retry-After saying when
// it takes signups again, and adds nobody.
func TestFormSignupDailyCap(t *testing.T) {
	f := newSignupFixture(t, `{"name":"Footer","double_opt_in":false}`)
	f.canSend(t)
	f.srv.Signups.DailyCap = 1
	if rec := f.post(f.values("ann@example.com"), "192.0.2.10", acceptJSON); rec.Code != http.StatusOK {
		t.Fatalf("ann: %d %s", rec.Code, rec.Body)
	}
	rec := f.post(f.values("bo@example.com"), "192.0.2.11", acceptJSON)
	if rec.Code != http.StatusTooManyRequests || decode[ErrorBody](t, rec).Error.Code != "form_signup_limit" {
		t.Fatalf("bo: %d %s", rec.Code, rec.Body)
	}
	retry, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	if err != nil || retry < 86000 || retry > 86400 {
		t.Errorf("Retry-After %q", rec.Header().Get("Retry-After"))
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("no CORS header on the 429")
	}
	rec = f.post(f.values("bo@example.com"), "192.0.2.12")
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), "Please try again tomorrow") ||
		!strings.Contains(rec.Body.String(), "all the signups it can for today") {
		t.Fatalf("bo's page: %d %s", rec.Code, rec.Body)
	}
	if f.status(t, "bo@example.com") != "none" {
		t.Error("bo was added past the cap")
	}
}

func TestConfirmLinkPages(t *testing.T) {
	f := newSignupFixture(t, `{"name":"Footer"}`)
	f.canSend(t)
	if rec := f.post(f.values("ann@example.com"), "192.0.2.10"); rec.Code != http.StatusOK {
		t.Fatalf("post: %d %s", rec.Code, rec.Body)
	}
	path := f.confirmPath(t)

	f.page(t, http.MethodGet, signups.ConfirmPrefix+"nope", http.StatusBadRequest)
	f.page(t, http.MethodGet, signups.ConfirmPrefix, http.StatusBadRequest)
	f.page(t, http.MethodGet, signups.ConfirmPrefix+"c1"+strings.Repeat("A", 43), http.StatusNotFound)
	if _, err := f.db.Exec(t.Context(), `UPDATE consents SET confirm_expires_at = now() - interval '1 minute'`); err != nil {
		t.Fatal(err)
	}
	f.page(t, http.MethodPost, path, http.StatusGone)
	if f.status(t, "ann@example.com") != contacts.StatusUnconfirmed {
		t.Fatal("an expired link confirmed")
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, path, nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE: %d", rec.Code)
	}
}

func TestSignupPagesWithoutADatabase(t *testing.T) {
	h := serverWith(nil)(t).Handler()
	for _, r := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/f/"+noSuchID, strings.NewReader("email=a%40example.com")),
		httptest.NewRequest(http.MethodGet, signups.ConfirmPrefix+"c1"+strings.Repeat("A", 43), nil),
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s: %d", r.Method, r.URL.Path, rec.Code)
		}
	}
}

func TestFormPostJSONFields(t *testing.T) {
	var b formPost
	if err := json.Unmarshal([]byte(`{"_mg_ms":1500}`), &b); err != nil {
		t.Fatal(err)
	}
	if e := b.elapsed(); e == nil || *e != 1500*time.Millisecond {
		t.Fatalf("elapsed %v", e)
	}
}
