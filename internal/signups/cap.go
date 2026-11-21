package signups

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gremlin-technologies/mailgoose/internal/contacts"
	"github.com/gremlin-technologies/mailgoose/internal/forms"
	"github.com/gremlin-technologies/mailgoose/internal/mail"
)

// The daily signup cap (MG-92). The per-IP and per-form limits a minute are
// held in memory in each API task, so a botnet spread over many addresses
// can still add tens of thousands of made-up contacts an hour through one
// form. The cap counts in Postgres, so it holds across tasks: a form adds
// at most its cap of new contacts in any CapWindow. A submission counts
// when it adds a contact; one from an address already on the list doesn't,
// since it adds nobody and its confirmation emails are already held to one
// an hour (ResendGap). Past the cap every submission is refused alike, new
// address or not, so the answer doesn't tell who's on the list.
const (
	// DefaultDailyCap is the cap when Service.DailyCap is 0.
	DefaultDailyCap = 1000
	// CapWindow is how far back new signups count towards the cap.
	CapWindow = 24 * time.Hour
)

// CapError means the form has added its cap of new contacts in the last
// CapWindow, so it takes no more for now. Nothing was stored.
type CapError struct {
	Cap int
	// At is when the signup was refused.
	At time.Time
	// RetryAt is when enough of the window's signups will have aged out
	// for the form to take one again.
	RetryAt time.Time
}

// Wait is how long until the form takes a signup again.
func (e *CapError) Wait() time.Duration { return e.RetryAt.Sub(e.At) }

func (e *CapError) Error() string {
	return fmt.Sprintf("the form has taken its %d new signups for the last 24 hours", e.Cap)
}

// dailyCap is the cap, or 0 for none.
func (s *Service) dailyCap() int {
	switch {
	case s.DailyCap == 0:
		return DefaultDailyCap
	case s.DailyCap < 0:
		return 0
	}
	return s.DailyCap
}

// checkCap returns a *CapError when the form has added its cap of new
// contacts in the window up to now. It locks the account row first, the
// lock contacts.SignUp and contacts.Confirm take first too, so two tasks'
// signups for one form are counted one after the other. The count is a
// statement of its own after the lock: under READ COMMITTED a statement's
// snapshot is taken before it waits for a lock, so a count in the locking
// statement wouldn't see a signup committed while it waited.
func (s *Service) checkCap(ctx context.Context, tx pgx.Tx, f *forms.Form, now time.Time) error {
	limit := s.dailyCap()
	if limit == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM accounts WHERE id = $1 FOR UPDATE`, f.AccountID); err != nil {
		return err
	}
	since := now.Add(-CapWindow)
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM form_signups WHERE form_id = $1 AND created_at > $2`,
		f.ID, since).Scan(&n); err != nil {
		return err
	}
	if n < limit {
		return nil
	}
	retry, err := capRetryAt(ctx, tx, f.ID, since, n-limit)
	if err != nil {
		return err
	}
	return &CapError{Cap: limit, At: now, RetryAt: retry}
}

// capRetryAt is when the form can take a signup again: once the signup
// skip places after the oldest in the window ages out of it, the count is
// under the cap.
func capRetryAt(ctx context.Context, q contacts.RowQuerier, formID string, since time.Time, skip int) (time.Time, error) {
	var at time.Time
	err := q.QueryRow(ctx, `
		SELECT created_at FROM form_signups WHERE form_id = $1 AND created_at > $2
		ORDER BY created_at OFFSET $3 LIMIT 1`, formID, since, skip).Scan(&at)
	if err != nil {
		return time.Time{}, err
	}
	return at.Add(CapWindow).UTC(), nil
}

// countSignup counts a submission that added a contact towards the
// form's cap, in the signup's transaction, and prunes the form's rows that
// have left the window.
func (s *Service) countSignup(ctx context.Context, tx pgx.Tx, formID string, now time.Time) error {
	if s.dailyCap() == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `INSERT INTO form_signups (form_id, created_at) VALUES ($1, $2)`, formID, now); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `DELETE FROM form_signups WHERE form_id = $1 AND created_at <= $2`, formID, now.Add(-CapWindow))
	return err
}

// FormCapped is a form that reached its daily cap, for the notices.
type FormCapped struct {
	AccountID   string
	AccountName string
	// OwnerEmail is who's told; empty if the account has no user.
	OwnerEmail  string
	FormID      string
	FormName    string
	DoubleOptIn bool
	Cap         int
	At          time.Time
	RetryAt     time.Time
}

// CapNotices tells a form's owner, and mailgoose's operators, that the form
// reached its daily cap (MG-92).
type CapNotices struct {
	Mail mail.Sender
	// From is mailgoose's own notices address, on the system sending
	// domain.
	From mail.Address
	// Alert emails the operators (sending.OpsAlert.Notify); nil sends no
	// alert.
	Alert func(ctx context.Context, subject, text string)
	Log   *slog.Logger
}

func (n *CapNotices) log() *slog.Logger {
	if n == nil || n.Log == nil {
		return slog.Default()
	}
	return n.Log
}

// capReached tells the owner and the operators that f reached its cap,
// once in CapWindow per form: the form's row is stamped first, so when two
// tasks refuse a signup at once only one sends. It never fails: what can't
// be done is logged.
func (s *Service) capReached(ctx context.Context, f *forms.Form, ce *CapError, now time.Time) {
	ctx = context.WithoutCancel(ctx)
	c := FormCapped{AccountID: f.AccountID, FormID: f.ID, Cap: ce.Cap, At: now, RetryAt: ce.RetryAt}
	err := s.DB.QueryRow(ctx, `
		UPDATE forms k SET signup_cap_noticed_at = $2
		FROM accounts a
		WHERE k.id = $1 AND a.id = k.account_id
			AND (k.signup_cap_noticed_at IS NULL OR k.signup_cap_noticed_at <= $3)
		RETURNING k.name, k.double_opt_in, a.name,
			coalesce((SELECT u.email FROM users u WHERE u.account_id = a.id ORDER BY u.created_at LIMIT 1), '')`,
		f.ID, now, now.Add(-CapWindow)).Scan(&c.FormName, &c.DoubleOptIn, &c.AccountName, &c.OwnerEmail)
	if errors.Is(err, pgx.ErrNoRows) {
		return
	}
	n := s.CapNotices
	if err != nil {
		n.log().ErrorContext(ctx, "stamp a form's signup cap notice", "err", err, "form_id", f.ID)
		return
	}
	n.log().WarnContext(ctx, "form reached its daily signup cap", "account_id", c.AccountID, "form_id", c.FormID,
		"cap", c.Cap, "retry_at", c.RetryAt)
	if n == nil {
		return
	}
	n.formCapped(ctx, c)
}

func (n *CapNotices) formCapped(ctx context.Context, c FormCapped) {
	retry := c.RetryAt.UTC().Format("Jan 2, 2006 at 15:04 UTC")
	if n.Alert != nil {
		doi := "off"
		if c.DoubleOptIn {
			doi = "on"
		}
		n.Alert(ctx, fmt.Sprintf("%s's form %q reached its daily signup cap", c.AccountName, c.FormName),
			fmt.Sprintf(`FORM SIGNUP CAP: form %q (%s) of %s (account %s) added %s new contacts in 24 hours and refuses signups until %s.

Double opt-in is %s. That many signups in a day often means bots found the form.

Look into it with:
  mailgoose admin account %s
Pause it with:
  mailgoose admin pause %s --reason "..."
`, c.FormName, c.FormID, c.AccountName, c.AccountID, thousands(c.Cap), c.RetryAt.UTC().Format(time.RFC3339),
				doi, c.AccountID, c.AccountID))
	}
	if c.OwnerEmail == "" || n.Mail == nil {
		return
	}
	doi := fmt.Sprintf("- Turn on double opt-in, so only people who confirm by email are subscribed: `mailgoose form update %s --double-opt-in`.\n", c.FormID)
	if c.DoubleOptIn {
		doi = "- The form uses double opt-in, so addresses that never confirm aren't sent your campaigns.\n"
	}
	text := fmt.Sprintf(`Your signup form %q has added %s new contacts in the last 24 hours, the most a form adds in a day, so it's turning signups away for now. It starts taking them again on its own from %s.

That many signups in a day often means bots found the form and are adding made-up addresses. Those bounce when you send to them, and too many bounces pause your sending. Before your next campaign:
- Look through the contacts the form added today, and delete any you don't recognise.
%s- Make sure the form is only on pages you expect.

Expecting this many signups? Reply to this email.
`, c.FormName, thousands(c.Cap), retry, doi)
	_, err := n.Mail.Send(ctx, mail.Message{
		From:      n.From,
		To:        mail.Address{Email: c.OwnerEmail},
		Subject:   fmt.Sprintf("Your form %q is turning signups away for today", c.FormName),
		Text:      text,
		HTML:      "<!doctype html><html><body><pre style=\"white-space:pre-wrap;font-family:inherit\">" + html.EscapeString(text) + "</pre></body></html>",
		Kind:      mail.KindSystem,
		AccountID: c.AccountID,
	})
	if err != nil {
		n.log().ErrorContext(ctx, "send the signup cap notice", "err", err, "account_id", c.AccountID, "form_id", c.FormID)
	}
}

// capProblem is the problem a form at its cap shows, or nil.
func (s *Service) capProblem(ctx context.Context, f *forms.Form) (*Problem, error) {
	limit := s.dailyCap()
	if limit == 0 {
		return nil, nil
	}
	since := s.now().Add(-CapWindow)
	var n int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM form_signups WHERE form_id = $1 AND created_at > $2`,
		f.ID, since).Scan(&n); err != nil {
		return nil, err
	}
	if n < limit {
		return nil, nil
	}
	retry, err := capRetryAt(ctx, s.DB, f.ID, since, n-limit)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	hint := "That many usually means bots found the form: delete the contacts you don't recognise"
	if !f.DoubleOptIn {
		hint += fmt.Sprintf(", and turn on double opt-in with `mailgoose form update %s --double-opt-in`", f.ID)
	}
	return &Problem{Code: "signup_cap_reached",
		Message: fmt.Sprintf("Signups are refused for now: the form added %s new contacts in the last 24 hours, the most a form adds in a day. It takes them again from %s.",
			thousands(limit), retry.Format("Jan 2, 2006 15:04 UTC")),
		Hint: hint + "."}, nil
}

// thousands writes n with thousands separators: 1,000.
func thousands(n int) string {
	if n < 0 {
		return "-" + thousands(-n)
	}
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
