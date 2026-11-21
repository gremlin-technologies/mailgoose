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
	"github.com/gremlin-technologies/mailgoose/internal/mail"
	"github.com/gremlin-technologies/mailgoose/internal/senders"
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
	in := emailcheck.Input{Doc: e.Document, Safety: s.LinkSafety}
	var fromName string
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
		if st.FromName != nil {
			fromName = strings.TrimSpace(*st.FromName)
		}
	}
	// The email's own From wins over the account's default.
	if f := e.Document.Settings.FromEmail; f != nil && strings.TrimSpace(*f) != "" {
		in.FromEmail = strings.TrimSpace(*f)
	}
	if n := strings.TrimSpace(e.Document.Settings.FromName); n != "" {
		fromName = n
	}
	// Who it really goes from is worked out as a send would (MG-85),
	// including the default for a missing From (MG-108).
	if s.Domains != nil {
		var replyTo string
		if rt := e.Document.Settings.ReplyTo; rt != nil {
			replyTo = *rt
		}
		res := s.senders()
		sender, err := res.Resolve(r.Context(), c.AccountID, mail.Address{Name: fromName, Email: in.FromEmail}, replyTo)
		var badReplyTo *senders.ReplyToError
		switch {
		case errors.Is(err, senders.ErrNoFrom):
			// missing_from, as before.
		case err == nil:
			in.FromVerified = true
			if sender.Default {
				in.FromEmail, in.DefaultFrom = sender.Address, true
			}
			if sender.Shared {
				in.SharedFrom, in.SharedName = sender.From.Email, sender.From.Name
				in.SharedReplyTo = sender.ReplyTo[0].Email
			}
		case errors.Is(err, domains.ErrFromNotVerified):
			if res.SharedOn() {
				in.UnverifiedFrom = fmt.Sprintf("%s isn't on a verified domain or one of your verified sender addresses, so it can't send yet. "+
					"Your own sign-in address sends as it is; or verify this address to send from mailgoose's shared domain, or add its domain.", in.FromEmail)
			}
		case errors.As(err, &badReplyTo):
			in.FromVerified = true
			in.ReplyToProblem = fmt.Sprintf("The Reply-To %s isn't one of your verified sender addresses or on a verified domain, so an email from mailgoose's shared domain can't send replies there. "+
				"Verify it, or clear the Reply-To.", badReplyTo.Address)
		default:
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

// senders is s.Senders, or a Resolver with shared sending off, which needs
// no database of its own.
func (s *Server) senders() *senders.Resolver {
	if s.Senders != nil {
		return s.Senders
	}
	return &senders.Resolver{Domains: s.Domains}
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
