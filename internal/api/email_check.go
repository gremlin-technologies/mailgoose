package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gremlin-technologies/mailgoose/internal/domains"
	"github.com/gremlin-technologies/mailgoose/internal/emailcheck"
)

// emailCheckResult is checkEmail's answer.
type emailCheckResult struct {
	OK bool `json:"ok"`
	emailcheck.Result
}

// CheckEmail is POST /v1/emails/{email_id}/check (DES-12).
func (s *Server) CheckEmail(w http.ResponseWriter, r *http.Request, id string) {
	c := s.emailsCaller(w, r)
	if c == nil || !validEmailID(w, id) {
		return
	}
	e, err := s.Emails.Get(r.Context(), c.AccountID, id)
	if s.emailError(w, r, err) {
		return
	}
	in := emailcheck.Input{Doc: e.Document}
	if s.Auth != nil {
		st, err := s.Auth.AccountSettings(r.Context(), c.AccountID)
		if err != nil {
			s.emailError(w, r, err)
			return
		}
		if st.PostalAddress != nil {
			in.PostalAddress = strings.TrimSpace(*st.PostalAddress)
		}
		if st.FromEmail != nil {
			in.FromEmail = *st.FromEmail
		}
	}
	// The email's own From wins over the account's default.
	if f := e.Document.Settings.FromEmail; f != nil && strings.TrimSpace(*f) != "" {
		in.FromEmail = strings.TrimSpace(*f)
	}
	if in.FromEmail != "" && s.Domains != nil {
		err := s.Domains.RequireVerifiedFrom(r.Context(), c.AccountID, in.FromEmail)
		switch {
		case err == nil:
			in.FromVerified = true
		case !errors.Is(err, domains.ErrFromNotVerified):
			s.emailError(w, r, err)
			return
		}
	}
	res := emailcheck.Check(r.Context(), in, s.Links)
	writeJSON(w, http.StatusOK, emailCheckResult{OK: res.OK(), Result: res})
}
