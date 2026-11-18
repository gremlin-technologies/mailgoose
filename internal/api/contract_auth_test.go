package api

import (
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"sync"
	"testing"

	"github.com/gremlin-technologies/mailgoose/internal/auth"
	"github.com/gremlin-technologies/mailgoose/internal/dbtest"
	"github.com/gremlin-technologies/mailgoose/internal/mail"
)

const signupBody = `{"email":"pat@example.com","account_name":"Pat's Bakery","accepted_terms":true}`

// outboxes holds each test's fake mail sender, so a later request in the
// same test can read the code an earlier one emailed.
var outboxes sync.Map // *testing.T -> *mail.Fake

func authServer(t *testing.T) *Server {
	t.Helper()
	db := dbtest.New(t)
	fake := &mail.Fake{}
	outboxes.Store(t, fake)
	t.Cleanup(func() { outboxes.Delete(t) })
	return &Server{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:  db,
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
	return func(t *testing.T) *http.Request {
		if code == "" {
			v, ok := outboxes.Load(t)
			if !ok {
				t.Fatal("no outbox for this test")
			}
			last, ok := v.(*mail.Fake).Last()
			if !ok {
				t.Fatal("no email sent")
			}
			code = sixDigits.FindString(last.Message.Text)
		}
		return jsonRequest(http.MethodPost, "/v1/sessions", `{"email":"pat@example.com","code":"`+code+`"}`)(t)
	}
}
