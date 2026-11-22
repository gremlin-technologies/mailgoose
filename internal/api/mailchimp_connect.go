package api

import (
	"errors"
	"net/http"

	"github.com/gremlin-technologies/mailgoose/internal/api/apigen"
	"github.com/gremlin-technologies/mailgoose/internal/mailchimp"
)

// Connecting a Mailchimp account with OAuth (MG-73).

// MailchimpCallbackPath is where Mailchimp sends the browser back. It's a
// page for people, outside the spec, like the unsubscribe page; it needs the
// browser's web session.
const MailchimpCallbackPath = "/oauth/mailchimp/callback"

// mailchimpCallbacksPerMinute limits callbacks per IP.
const mailchimpCallbacksPerMinute = 30

// connectionsCaller is the caller of a connection endpoint, or nil after
// writing the error.
func (s *Server) connectionsCaller(w http.ResponseWriter, r *http.Request) *Caller {
	c := s.requireCaller(w, r)
	if c != nil && s.MailchimpConnections == nil {
		WriteError(w, &Error{Status: http.StatusServiceUnavailable, Code: "service_unavailable",
			Message: "Connecting Mailchimp needs a database, and none is configured."})
		return nil
	}
	return c
}

var errMailchimpNotConfigured = (&Error{Status: http.StatusServiceUnavailable, Code: "mailchimp_not_configured",
	Message: "Connecting a Mailchimp account isn't set up here."}).
	WithHint("Upload Mailchimp's export instead: `mailgoose import mailchimp upload <file>`.")

// ConnectMailchimp (POST /v1/imports/mailchimp/connect) makes a connect link.
func (s *Server) ConnectMailchimp(w http.ResponseWriter, r *http.Request) {
	c := s.connectionsCaller(w, r)
	if c == nil {
		return
	}
	u, expires, err := s.MailchimpConnections.Start(r.Context(), c.AccountID)
	switch {
	case errors.Is(err, mailchimp.ErrNotConfigured):
		WriteError(w, errMailchimpNotConfigured)
		return
	case err != nil:
		s.Log.ErrorContext(r.Context(), "starting a Mailchimp connect failed", "err", err)
		WriteError(w, Internal())
		return
	}
	writeJSON(w, http.StatusOK, apigen.MailchimpConnect{AuthorizeUrl: u, ExpiresAt: expires})
}

// GetMailchimpConnection (GET /v1/imports/mailchimp/connection).
func (s *Server) GetMailchimpConnection(w http.ResponseWriter, r *http.Request) {
	c := s.connectionsCaller(w, r)
	if c == nil {
		return
	}
	conn, err := s.MailchimpConnections.Get(r.Context(), c.AccountID)
	if err != nil {
		s.Log.ErrorContext(r.Context(), "reading a Mailchimp connection failed", "err", err)
		WriteError(w, Internal())
		return
	}
	writeJSON(w, http.StatusOK, connectionJSON(conn, s.MailchimpConnections.Configured()))
}

// DisconnectMailchimp (POST /v1/imports/mailchimp/disconnect).
func (s *Server) DisconnectMailchimp(w http.ResponseWriter, r *http.Request) {
	c := s.connectionsCaller(w, r)
	if c == nil {
		return
	}
	if err := s.MailchimpConnections.Forget(r.Context(), c.AccountID); err != nil {
		s.Log.ErrorContext(r.Context(), "forgetting a Mailchimp connection failed", "err", err)
		WriteError(w, Internal())
		return
	}
	writeJSON(w, http.StatusOK, connectionJSON(nil, s.MailchimpConnections.Configured()))
}

// connectionJSON is the account's connection; canConnect says whether
// connecting is set up here at all.
func connectionJSON(c *mailchimp.Connection, canConnect bool) apigen.MailchimpConnection {
	if c == nil {
		return apigen.MailchimpConnection{CanConnect: canConnect}
	}
	out := apigen.MailchimpConnection{Connected: true, CanConnect: canConnect, DataCenter: &c.DataCenter}
	if c.AccountName != "" {
		out.AccountName = &c.AccountName
	}
	at, until := c.ConnectedAt.UTC(), c.ExpiresAt.UTC()
	out.ConnectedAt, out.ExpiresAt = &at, &until
	return out
}

// mailchimpWebPage is the web app's Mailchimp page, which the callback's
// pages link back to.
const mailchimpWebPage = "/contacts/import/mailchimp"

var (
	mailchimpConnectedPage = func(name string) page {
		what := "your Mailchimp account"
		if name != "" {
			what = "the Mailchimp account " + name
		}
		return page{
			Title: "Mailchimp is connected",
			Body: []string{
				"mailgoose can now read " + what + " to import it. It only reads, and forgets the access once the import is done, after 24 hours at the latest, or when you disconnect.",
				"Carry on where you started: in the web app, follow the link below to pick the audiences to bring; in the CLI, run mailgoose import mailchimp audiences.",
			},
			Back: mailchimpWebPage,
		}
	}
	mailchimpDeniedPage = page{
		Title: "Mailchimp wasn't connected",
		Body:  []string{"Access wasn't allowed on Mailchimp's page, so nothing was connected.", "To try again, start connecting again."},
		Back:  mailchimpWebPage,
	}
	mailchimpExpiredPage = page{
		Title: "This link has expired",
		Body: []string{"A connect link works once, for 10 minutes, so nothing was connected.",
			"Start connecting again from the web app, or with mailgoose import mailchimp connect."},
	}
	mailchimpSignInPage = page{
		Title: "Sign in to mailgoose first",
		Body: []string{"This browser isn't signed in to the mailgoose account that started connecting Mailchimp, so nothing was connected.",
			"Sign in to mailgoose in this browser, then start connecting again."},
	}
	mailchimpFailedPage = page{
		Title: "Mailchimp couldn't be connected",
		Body: []string{"Mailchimp didn't complete the connection, and nothing was saved.",
			"Try again in a few minutes. If it keeps failing, upload Mailchimp's export instead."},
	}
	mailchimpNotSetUpPage = page{
		Title: "Connecting Mailchimp isn't set up here",
		Body:  []string{"Nothing was connected. Upload Mailchimp's export instead."},
	}
	mailchimpCallbackBusyPage = page{
		Title: "Too many tries just now",
		Body:  []string{"Please wait a minute, then start connecting again. Nothing was connected."},
	}
)

// mailchimpCallback finishes a connect. It needs the browser's web session
// for the account that started it.
func (s *Server) mailchimpCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writePage(w, http.StatusMethodNotAllowed, page{Title: "This page can't do that", Body: []string{"Mailchimp sends you here after you allow access."}})
		return
	}
	if s.MailchimpConnections == nil {
		writePage(w, http.StatusServiceUnavailable, mailchimpNotSetUpPage)
		return
	}
	signedIn := ""
	if c := CallerFrom(r.Context()); c != nil && c.Session != nil {
		signedIn = c.AccountID
	}
	q := r.URL.Query()
	conn, err := s.MailchimpConnections.Finish(r.Context(), q.Get("state"), signedIn, q.Get("code"), q.Get("error"))
	var oe *mailchimp.ErrOAuth
	switch {
	case err == nil:
		writePage(w, http.StatusOK, mailchimpConnectedPage(conn.AccountName))
	case errors.Is(err, mailchimp.ErrNotConfigured):
		writePage(w, http.StatusServiceUnavailable, mailchimpNotSetUpPage)
	case errors.Is(err, mailchimp.ErrStateNotFound):
		writePage(w, http.StatusBadRequest, mailchimpExpiredPage)
	case errors.Is(err, mailchimp.ErrDenied):
		writePage(w, http.StatusOK, mailchimpDeniedPage)
	case errors.Is(err, mailchimp.ErrWrongAccount):
		writePage(w, http.StatusForbidden, mailchimpSignInPage)
	case errors.As(err, &oe):
		s.Log.WarnContext(r.Context(), "connecting a Mailchimp account failed", "err", oe.Error())
		writePage(w, http.StatusBadGateway, mailchimpFailedPage)
	default:
		s.Log.ErrorContext(r.Context(), "finishing a Mailchimp connect failed", "err", err)
		writePage(w, http.StatusInternalServerError, mailchimpFailedPage)
	}
}

// mailchimpCallbackLimited answers a rate-limited callback.
func mailchimpCallbackLimited(w http.ResponseWriter, _ *http.Request, _ int) {
	writePage(w, http.StatusTooManyRequests, mailchimpCallbackBusyPage)
}
