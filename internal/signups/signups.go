// Package signups takes signup form submissions (FRM-3 to FRM-6): it turns
// away bots, adds or finds the contact, keeps a consent record (CMP-3),
// sends the double opt-in email and confirms it.
package signups

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/auth"
	"github.com/gremlin-technologies/mailgoose/internal/contacts"
	"github.com/gremlin-technologies/mailgoose/internal/domains"
	"github.com/gremlin-technologies/mailgoose/internal/forms"
	"github.com/gremlin-technologies/mailgoose/internal/mail"
	"github.com/gremlin-technologies/mailgoose/internal/senders"
	"github.com/gremlin-technologies/mailgoose/internal/testmode"
)

const (
	// MinElapsed is the least time a person takes to fill in a form. The
	// embed code's script reports how long the page was open; a post sooner
	// than this is a bot's, and is dropped.
	MinElapsed = 2 * time.Second
	// ConfirmTTL is how long a confirm link works.
	ConfirmTTL = 7 * 24 * time.Hour
	// ResendGap is the least time between two confirmation emails to one
	// contact, so a form can't be used to flood someone's inbox.
	ResendGap = time.Hour
	// ConfirmPrefix starts the path of a confirm link: ConfirmPrefix+token.
	ConfirmPrefix = "/confirm/"
	// tokenPrefix starts every confirm token, so its format can change.
	tokenPrefix = "c1"
)

var (
	// ErrFormNotFound means no form with that id, or one of an account that
	// isn't active.
	ErrFormNotFound = errors.New("form not found")
	// ErrBadStamp means a post without the form's own signed stamp: it
	// didn't come from the form's embed code.
	ErrBadStamp = errors.New("the form's code is incomplete")
	// ErrNotReady means a double opt-in form whose account has no From on a
	// verified sending domain, so no confirmation email can go.
	ErrNotReady = errors.New("the form can't send confirmation emails yet")
	// ErrFormClosed means a form whose account can't take signups now: it's
	// suspended (MG-65), or waiting for its sign-up review (MG-66) with full
	// sending.
	ErrFormClosed = errors.New("the form isn't taking signups")
	// ErrTestMode means a signup from an address that isn't a confirmed
	// test address (or the owner's) of an account in test mode (MG-80).
	// Nothing is stored and no confirmation email goes.
	ErrTestMode = errors.New("the form is in test mode")
	// ErrBadToken means a confirm link that isn't one, or was cut short.
	ErrBadToken = errors.New("confirm link not valid")
	// ErrNotFound means a confirm link whose signup is gone: the contact
	// was deleted.
	ErrNotFound = errors.New("signup not found")
	// ErrExpired means a confirm link older than ConfirmTTL.
	ErrExpired = errors.New("confirm link expired")
)

// InvalidError is a submission with a field that's wrong or missing.
type InvalidError struct {
	Field   string
	Message string
}

func (e *InvalidError) Error() string { return e.Field + ": " + e.Message }

// SendError means the confirmation email couldn't be sent. The signup is
// kept; posting again sends it.
type SendError struct{ Err error }

func (e *SendError) Error() string { return "send the confirmation email: " + e.Err.Error() }
func (e *SendError) Unwrap() error { return e.Err }

// VerifiedFrom is what signups need from the domains service.
type VerifiedFrom interface {
	RequireVerifiedFrom(ctx context.Context, accountID, email string) error
}

// Service takes signups.
type Service struct {
	DB       *pgxpool.Pool
	Forms    *forms.Service
	Settings *auth.Service
	Domains  VerifiedFrom
	// Senders works out who confirmation emails go from (MG-85): the
	// account's From on a verified domain, or the shared sending domain for
	// a verified sender. Nil means a Resolver on Domains with shared sending
	// off.
	Senders *senders.Resolver
	Mail    mail.Sender
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

func (s *Service) senders() *senders.Resolver {
	if s.Senders != nil {
		return s.Senders
	}
	return &senders.Resolver{DB: s.DB, Domains: s.Domains}
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// Submission is one post to a form's public endpoint.
type Submission struct {
	Email     string
	FirstName string
	LastName  string
	// Honeypot is the hidden field only bots fill in.
	Honeypot string
	// Stamp is the embed code's signed stamp.
	Stamp string
	// Elapsed is how long the page was open, when the embed script said;
	// nil without the script.
	Elapsed *time.Duration
	// IP is the poster's address, for the consent record.
	IP string
}

// Result is what a submission came to. The person sees the same answer
// whatever happened to their address, so a form can't be used to find out
// who's on a list.
type Result struct {
	Form *forms.Form
	// Dropped is true for a bot's post: nothing was done.
	Dropped bool
	// ConfirmSent is true when a confirmation email went out.
	ConfirmSent bool
}

// Submit takes a post to a form. baseURL is the app's public URL, for the
// confirm link.
func (s *Service) Submit(ctx context.Context, formID string, sub Submission, baseURL string) (*Result, error) {
	f, err := s.Forms.Public(ctx, formID)
	if errors.Is(err, forms.ErrNotFound) {
		return nil, ErrFormNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := s.accepting(ctx, f.AccountID); err != nil {
		return nil, err
	}
	res := &Result{Form: f}
	// Bots are answered as if they'd signed up, so they learn nothing.
	if strings.TrimSpace(sub.Honeypot) != "" || (sub.Elapsed != nil && *sub.Elapsed < MinElapsed) {
		res.Dropped = true
		return res, nil
	}
	if _, err := forms.CheckStamp(f.SigningKey, f.ID, strings.TrimSpace(sub.Stamp)); err != nil {
		return nil, ErrBadStamp
	}
	in, err := fieldsOf(f, sub)
	if err != nil {
		return nil, err
	}
	// In test mode only test addresses sign up, so no confirmation email
	// goes to anyone else (MG-80).
	ok, err := testmode.Allows(ctx, s.DB, f.AccountID, in.Email)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrTestMode
	}

	// A double opt-in form can't take signups it can't confirm. A form
	// without it still sends a confirmation to an address that unsubscribed
	// before, when it can.
	from, err := s.from(ctx, f.AccountID)
	if err != nil {
		return nil, err
	}
	if f.DoubleOptIn && from == nil {
		return nil, ErrNotReady
	}

	now := s.now()
	in.Now = now
	var token string
	var su *contacts.SignUpResult
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		if su, err = contacts.SignUp(ctx, tx, f.AccountID, in); err != nil {
			return err
		}
		var tokenHash []byte
		var expires *time.Time
		if su.NeedsConfirming && from != nil {
			var last *time.Time
			if err := tx.QueryRow(ctx, `SELECT max(confirm_sent_at) FROM consents WHERE contact_id = $1`,
				su.ContactID).Scan(&last); err != nil {
				return err
			}
			if last == nil || now.Sub(*last) >= ResendGap {
				if token, err = newToken(); err != nil {
					return err
				}
				tokenHash = hashToken(token)
				e := now.Add(ConfirmTTL)
				expires = &e
			}
		}
		wording, err := json.Marshal(wordingOf(f))
		if err != nil {
			return err
		}
		// The send is stamped here, before it goes out, so a second post
		// for the address (it waits on the contact's row lock) sees it and
		// sends nothing. A send that fails clears the stamp.
		var sentAt *time.Time
		if tokenHash != nil {
			sentAt = &now
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO consents (account_id, contact_id, form_id, ip, wording, created_at, confirm_token_hash, confirm_expires_at, confirm_sent_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			f.AccountID, su.ContactID, f.ID, sub.IP, wording, now, tokenHash, expires, sentAt); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE forms SET signup_count = signup_count + 1 WHERE id = $1`, f.ID)
		return err
	})
	if err != nil {
		return nil, err
	}
	if token == "" {
		return res, nil
	}

	msg := confirmMessage(f, from, in.Email, strings.TrimRight(baseURL, "/")+ConfirmPrefix+token)
	if _, err := s.Mail.Send(ctx, msg); err != nil {
		// Not sent, so a retry may send at once. The link stays valid, but
		// nobody has it.
		if _, cerr := s.DB.Exec(ctx, `UPDATE consents SET confirm_sent_at = NULL WHERE confirm_token_hash = $1`,
			hashToken(token)); cerr != nil {
			err = errors.Join(err, cerr)
		}
		return nil, &SendError{Err: err}
	}
	res.ConfirmSent = true
	return res, nil
}

// fieldsOf checks a submission's fields against the form's.
func fieldsOf(f *forms.Form, sub Submission) (contacts.SignUpInput, error) {
	var in contacts.SignUpInput
	email, err := contacts.NormalizeEmail(sub.Email)
	if err != nil {
		if strings.TrimSpace(sub.Email) == "" {
			return in, &InvalidError{Field: forms.FieldEmail, Message: "Please enter your email address."}
		}
		return in, &InvalidError{Field: forms.FieldEmail, Message: "That doesn't look like an email address. Please check it."}
	}
	in.Email = email
	for _, fl := range f.Fields {
		var v string
		switch fl.Name {
		case forms.FieldFirstName:
			v = strings.TrimSpace(sub.FirstName)
			if v != "" {
				in.FirstName = &v
			}
		case forms.FieldLastName:
			v = strings.TrimSpace(sub.LastName)
			if v != "" {
				in.LastName = &v
			}
		default:
			continue
		}
		if fl.Required && v == "" {
			return in, &InvalidError{Field: fl.Name, Message: "Please fill in " + strings.ToLower(fl.Label) + "."}
		}
		if len([]rune(v)) > contacts.MaxNameLen || strings.ContainsAny(v, "\r\n") {
			return in, &InvalidError{Field: fl.Name, Message: fmt.Sprintf("%s can be at most %d characters.", fl.Label, contacts.MaxNameLen)}
		}
	}
	in.DoubleOptIn = f.DoubleOptIn
	for _, t := range f.Tags {
		in.TagIDs = append(in.TagIDs, t.ID)
	}
	return in, nil
}

// wording is what the form showed, kept with each consent record.
type wording struct {
	FormName       string   `json:"form_name"`
	Fields         []string `json:"fields"`
	ButtonText     string   `json:"button_text"`
	DoubleOptIn    bool     `json:"double_opt_in"`
	ConfirmSubject string   `json:"confirm_subject,omitempty"`
	ConfirmText    string   `json:"confirm_text,omitempty"`
}

func wordingOf(f *forms.Form) wording {
	w := wording{FormName: f.Name, ButtonText: f.ButtonText, DoubleOptIn: f.DoubleOptIn}
	for _, fl := range f.Fields {
		w.Fields = append(w.Fields, fl.Label)
	}
	if f.DoubleOptIn {
		w.ConfirmSubject, w.ConfirmText = f.ConfirmSubject, f.ConfirmText
	}
	return w
}

// from is who the account's confirmation emails go from: its From on a
// verified sending domain, or, with shared sending on, the shared domain
// for a From that's a verified sender (MG-85). Nil when it has neither.
func (s *Service) from(ctx context.Context, accountID string) (*senders.Sender, error) {
	st, err := s.Settings.AccountSettings(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if st.FromEmail == nil || strings.TrimSpace(*st.FromEmail) == "" {
		return nil, nil
	}
	addr := mail.Address{Email: strings.TrimSpace(*st.FromEmail), Name: st.Name}
	if st.FromName != nil && strings.TrimSpace(*st.FromName) != "" {
		addr.Name = strings.TrimSpace(*st.FromName)
	}
	sender, err := s.senders().Resolve(ctx, accountID, addr, "")
	if errors.Is(err, domains.ErrFromNotVerified) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return sender, nil
}

// Problem is something that stops a form taking signups.
type Problem struct {
	Code    string
	Message string
	Hint    string
}

// Problems lists what stops a form taking signups; none when it can.
func (s *Service) Problems(ctx context.Context, f *forms.Form) ([]Problem, error) {
	if !f.DoubleOptIn {
		return nil, nil
	}
	from, err := s.from(ctx, f.AccountID)
	if err != nil || from != nil {
		return nil, err
	}
	if s.senders().SharedOn() {
		return []Problem{{Code: "no_verified_from",
			Message: "Signups are refused: the form uses double opt-in, and the account's From address is neither on a verified domain nor a verified sender address, so the confirmation email can't be sent.",
			Hint:    "Set the From to your own address with `mailgoose account update --from-email you@example.com` and verify it with `mailgoose sender-addresses add` (your sign-in address needs no link); or, better for deliverability, verify its domain with `mailgoose domain add <domain>`."}}, nil
	}
	return []Problem{{Code: "no_verified_from",
		Message: "Signups are refused: the form uses double opt-in, and the account has no From address on a verified sending domain to send the confirmation email from.",
		Hint:    "Verify a domain with `mailgoose domain add <domain>` and set the From with `mailgoose account update --from-email you@<domain>`."}}, nil
}

var confirmHTML = template.Must(template.New("confirm").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>{{.Subject}}</title></head>
<body style="margin:0;padding:24px;background:#faf5ef;font:16px/1.5 -apple-system,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;color:#473e3b">
<div style="max-width:520px;margin:0 auto;background:#ffffff;border:1px solid #ebddd2;border-radius:12px;padding:28px">
{{range .Paragraphs}}<p style="margin:0 0 16px">{{.}}</p>
{{end}}<p style="margin:24px 0"><a href="{{.URL}}" style="display:inline-block;background:#5562a3;color:#ffffff;text-decoration:none;font-weight:600;padding:12px 20px;border-radius:8px">Confirm my subscription</a></p>
<p style="margin:0;color:#6b5f5a;font-size:14px">Or open this link: {{.URL}}</p>
</div>
</body></html>
`))

// confirmMessage is the double opt-in email for a signup to f.
func confirmMessage(f *forms.Form, from *senders.Sender, to, url string) mail.Message {
	var paras []string
	for _, p := range strings.Split(strings.ReplaceAll(f.ConfirmText, "\r\n", "\n"), "\n") {
		if p = strings.TrimSpace(p); p != "" {
			paras = append(paras, p)
		}
	}
	var b strings.Builder
	_ = confirmHTML.Execute(&b, map[string]any{"Subject": f.ConfirmSubject, "Paragraphs": paras, "URL": url})
	text := strings.Join(paras, "\n\n") + "\n\nConfirm your subscription:\n" + url + "\n"
	return mail.Message{
		From:      from.From,
		ReplyTo:   from.ReplyTo,
		To:        mail.Address{Email: to},
		Subject:   f.ConfirmSubject,
		HTML:      b.String(),
		Text:      text,
		Kind:      mail.KindConfirm,
		AccountID: f.AccountID,
	}
}

// Pending is a signup a confirm link is for.
type Pending struct {
	AccountName string
	Email       string
	// Confirmed is true once the link has been used.
	Confirmed bool
}

// Lookup returns the signup a confirm link is for, without confirming it:
// mail scanners open links, so only the page's button confirms.
func (s *Service) Lookup(ctx context.Context, token string) (*Pending, error) {
	var p *Pending
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		p, _, err = s.find(ctx, tx, token, false)
		return err
	})
	return p, err
}

// Confirm uses a confirm link: the contact becomes subscribed, the consent
// record notes when, and the form counts it. Using it again changes nothing.
func (s *Service) Confirm(ctx context.Context, token string) (*Pending, error) {
	var p *Pending
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var c *consent
		var err error
		p, c, err = s.find(ctx, tx, token, true)
		if err != nil || p.Confirmed {
			return err
		}
		now := s.now()
		status, err := contacts.Confirm(ctx, tx, c.accountID, c.contactID, now)
		if err != nil {
			return err
		}
		// The consent records the click whatever happens. The form counts
		// it only when the contact ends up subscribed: a bounced or
		// complained address stays as it is.
		if _, err := tx.Exec(ctx, `UPDATE consents SET confirmed_at = $2 WHERE id = $1`, c.id, now); err != nil {
			return err
		}
		if c.formID != nil && status == contacts.StatusSubscribed {
			if _, err := tx.Exec(ctx, `UPDATE forms SET confirm_count = confirm_count + 1 WHERE id = $1`, *c.formID); err != nil {
				return err
			}
		}
		p.Confirmed = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	return p, nil
}

type consent struct {
	id, accountID, contactID string
	formID                   *string
}

func (s *Service) find(ctx context.Context, tx pgx.Tx, token string, lock bool) (*Pending, *consent, error) {
	if !strings.HasPrefix(token, tokenPrefix) || len(token) != len(tokenPrefix)+43 {
		return nil, nil, ErrBadToken
	}
	q := `SELECT k.id, k.account_id, k.contact_id, k.form_id, k.confirm_expires_at, k.confirmed_at, c.email, a.name
		FROM consents k JOIN contacts c ON c.id = k.contact_id JOIN accounts a ON a.id = k.account_id
		WHERE k.confirm_token_hash = $1 AND a.status = 'active'`
	if lock {
		q += ` FOR UPDATE OF k`
	}
	var c consent
	var p Pending
	var expires, confirmed *time.Time
	err := tx.QueryRow(ctx, q, hashToken(token)).Scan(&c.id, &c.accountID, &c.contactID, &c.formID, &expires, &confirmed,
		&p.Email, &p.AccountName)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	p.Confirmed = confirmed != nil
	if !p.Confirmed && expires != nil && !s.now().Before(*expires) {
		return nil, nil, ErrExpired
	}
	return &p, &c, nil
}

func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return tokenPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte("mailgoose-confirm\x00" + token))
	return sum[:]
}

func (s *Service) inTx(ctx context.Context, f func(pgx.Tx) error) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := f(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// accepting returns ErrFormClosed when the account can't take signups now.
// An account in test mode that's waiting for review takes them, since only
// its test addresses can sign up (MG-80).
func (s *Service) accepting(ctx context.Context, accountID string) error {
	var closed bool
	if err := s.DB.QueryRow(ctx, `
		SELECT suspended_at IS NOT NULL OR (coalesce(review = 'waiting', false) AND sending_mode <> 'test')
		FROM accounts WHERE id = $1`,
		accountID).Scan(&closed); err != nil {
		return err
	}
	if closed {
		return ErrFormClosed
	}
	return nil
}
