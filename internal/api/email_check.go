package api

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

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
	links := s.Links
	if links != nil && s.linkChecks != nil {
		if ok, _, wait := s.linkChecks.allow("account:" + c.AccountID); !ok {
			links = linksNotFetched{wait: wait}
		}
	}
	res := emailcheck.Check(r.Context(), in, links)
	writeJSON(w, http.StatusOK, emailCheckResult{OK: res.OK(), Result: res})
}

// linkChecksPerMinute is how many email checks a minute may fetch links for
// one account. Each check fetches up to emailcheck.MaxLinks links, so this
// keeps the check from being used to send many requests to other people's
// servers. Checks past it still run; their links come back not checked.
const linkChecksPerMinute = 10

// linksNotFetched answers for an account that has checked links too often
// in the last minute.
type linksNotFetched struct{ wait time.Duration }

func (l linksNotFetched) CheckLinks(_ context.Context, urls []string) map[string]emailcheck.LinkStatus {
	secs := int(math.Ceil(l.wait.Seconds()))
	if secs < 1 {
		secs = 1
	}
	detail := fmt.Sprintf("links were checked %d times in the last minute; check again in %d seconds", linkChecksPerMinute, secs)
	out := make(map[string]emailcheck.LinkStatus, len(urls))
	for _, u := range urls {
		out[u] = emailcheck.LinkStatus{Result: emailcheck.ResultNotChecked, Detail: detail}
	}
	return out
}
