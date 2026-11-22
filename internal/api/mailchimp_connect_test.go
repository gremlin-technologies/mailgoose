package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/mailchimp"
)

// fakeMailchimpLogin stands in for login.mailchimp.com: it exchanges the
// code "good-code" and describes the account. Nothing in the tests reaches
// Mailchimp.
func fakeMailchimpLogin(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var exchanges atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/oauth2/token":
			exchanges.Add(1)
			_ = r.ParseForm()
			if r.PostForm.Get("code") != "good-code" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"tok-secret-value","expires_in":0,"scope":null}`))
		case "/oauth2/metadata":
			_, _ = w.Write([]byte(`{"dc":"us21","accountname":"Riverside <b>Bakery</b>"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &exchanges
}

// mailchimpOAuthServer is authServer with connecting set up, against a fake
// Mailchimp.
func mailchimpOAuthServer(t *testing.T) *Server {
	t.Helper()
	s := authServer(t)
	login, _ := fakeMailchimpLogin(t)
	s.MailchimpConnections.OAuth = &mailchimp.OAuth{ClientID: "client-1", ClientSecret: "secret-1",
		RedirectURL: "https://mailgoose.test" + MailchimpCallbackPath, LoginURL: login.URL}
	return s
}

var (
	connectMailchimp    = request(http.MethodPost, "/v1/imports/mailchimp/connect")
	mailchimpConnection = request(http.MethodGet, "/v1/imports/mailchimp/connection")
	disconnectMailchimp = request(http.MethodPost, "/v1/imports/mailchimp/disconnect")
)

func init() {
	limited := func(req func(*testing.T) *http.Request) contractCase {
		return contractCase{name: "rate limited", server: fullKey(rateLimitedAuthServer),
			before: send(bearer(request(http.MethodGet, "/v1/imports"))), req: bearer(req), status: http.StatusTooManyRequests}
	}
	contractCases["connectMailchimp"] = []contractCase{
		{name: "link made", server: draftKey(mailchimpOAuthServer), req: bearer(connectMailchimp), status: http.StatusOK},
		{name: "no credentials", server: mailchimpOAuthServer, req: connectMailchimp, status: http.StatusUnauthorized},
		{name: "from another site", server: mailchimpOAuthServer, before: signedIn, req: fromAnotherSite(withSession(connectMailchimp)), status: http.StatusForbidden},
		limited(connectMailchimp),
		{name: "not set up", server: fullKey(authServer), req: bearer(connectMailchimp), status: http.StatusServiceUnavailable},
		{name: "no database", server: serverWith(nil), req: bearer(connectMailchimp), status: http.StatusServiceUnavailable},
	}
	contractCases["getMailchimpConnection"] = []contractCase{
		{name: "none", server: draftKey(authServer), req: bearer(mailchimpConnection), status: http.StatusOK},
		{name: "no credentials", server: authServer, req: mailchimpConnection, status: http.StatusUnauthorized},
		limited(mailchimpConnection),
		{name: "no database", server: serverWith(nil), req: bearer(mailchimpConnection), status: http.StatusServiceUnavailable},
	}
	contractCases["disconnectMailchimp"] = []contractCase{
		{name: "disconnected", server: draftKey(authServer), req: bearer(disconnectMailchimp), status: http.StatusOK},
		{name: "no credentials", server: authServer, req: disconnectMailchimp, status: http.StatusUnauthorized},
		{name: "from another site", server: authServer, before: signedIn, req: fromAnotherSite(withSession(disconnectMailchimp)), status: http.StatusForbidden},
		limited(disconnectMailchimp),
		{name: "no database", server: serverWith(nil), req: bearer(disconnectMailchimp), status: http.StatusServiceUnavailable},
	}
}

// startConnect connects through h with the test's session and returns the
// state in the link.
func startConnect(t *testing.T, h http.Handler) string {
	t.Helper()
	rec := do(t, h, withSession(connectMailchimp)(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("connect: %d %s", rec.Code, rec.Body)
	}
	var out struct {
		AuthorizeURL string `json:"authorize_url"`
	}
	decodeInto(t, rec, &out)
	u, err := url.Parse(out.AuthorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	if u.Path != "/oauth2/authorize" || u.Query().Get("redirect_uri") != "https://mailgoose.test"+MailchimpCallbackPath {
		t.Fatalf("authorize URL = %s", out.AuthorizeURL)
	}
	return u.Query().Get("state")
}

func callback(query string) func(*testing.T) *http.Request {
	return request(http.MethodGet, MailchimpCallbackPath+"?"+query)
}

func TestConnectMailchimpThroughTheCallback(t *testing.T) {
	s := authServer(t)
	login, exchanges := fakeMailchimpLogin(t)
	s.MailchimpConnections.OAuth = &mailchimp.OAuth{ClientID: "client-1", ClientSecret: "secret-1",
		RedirectURL: "https://mailgoose.test" + MailchimpCallbackPath, LoginURL: login.URL}
	h := s.Handler()
	signedIn(t, h)

	// A browser without the account's session can't finish it, and the link
	// is used up.
	state := startConnect(t, h)
	rec := do(t, h, callback("code=good-code&state="+state)(t))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "Sign in to mailgoose first") {
		t.Fatalf("no session: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, withSession(callback("code=good-code&state="+state))(t)); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "This link has expired") {
		t.Fatalf("used link: %d %s", rec.Code, rec.Body)
	}
	if exchanges.Load() != 0 {
		t.Fatalf("%d exchanges", exchanges.Load())
	}

	// Denied on Mailchimp's page.
	state = startConnect(t, h)
	if rec := do(t, h, withSession(callback("error=access_denied&state="+state))(t)); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), "wasn&#39;t connected") {
		t.Fatalf("denied: %d %s", rec.Code, rec.Body)
	}

	// A code Mailchimp refuses.
	state = startConnect(t, h)
	if rec := do(t, h, withSession(callback("code=bad-code&state="+state))(t)); rec.Code != http.StatusBadGateway {
		t.Fatalf("bad code: %d %s", rec.Code, rec.Body)
	}

	// Connected, with the account's name escaped on the page.
	state = startConnect(t, h)
	rec = do(t, h, withSession(callback("code=good-code&state="+state))(t))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Mailchimp is connected") ||
		!strings.Contains(rec.Body.String(), "Riverside &lt;b&gt;Bakery&lt;/b&gt;") || rec.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("connected: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "tok-secret-value") {
		t.Fatal("the page shows the token")
	}

	rec = do(t, h, withSession(mailchimpConnection)(t))
	var conn struct {
		Connected   bool    `json:"connected"`
		AccountName *string `json:"account_name"`
		DataCenter  *string `json:"data_center"`
		ExpiresAt   *string `json:"expires_at"`
	}
	decodeInto(t, rec, &conn)
	if !conn.Connected || conn.AccountName == nil || *conn.AccountName != "Riverside <b>Bakery</b>" || *conn.DataCenter != "us21" || conn.ExpiresAt == nil {
		t.Fatalf("connection = %s", rec.Body)
	}
	if strings.Contains(rec.Body.String(), "tok-") {
		t.Fatal("the API returns the token")
	}
	var stored int
	db, _ := testDBs.Load(t)
	if err := db.(*pgxpool.Pool).QueryRow(t.Context(), `SELECT count(*) FROM mailchimp_connections WHERE position('tok-secret-value'::bytea in token) > 0`).Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("token stored in the clear: %d %v", stored, err)
	}

	rec = do(t, h, withSession(disconnectMailchimp)(t))
	decodeInto(t, rec, &conn)
	if rec.Code != http.StatusOK || conn.Connected {
		t.Fatalf("disconnect: %d %s", rec.Code, rec.Body)
	}
	decodeInto(t, do(t, h, withSession(mailchimpConnection)(t)), &conn)
	if conn.Connected {
		t.Fatal("still connected")
	}
}

func TestMailchimpCallbackPages(t *testing.T) {
	h := authServer(t).Handler()
	// Not set up here.
	if rec := do(t, h, callback("code=x&state=y")(t)); rec.Code != http.StatusServiceUnavailable ||
		!strings.Contains(rec.Body.String(), "Upload Mailchimp") {
		t.Fatalf("not set up: %d %s", rec.Code, rec.Body)
	}
	h = mailchimpOAuthServer(t).Handler()
	if rec := do(t, h, callback("code=x&state=unknown")(t)); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown state: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, request(http.MethodPost, MailchimpCallbackPath)(t)); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("post: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, serverWith(nil)(t).Handler(), callback("code=x&state=y")(t)); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no database: %d %s", rec.Code, rec.Body)
	}
}

func TestConnectMailchimpNotSetUpPointsToTheUpload(t *testing.T) {
	h := fullKey(authServer)(t).Handler()
	rec := do(t, h, bearer(connectMailchimp)(t))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "mailchimp_not_configured") ||
		!strings.Contains(rec.Body.String(), "mailgoose import mailchimp upload") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}
