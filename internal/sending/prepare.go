// Package sending turns a campaign into the messages that go out: test
// sends (CAM-2), launch (CAM-3 to CAM-5), and the send workers later
// (MG-31).
// Prepare is the step they share: it loads the campaign's email and the
// account's settings, works out the From, checks it may be used, and
// compiles the email once, ready to fill in per recipient.
package sending

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/auth"
	"github.com/gremlin-technologies/mailgoose/internal/campaigns"
	"github.com/gremlin-technologies/mailgoose/internal/domains"
	"github.com/gremlin-technologies/mailgoose/internal/emails"
	"github.com/gremlin-technologies/mailgoose/internal/linkrisk"
	"github.com/gremlin-technologies/mailgoose/internal/mail"
	"github.com/gremlin-technologies/mailgoose/internal/render"
	"github.com/gremlin-technologies/mailgoose/internal/unsubscribe"
)

// Service prepares and sends a campaign's email.
type Service struct {
	DB          *pgxpool.Pool
	Campaigns   *campaigns.Service
	Emails      *emails.Service
	Settings    *auth.Service
	Domains     VerifiedFrom
	Unsubscribe *unsubscribe.Service
	Mail        mail.Sender
	// Limits are the send limits, for showing an account its own.
	Limits Limits
	// Slicing is how a new account's campaigns are sliced (MG-62); zero
	// values are the defaults.
	Slicing Slicing
	// LinkSafety looks links up on the phishing and malware lists at test
	// and launch (MG-64); nil skips that.
	LinkSafety *linkrisk.Checker
	// Alert tells the ops address about an account paused for an unsafe
	// link; nil only logs it.
	Alert AlertFunc
	Log   *slog.Logger
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// VerifiedFrom is what Prepare needs from the domains service.
type VerifiedFrom interface {
	RequireVerifiedFrom(ctx context.Context, accountID, email string) error
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Codes of the problems that stop a campaign's email being sent.
const (
	NoEmail          = "no_email"
	MissingFrom      = "missing_from"
	UnverifiedFrom   = "unverified_from"
	MissingPostal    = "missing_postal_address"
	MissingSubject   = "missing_subject"
	EmptyEmail       = "empty_email"
	MergeTagProblems = "merge_tag"
	InvalidHTML      = "invalid_html"
)

// Problem is one thing to fix before the email can be sent, with how.
// Details are extra facts some problems carry (credits_short: needed,
// balance, short).
type Problem struct {
	Code    string
	Message string
	Hint    string
	Details map[string]any
}

// NotReadyError lists everything that stops the email being sent.
type NotReadyError struct {
	Problems []Problem
}

func (e *NotReadyError) Error() string {
	msgs := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		msgs[i] = p.Message
	}
	return strings.Join(msgs, " ")
}

// Prepared is a campaign ready to send: the email compiled, and who it's
// from.
type Prepared struct {
	Campaign *campaigns.Campaign
	Email    *emails.Email
	From     mail.Address
	ReplyTo  []mail.Address
	// PostalAddress is the account's, which the footer shows.
	PostalAddress string
	Template      *render.Template
}

// Prepare gets a campaign ready to send, or returns a *NotReadyError with
// every problem found, or the campaigns package's ErrNotFound.
func (s *Service) Prepare(ctx context.Context, accountID, campaignID string) (*Prepared, error) {
	c, err := s.Campaigns.Get(ctx, accountID, campaignID)
	if err != nil {
		return nil, err
	}
	if c.EmailID == nil {
		return nil, &NotReadyError{Problems: []Problem{{Code: NoEmail,
			Message: "This campaign has no email; it was deleted.",
			Hint:    "Pick one with `mailgoose campaign update " + c.ID + " --email <email-id>`."}}}
	}
	e, err := s.Emails.Get(ctx, accountID, *c.EmailID)
	if errors.Is(err, emails.ErrNotFound) {
		return nil, &NotReadyError{Problems: []Problem{{Code: NoEmail, Message: "This campaign's email no longer exists.",
			Hint: "Pick one with `mailgoose campaign update " + c.ID + " --email <email-id>`."}}}
	}
	if err != nil {
		return nil, err
	}
	st, err := s.Settings.AccountSettings(ctx, accountID)
	if err != nil {
		return nil, err
	}

	p := &Prepared{Campaign: c, Email: e}
	var problems []Problem
	doc := e.Document

	// The email's own From wins over the account's default, field by field.
	p.From.Email = trimmed(st.FromEmail)
	if f := trimmed(doc.Settings.FromEmail); f != "" {
		p.From.Email = f
	}
	p.From.Name = trimmed(st.FromName)
	if n := strings.TrimSpace(doc.Settings.FromName); n != "" {
		p.From.Name = n
	}
	if r := trimmed(doc.Settings.ReplyTo); r != "" {
		p.ReplyTo = []mail.Address{{Email: r}}
	}
	if p.From.Email == "" {
		problems = append(problems, Problem{Code: MissingFrom, Message: "There's no From address.",
			Hint: "Set one for the account with `mailgoose account update --from-email you@yourdomain.com`, or on the email."})
	} else {
		err := s.Domains.RequireVerifiedFrom(ctx, accountID, p.From.Email)
		switch {
		case errors.Is(err, domains.ErrFromNotVerified):
			problems = append(problems, Problem{Code: UnverifiedFrom,
				Message: "The From address " + p.From.Email + " isn't on a verified sending domain of this account.",
				Hint:    "Verify its domain with `mailgoose domain add " + domainOf(p.From.Email) + "` and the DNS records it gives, or use a From on a verified domain (`mailgoose domain list`)."})
		case err != nil:
			return nil, err
		}
	}
	postal := trimmed(st.PostalAddress)
	if postal == "" {
		problems = append(problems, Problem{Code: MissingPostal, Message: "The account has no postal address, which every campaign email must show.",
			Hint: "Add it with `mailgoose account update --postal-address \"...\"`."})
	}
	if strings.TrimSpace(doc.Settings.Subject) == "" {
		problems = append(problems, Problem{Code: MissingSubject, Message: "The email has no subject.",
			Hint: "Set one with `mailgoose email update " + e.ID + " --subject \"...\"`."})
	}
	if doc.HTML != nil && strings.TrimSpace(*doc.HTML) == "" {
		problems = append(problems, Problem{Code: EmptyEmail, Message: "This html email has no markup yet.",
			Hint: "Add it with `mailgoose email import-html " + e.ID + " <file.html>`."})
	}
	p.PostalAddress = postal
	if len(problems) == 0 {
		acct := render.Account{PostalAddress: postal}
		if doc.HTML != nil {
			p.Template, err = render.CompileHTML(doc, acct)
		} else {
			p.Template, err = render.Compile(doc, acct)
		}
		var tagProblems render.Problems
		var htmlErr *emails.HTMLError
		switch {
		case errors.As(err, &htmlErr):
			problems = append(problems, Problem{Code: InvalidHTML, Message: "The email's markup has errors: " + htmlErr.Error() + ".",
				Hint: "Fix them and import the markup again with `mailgoose email import-html " + e.ID + " <file.html>`."})
		case errors.As(err, &tagProblems):
			problems = append(problems, Problem{Code: MergeTagProblems, Message: "The email has merge tags or links that can't be filled in: " + tagProblems.Error() + ".",
				Hint: "Run `mailgoose email check " + e.ID + "` for the list, and fix them."})
		case err != nil:
			return nil, err
		}
	}
	if len(problems) > 0 {
		return nil, &NotReadyError{Problems: problems}
	}
	return p, nil
}

func trimmed(s *string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(*s)
}

func domainOf(email string) string {
	if at := strings.LastIndexByte(email, '@'); at >= 0 {
		return email[at+1:]
	}
	return email
}
