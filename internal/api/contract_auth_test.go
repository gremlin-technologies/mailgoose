package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"

	"github.com/gremlin-technologies/mailgoose/internal/auth"
	"github.com/gremlin-technologies/mailgoose/internal/campaigns"
	"github.com/gremlin-technologies/mailgoose/internal/contacts"
	"github.com/gremlin-technologies/mailgoose/internal/credits"
	"github.com/gremlin-technologies/mailgoose/internal/dbtest"
	"github.com/gremlin-technologies/mailgoose/internal/domains"
	"github.com/gremlin-technologies/mailgoose/internal/emails"
	"github.com/gremlin-technologies/mailgoose/internal/forms"
	"github.com/gremlin-technologies/mailgoose/internal/mail"
	"github.com/gremlin-technologies/mailgoose/internal/unsubscribe"
)

const signInBody = `{"email":"pat@example.com"}`

const signupBody = `{"email":"pat@example.com","account_name":"Pat's Bakery","accepted_terms":true}`

// outboxes holds each test's fake mail sender, so a later request in the
// same test can read the code an earlier one emailed.
var outboxes sync.Map // *testing.T -> *mail.Fake

// testDBs holds each test's database, for a before step that has to set up
// what no endpoint can yet.
var testDBs sync.Map // *testing.T -> *pgxpool.Pool

func authServer(t *testing.T) *Server {
	t.Helper()
	db := dbtest.New(t)
	fake := &mail.Fake{}
	outboxes.Store(t, fake)
	testDBs.Store(t, db)
	t.Cleanup(func() { outboxes.Delete(t); testDBs.Delete(t) })
	return &Server{
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:          db,
		Credits:     &credits.Ledger{DB: db},
		Contacts:    &contacts.Service{DB: db},
		Emails:      &emails.Service{DB: db},
		Forms:       &forms.Service{DB: db},
		Campaigns:   &campaigns.Service{DB: db},
		Unsubscribe: &unsubscribe.Service{DB: db},
		// Fake SES, and DNS that answers with every record asked for, so
		// domains verify on their first check.
		Domains: &domains.Service{DB: db, SES: &domains.FakeSES{}, DNS: domains.LocalDNS{Region: "us-east-2"},
			Region: "us-east-2", Reserved: []string{"mailgoose.app"}},
		Auth: &auth.Service{
			DB:   db,
			Mail: fake,
			From: mail.Address{Name: "mailgoose", Email: "signin@mail.test.example"},
		},
	}
}

func rateLimitedAuthServer(t *testing.T) *Server {
	s := authServer(t)
	s.RateLimitPerMinute = 1
	return s
}

func signedUp(t *testing.T, h http.Handler) {
	send(jsonRequest(http.MethodPost, "/v1/signup", signupBody))(t, h)
}

var sixDigits = regexp.MustCompile(`\b\d{6}\b`)

// sessionRequest posts a code for pat@example.com: the one in the test's
// last email when code is "", or code as given.
func sessionRequest(code string) func(*testing.T) *http.Request {
	return codeRequest("/v1/sessions", code)
}

// cliTokenRequest is sessionRequest for a CLI token.
func cliTokenRequest(code string) func(*testing.T) *http.Request {
	return codeRequest("/v1/cli-tokens", code)
}

func codeRequest(path, code string) func(*testing.T) *http.Request {
	return func(t *testing.T) *http.Request {
		c := code
		if c == "" {
			c = lastCode(t)
		}
		return jsonRequest(http.MethodPost, path, `{"email":"pat@example.com","code":"`+c+`"}`)(t)
	}
}

// lastCode is the six-digit code in the test's last email.
func lastCode(t *testing.T) string {
	t.Helper()
	v, ok := outboxes.Load(t)
	if !ok {
		t.Fatal("no outbox for this test")
	}
	last, ok := v.(*mail.Fake).Last()
	if !ok {
		t.Fatal("no email sent")
	}
	return sixDigits.FindString(last.Message.Text)
}

// cookies holds each test's session cookie, set by signedIn.
var sessionCookies sync.Map // *testing.T -> *http.Cookie

// signedIn signs pat@example.com up and in through h and keeps the session
// cookie for withSession.
func signedIn(t *testing.T, h http.Handler) {
	t.Helper()
	signedUp(t, h)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, sessionRequest("")(t))
	if rec.Code != http.StatusCreated {
		t.Fatalf("sign in: status %d: %s", rec.Code, rec.Body)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookie {
			sessionCookies.Store(t, c)
			t.Cleanup(func() { sessionCookies.Delete(t) })
			return
		}
	}
	t.Fatal("sign in set no session cookie")
}

// withSession sends the test's session cookie (from signedIn) or, with no
// signed-in session, a cookie that matches none.
func withSession(req func(*testing.T) *http.Request) func(*testing.T) *http.Request {
	return func(t *testing.T) *http.Request {
		r := req(t)
		c := &http.Cookie{Name: SessionCookie, Value: "no-such-session"}
		if v, ok := sessionCookies.Load(t); ok {
			c = v.(*http.Cookie)
		}
		r.AddCookie(c)
		return r
	}
}

// fromAnotherSite makes req look like a browser request from another site.
func fromAnotherSite(req func(*testing.T) *http.Request) func(*testing.T) *http.Request {
	return func(t *testing.T) *http.Request {
		r := req(t)
		r.Header.Set("Origin", "https://attacker.example")
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		return r
	}
}
