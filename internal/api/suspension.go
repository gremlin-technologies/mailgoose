package api

import (
	"net/http"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/auth"
	"github.com/gremlin-technologies/mailgoose/internal/sending"
)

// A suspended account (MG-65) has no API keys or CLI logins, but its owner
// can still sign in to the web app: to read why, to export contacts and
// campaigns for sending.SuspensionExport, and to sign out.

// alwaysWhileSuspended are the reads a suspended account's session always
// has: who's signed in and the account, which say why.
var alwaysWhileSuspended = map[string]bool{
	"/v1/sessions/current": true,
	"/v1/account":          true,
	"/v1/whoami":           true,
}

// publicPosts take no credentials, so a suspended account's cookie or key
// sent with them changes nothing: they answer for themselves.
var publicPosts = map[string]bool{
	"/v1/signup":              true,
	"/v1/sign-in":             true,
	"/v1/sessions":            true,
	"/v1/cli-tokens":          true,
	"/v1/device-logins":       true,
	"/v1/device-logins/token": true,
}

// allowedWhileSuspended reports whether a suspended account may make r:
// signing out at any time, and reading what the web app's suspension page
// and the data export need, until the export window ends.
func allowedWhileSuspended(r *http.Request, s *auth.Suspension, now time.Time) bool {
	switch r.Method {
	case http.MethodPost:
		return publicPosts[r.URL.Path]
	case http.MethodDelete:
		return r.URL.Path == "/v1/sessions/current" || r.URL.Path == "/v1/sessions"
	case http.MethodGet, http.MethodHead:
		if alwaysWhileSuspended[r.URL.Path] {
			return true
		}
		return now.Before(s.At.Add(sending.SuspensionExport))
	}
	return false
}

// suspendedError is the 403 for everything else.
func (s *Server) suspendedError(sus *auth.Suspension) *Error {
	appeal := "To appeal, reply to the email about the suspension"
	if s.AbuseEmail != "" {
		appeal += " (it goes to " + s.AbuseEmail + ")"
	}
	return Forbidden("account_suspended", "This account is suspended, so it can't do this. "+sus.Reason).
		WithHint(appeal + ". Until " + sus.At.Add(sending.SuspensionExport).UTC().Format("January 2, 2006") +
			", you can sign in to the web app to export your contacts and campaigns.").
		WithDetails(map[string]any{
			"suspended_at": sus.At.UTC(),
			"export_until": sus.At.Add(sending.SuspensionExport).UTC(),
		})
}
