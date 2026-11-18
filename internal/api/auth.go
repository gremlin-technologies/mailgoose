package api

import (
	"errors"
	"net/http"

	"github.com/gremlin-technologies/mailgoose/internal/api/apigen"
	"github.com/gremlin-technologies/mailgoose/internal/auth"
)

// SessionCookie is the web session cookie's name.
const SessionCookie = "mg_session"

func (s *Server) authUnavailable(w http.ResponseWriter) bool {
	if s.Auth != nil {
		return false
	}
	WriteError(w, &Error{Status: http.StatusServiceUnavailable, Code: "service_unavailable",
		Message: "Sign-up and sign-in need a database, and none is configured."})
	return true
}

// CreateSignup is POST /v1/signup.
func (s *Server) CreateSignup(w http.ResponseWriter, r *http.Request) {
	if s.authUnavailable(w) {
		return
	}
	var req apigen.SignupRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, err)
		return
	}
	err := s.Auth.SignUp(r.Context(), auth.SignUpInput{
		Email:         req.Email,
		AccountName:   req.AccountName,
		AcceptedTerms: req.AcceptedTerms,
		IP:            ClientIP(r),
	})
	if s.authError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusAccepted, apigen.CodeSent{
		Status:  apigen.CodeSentStatusCodeSent,
		Message: "Check your email for a six-digit code. It works for 10 minutes.",
	})
}

// CreateSession is POST /v1/sessions.
func (s *Server) CreateSession(w http.ResponseWriter, r *http.Request) {
	if s.authUnavailable(w) {
		return
	}
	var req apigen.SessionRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, err)
		return
	}
	sess, err := s.Auth.Verify(r.Context(), req.Email, req.Code, auth.SessionMeta{
		UserAgent: r.UserAgent(),
		IP:        ClientIP(r),
	})
	if errors.Is(err, auth.ErrInvalidCode) {
		WriteError(w, Unprocessable("invalid_code", "That code is wrong, expired or already used.").
			WithHint("Request a new code and use it within 10 minutes. A code stops working after 5 wrong tries."))
		return
	}
	if s.authError(w, r, err) {
		return
	}
	setSessionCookie(w, sess.Token, sess.ExpiresAt)
	writeJSON(w, http.StatusCreated, sessionJSON(sess.Account, sess.User, sess.ExpiresAt))
}

// CreateCliToken is POST /v1/cli-tokens: an emailed code for a CLI token,
// for `mailgoose signup`.
func (s *Server) CreateCliToken(w http.ResponseWriter, r *http.Request) {
	if s.authUnavailable(w) {
		return
	}
	var req apigen.CliTokenRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, err)
		return
	}
	clientName := ""
	if req.ClientName != nil {
		clientName = *req.ClientName
	}
	t, err := s.Auth.VerifyForCLI(r.Context(), req.Email, req.Code, clientName)
	if errors.Is(err, auth.ErrInvalidCode) {
		WriteError(w, Unprocessable("invalid_code", "That code is wrong, expired or already used.").
			WithHint("Ask for a new code with `mailgoose signup` and use it within 10 minutes. A code stops working after 5 wrong tries."))
		return
	}
	if s.authError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, apigen.DeviceLoginToken{
		Token:   t.Token.Secret,
		KeyId:   t.Token.ID,
		Account: accountSummaryJSON(t.Account),
		User:    userJSON(t.User),
	})
}

// authError writes err when there is one and reports whether it did.
func (s *Server) authError(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	var rl *auth.RateLimitError
	if errors.As(err, &rl) {
		writeRateLimited(w, rl.RetryAfter)
		return true
	}
	var ve *auth.ValidationError
	if errors.As(err, &ve) {
		WriteError(w, Unprocessable(ve.Code, ve.Message))
		return true
	}
	s.Log.ErrorContext(r.Context(), "auth request failed", "err", err)
	WriteError(w, Internal())
	return true
}
