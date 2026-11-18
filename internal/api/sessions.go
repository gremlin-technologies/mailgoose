package api

import (
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/api/apigen"
	"github.com/gremlin-technologies/mailgoose/internal/auth"
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
	clearSessionCookie(w)
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
	clearSessionCookie(w)
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

func setSessionCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(time.Until(expires).Seconds()),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

func sessionJSON(a auth.Account, u auth.User, expires time.Time) apigen.Session {
	return apigen.Session{
		Account: apigen.Account{
			Id:        a.ID,
			Name:      a.Name,
			Status:    apigen.AccountStatus(a.Status),
			Plan:      apigen.AccountPlan(a.Plan),
			CreatedAt: a.CreatedAt.UTC(),
		},
		User: apigen.User{
			Id:        u.ID,
			Email:     u.Email,
			CreatedAt: u.CreatedAt.UTC(),
		},
		ExpiresAt: expires.UTC(),
	}
}

// writeRateLimited answers a code-request limit with a 429 and Retry-After.
func writeRateLimited(w http.ResponseWriter, retryAfter time.Duration) {
	secs := max(1, int(math.Ceil(retryAfter.Seconds())))
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	WriteError(w, TooManyRequests("rate_limited", "Too many code requests for this address or network.").
		WithHint("Wait "+strconv.Itoa(secs)+" seconds (see Retry-After) and try again."))
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
