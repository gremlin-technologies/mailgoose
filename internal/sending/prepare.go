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

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/auth"
	"github.com/gremlin-technologies/mailgoose/internal/campaigns"
	"github.com/gremlin-technologies/mailgoose/internal/contacts"
	"github.com/gremlin-technologies/mailgoose/internal/domains"
	"github.com/gremlin-technologies/mailgoose/internal/emails"
	"github.com/gremlin-technologies/mailgoose/internal/linkrisk"
	"github.com/gremlin-technologies/mailgoose/internal/mail"
	"github.com/gremlin-technologies/mailgoose/internal/render"
	"github.com/gremlin-technologies/mailgoose/internal/senders"
	"github.com/gremlin-technologies/mailgoose/internal/unsubscribe"
)

// Service prepares and sends a campaign's email.
type Service struct {
	DB        *pgxpool.Pool
	Campaigns *campaigns.Service
	Emails    *emails.Service
	Settings  *auth.Service
	Domains   VerifiedFrom
	// Senders works out who an email goes from (MG-85): its own From on a
	// verified domain, or the shared sending domain for a verified sender.
	// Nil means a Resolver on Domains with shared sending off.
	Senders     *senders.Resolver
	Unsubscribe *unsubscribe.Service
	Mail        mail.Sender
	// AssetsURL is where the API is reached (PUBLIC_BASE_URL), which serves
	// the social block's icons (MG-109); empty renders social links as text.
	AssetsURL string
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

// senders is s.Senders, or a Resolver with shared sending off.
func (s *Service) senders() *senders.Resolver {
	if s.Senders != nil {
		return s.Senders
	}
	return &senders.Resolver{DB: s.DB, Domains: s.Domains}
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Codes of the problems that stop a campaign's email being sent.
const (
	NoEmail        = "no_email"
	MissingFrom    = "missing_from"
	UnverifiedFrom = "unverified_from"
	// UnverifiedReplyTo is an email's Reply-To that the shared sending
	// domain can't carry (MG-85).
	UnverifiedReplyTo = "unverified_reply_to"
	MissingPostal     = "missing_postal_address"
	MissingSubject    = "missing_subject"
	EmptyEmail        = "empty_email"
	MergeTagProblems  = "merge_tag"
	InvalidHTML       = "invalid_html"
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

// Branded says whether the account's copies carry the "Sent by mailgoose"
// footer (MG-119): a free account's do, and so do a tester's. A paid
// account's don't. It's read when each copy is rendered, so upgrading ends
// the footer from the next copy and a plan that ends brings it back.
func Branded(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, accountID string) (bool, error) {
	var branded bool
	err := q.QueryRow(ctx, `SELECT plan = 'free' OR tester_started_at IS NOT NULL FROM accounts WHERE id = $1`, accountID).Scan(&branded)
	return branded, err
}

// Prepared is a campaign ready to send: the email compiled, and who it's
// from.
type Prepared struct {
	Campaign *campaigns.Campaign
	Email    *emails.Email
	From     mail.Address
	ReplyTo  []mail.Address
	// Shared is true when the email goes from the shared sending domain
	// (MG-85), and Sender is then the verified address the email asked to
	// go from, which replies go to unless it names its own Reply-To.
	Shared bool
	Sender string
	// DefaultFrom is true when neither the email nor the account set a
	// From, so it goes as the owner's sign-in address (Sender) by default
	// (MG-108).
	DefaultFrom bool
	// PostalAddress is the account's, which the footer shows.
	PostalAddress string
	// FieldKeys are the account's contact field keys, each a merge tag
	// (MG-133).
	FieldKeys []string
	Template  *render.Template
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
	replyTo := trimmed(doc.Settings.ReplyTo)
	// A missing From defaults to the owner's sign-in address where shared
	// sending is on and the account has no verified domain (MG-108); the
	// Resolver says so, and otherwise refuses it with ErrNoFrom.
	r := s.senders()
	sender, err := r.Resolve(ctx, accountID, p.From, replyTo)
	var badReplyTo *senders.ReplyToError
	switch {
	case errors.Is(err, senders.ErrNoFrom):
		problems = append(problems, Problem{Code: MissingFrom, Message: "There's no From address.",
			Hint: "Set one for the account with `mailgoose account update --from-email you@yourdomain.com`, or on the email."})
	case errors.Is(err, domains.ErrFromNotVerified):
		problems = append(problems, Problem{Code: UnverifiedFrom,
			Message: r.UnverifiedMessage(p.From.Email), Hint: r.UnverifiedHint(p.From.Email),
			Details: r.UnverifiedDetails(p.From.Email)})
	case errors.As(err, &badReplyTo):
		problems = append(problems, Problem{Code: UnverifiedReplyTo,
			Message: "The Reply-To " + badReplyTo.Address + " isn't one of this account's verified sender addresses or on a verified domain, so emails from mailgoose's shared domain can't send replies there.",
			Hint:    senders.ReplyToHint(badReplyTo.Address)})
	case err != nil:
		return nil, err
	default:
		p.From, p.ReplyTo, p.Shared, p.DefaultFrom = sender.From, sender.ReplyTo, sender.Shared, sender.Default
		if sender.Shared {
			p.Sender = sender.Address
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
	if p.FieldKeys, err = contacts.FieldKeys(ctx, s.DB, accountID); err != nil {
		return nil, err
	}
	if len(problems) == 0 {
		acct := render.Account{PostalAddress: postal, AssetsURL: s.AssetsURL, FieldKeys: p.FieldKeys}
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
