package api

import (
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/api/apigen"
	"github.com/gremlin-technologies/mailgoose/internal/auth"
	"github.com/gremlin-technologies/mailgoose/internal/sending"
)

// CreateSignIn is POST /v1/sign-in.
func (s *Server) CreateSignIn(w http.ResponseWriter, r *http.Request) {
	if s.authUnavailable(w) {
		return
	}
	var req apigen.SignInRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, err)
		return
	}
	if s.authError(w, r, s.Auth.RequestSignIn(r.Context(), req.Email, ClientIP(r))) {
		return
	}
	writeJSON(w, http.StatusAccepted, apigen.CodeSent{
		Status:  apigen.CodeSentStatusCodeSent,
		Message: "If that address has an account, a six-digit code is on its way. It works for 10 minutes.",
	})
}

// GetCurrentSession is GET /v1/sessions/current.
func (s *Server) GetCurrentSession(w http.ResponseWriter, r *http.Request) {
	cur, ok := s.currentSession(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, sessionJSON(cur.Account, cur.User, cur.ExpiresAt))
}

// DeleteCurrentSession is DELETE /v1/sessions/current.
func (s *Server) DeleteCurrentSession(w http.ResponseWriter, r *http.Request) {
	cur, ok := s.currentSession(w, r)
	if !ok {
		return
	}
	if s.authError(w, r, s.Auth.SignOut(r.Context(), cur.SessionID)) {
		return
	}
	clearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

// DeleteAllSessions is DELETE /v1/sessions: sign out everywhere.
func (s *Server) DeleteAllSessions(w http.ResponseWriter, r *http.Request) {
	cur, ok := s.currentSession(w, r)
	if !ok {
		return
	}
	if s.authError(w, r, s.Auth.SignOutEverywhere(r.Context(), cur.User.ID)) {
		return
	}
	clearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

// currentSession returns the web session the request's cookie belongs to,
// or writes a 401 (or 503) and reports false. API keys don't count here.
func (s *Server) currentSession(w http.ResponseWriter, r *http.Request) (*auth.Current, bool) {
	c := s.requireCaller(w, r)
	if c == nil {
		return nil, false
	}
	if c.Session == nil {
		WriteError(w, Unauthorized("unauthenticated", "This needs a web session; API keys and CLI tokens can't use it.").
			WithHint("Sign in to the web app."))
		return nil, false
	}
	return c.Session, true
}

// secureCookie reports whether the session cookie is marked Secure. It always
// is, except on a plain-HTTP request straight to this machine (the local
// stack, as the end-to-end test runs it): WebKit won't store a Secure cookie
// from http://localhost, though Chrome and Firefox do (MG-59). A request
// through the load balancer carries X-Forwarded-Proto, so it stays Secure.
func secureCookie(r *http.Request) bool {
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") != "" {
		return true
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if strings.EqualFold(host, "localhost") {
		return false
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip == nil || !ip.IsLoopback()
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: Secure except on the local stack, see secureCookie.
		Name:     SessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(time.Until(expires).Seconds()),
		HttpOnly: true,
		Secure:   secureCookie(r),
		SameSite: http.SameSiteLaxMode,
	})
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: Secure except on the local stack, see secureCookie.
		Name:     SessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secureCookie(r),
		SameSite: http.SameSiteLaxMode,
	})
}

func sessionJSON(a auth.Account, u auth.User, expires time.Time) apigen.Session {
	return apigen.Session{Account: accountSummaryJSON(a), User: userJSON(u), ExpiresAt: expires.UTC()}
}

func accountSummaryJSON(a auth.Account) apigen.Account {
	return apigen.Account{
		Id:          a.ID,
		Name:        a.Name,
		Status:      apigen.AccountStatus(a.Status),
		Plan:        apigen.AccountPlan(a.Plan),
		CreatedAt:   a.CreatedAt.UTC(),
		Suspension:  suspensionJSON(a.Suspension),
		InReview:    a.InReview,
		SendingMode: apigen.SendingMode(a.SendingMode),
	}
}

func suspensionJSON(s *auth.Suspension) *apigen.AccountSuspension {
	if s == nil {
		return nil
	}
	return &apigen.AccountSuspension{
		SuspendedAt: s.At.UTC(),
		Reason:      s.Reason,
		ExportUntil: s.At.Add(sending.SuspensionExport).UTC(),
	}
}

func userJSON(u auth.User) apigen.User {
	return apigen.User{Id: u.ID, Email: u.Email, CreatedAt: u.CreatedAt.UTC()}
}

// writeRateLimited answers a code-request limit with a 429 and Retry-After.
func writeRateLimited(w http.ResponseWriter, retryAfter time.Duration) {
	secs := max(1, int(math.Ceil(retryAfter.Seconds())))
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	WriteError(w, TooManyRequests("rate_limited", "Too many code requests for this address or network.").
		WithHint("Wait "+strconv.Itoa(secs)+" seconds (see Retry-After) and try again."))
}

// writeTooManyWrongCodes answers an address over the cap on wrong codes with
// a 429 and Retry-After (MG-123).
func writeTooManyWrongCodes(w http.ResponseWriter, retryAfter time.Duration) {
	secs := max(1, int(math.Ceil(retryAfter.Seconds())))
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	WriteError(w, TooManyRequests("too_many_wrong_codes", "Too many wrong codes for this address.").
		WithHint("Wait "+strconv.Itoa(secs)+" seconds (see Retry-After), then request a new code and try again."))
}

// crossOriginProtection rejects browser requests that change something, come
// from another origin and carry no Authorization header: the requests a
// session cookie would otherwise authenticate on another site's behalf.
// Requests with an Authorization header (API keys) and non-browser clients,
// which send neither Sec-Fetch-Site nor Origin, pass.
func (s *Server) crossOriginProtection(next http.Handler) http.Handler {
	cop := http.NewCrossOriginProtection()
	for _, o := range s.TrustedOrigins {
		if err := cop.AddTrustedOrigin(o); err != nil {
			s.Log.Error("ignoring a bad trusted origin", "origin", o, "err", err)
		}
	}
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		WriteError(w, Forbidden("cross_origin_request", "This request came from another site, so it was refused.").
			WithHint("Make the change from the mailgoose web app, or use an API key."))
	}))
	protected := cop.Handler(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			next.ServeHTTP(w, r)
			return
		}
		protected.ServeHTTP(w, r)
	})
}
