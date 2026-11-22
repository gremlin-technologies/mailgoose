// Package forms keeps an account's signup forms (FRM-1) and makes their
// embed code (FRM-2). Taking submissions is package signups'.
package forms

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The fields a form can ask for. Email is always one, and required.
const (
	FieldEmail     = "email"
	FieldFirstName = "first_name"
	FieldLastName  = "last_name"
)

// Limits on what a form holds.
const (
	MaxNameChars           = 200
	MaxLabelChars          = 100
	MaxButtonChars         = 50
	MaxSuccessChars        = 500
	MaxRedirectChars       = 2000
	MaxConfirmSubjectChars = 200
	MaxConfirmTextChars    = 2000
	MaxTags                = 20
)

// Defaults for what a new form leaves out.
const (
	DefaultButtonText     = "Subscribe"
	DefaultSuccessConfirm = "Thanks for signing up! Check your inbox for an email to confirm it."
	DefaultSuccess        = "Thanks for signing up!"
	DefaultConfirmSubject = "Please confirm your subscription"
	DefaultConfirmText    = "Thanks for signing up. Please confirm you'd like to get our emails with the link below. " +
		"If you didn't sign up, you can ignore this email and you won't hear from us."
)

// DefaultLabels are the labels fields get when none is given.
var DefaultLabels = map[string]string{FieldEmail: "Email", FieldFirstName: "First name", FieldLastName: "Last name"}

var (
	// ErrNotFound means no form with that id in the account.
	ErrNotFound = errors.New("form not found")
	// ErrUnknownTag means a tag id that isn't one of the account's tags.
	ErrUnknownTag = errors.New("unknown tag")
)

// InvalidError is a form that breaks a rule; Field names which.
type InvalidError struct {
	Field   string
	Message string
}

func (e *InvalidError) Error() string { return e.Field + ": " + e.Message }

func invalid(field, format string, args ...any) *InvalidError {
	return &InvalidError{Field: field, Message: fmt.Sprintf(format, args...)}
}

// Field is one input on a form.
type Field struct {
	Name     string `json:"name"`
	Label    string `json:"label"`
	Required bool   `json:"required"`
}

// Tag is a tag a form puts on everyone who signs up with it.
type Tag struct {
	ID   string
	Name string
}

// Form is one signup form.
type Form struct {
	ID             string
	AccountID      string
	Name           string
	Fields         []Field
	Tags           []Tag
	ButtonText     string
	SuccessMessage string
	// RedirectURL, when set, is where people go after signing up, instead
	// of seeing SuccessMessage.
	RedirectURL    *string
	DoubleOptIn    bool
	ConfirmSubject string
	ConfirmText    string
	SigningKey     []byte
	// Signups counts the submissions the form has taken, and
	// Confirmations the confirm links used (FRM-6).
	Signups       int
	Confirmations int
	// Draft is true for a form a Mailchimp import made (MG-76) that hasn't
	// been saved since: it takes no signups. MailchimpAudience is the
	// audience such a form was made for, nil for any other form.
	Draft             bool
	MailchimpAudience *string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Asks reports whether the form has the field.
func (f *Form) Asks(name string) bool {
	for _, fl := range f.Fields {
		if fl.Name == name {
			return true
		}
	}
	return false
}

// Changes are the parts of a form to set; nil leaves a part as it is, or
// takes its default on create. An empty RedirectURL removes it.
type Changes struct {
	Name           *string
	Fields         *[]Field
	TagIDs         *[]string
	ButtonText     *string
	SuccessMessage *string
	RedirectURL    *string
	DoubleOptIn    *bool
	ConfirmSubject *string
	ConfirmText    *string
}

// Cursor is the keyset position of the last form on a page.
type Cursor struct {
	CreatedAt time.Time
	ID        string
}

// Service stores forms.
type Service struct {
	DB *pgxpool.Pool
}

const columns = `id, account_id, name, fields, button_text, success_message, redirect_url, double_opt_in,
	confirm_subject, confirm_text, signing_key, signup_count, confirm_count, draft, mailchimp_audience, created_at, updated_at`

func scan(row pgx.Row) (*Form, error) {
	var f Form
	var fields []byte
	err := row.Scan(&f.ID, &f.AccountID, &f.Name, &fields, &f.ButtonText, &f.SuccessMessage, &f.RedirectURL,
		&f.DoubleOptIn, &f.ConfirmSubject, &f.ConfirmText, &f.SigningKey, &f.Signups, &f.Confirmations, &f.Draft, &f.MailchimpAudience, &f.CreatedAt, &f.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(fields, &f.Fields); err != nil {
		return nil, fmt.Errorf("stored fields of form %s: %w", f.ID, err)
	}
	return &f, nil
}

// apply sets the changes on f and checks the result.
func apply(f *Form, c Changes) error {
	if c.Name != nil {
		f.Name = strings.TrimSpace(*c.Name)
	}
	if f.Name == "" || utf8.RuneCountInString(f.Name) > MaxNameChars {
		return invalid("name", "a name is 1 to %d characters", MaxNameChars)
	}
	if c.Fields != nil {
		fields, err := cleanFields(*c.Fields)
		if err != nil {
			return err
		}
		f.Fields = fields
	}
	if c.ButtonText != nil {
		f.ButtonText = strings.TrimSpace(*c.ButtonText)
	}
	if f.ButtonText == "" || utf8.RuneCountInString(f.ButtonText) > MaxButtonChars || strings.ContainsAny(f.ButtonText, "\r\n") {
		return invalid("button_text", "the button text is one line of 1 to %d characters", MaxButtonChars)
	}
	if c.DoubleOptIn != nil {
		f.DoubleOptIn = *c.DoubleOptIn
	}
	if c.SuccessMessage != nil {
		f.SuccessMessage = strings.TrimSpace(*c.SuccessMessage)
	}
	if f.SuccessMessage == "" || utf8.RuneCountInString(f.SuccessMessage) > MaxSuccessChars {
		return invalid("success_message", "the success message is 1 to %d characters", MaxSuccessChars)
	}
	if c.RedirectURL != nil {
		u := strings.TrimSpace(*c.RedirectURL)
		if u == "" {
			f.RedirectURL = nil
		} else {
			f.RedirectURL = &u
		}
	}
	if f.RedirectURL != nil {
		if err := checkRedirect(*f.RedirectURL); err != nil {
			return err
		}
	}
	if c.ConfirmSubject != nil {
		f.ConfirmSubject = strings.TrimSpace(*c.ConfirmSubject)
	}
	if f.ConfirmSubject == "" || utf8.RuneCountInString(f.ConfirmSubject) > MaxConfirmSubjectChars || strings.ContainsAny(f.ConfirmSubject, "\r\n") {
		return invalid("confirm_subject", "the confirmation subject is one line of 1 to %d characters", MaxConfirmSubjectChars)
	}
	if c.ConfirmText != nil {
		f.ConfirmText = strings.TrimSpace(*c.ConfirmText)
	}
	if f.ConfirmText == "" || utf8.RuneCountInString(f.ConfirmText) > MaxConfirmTextChars {
		return invalid("confirm_text", "the confirmation text is 1 to %d characters", MaxConfirmTextChars)
	}
	return nil
}

// cleanFields checks a field list and fills in default labels.
func cleanFields(in []Field) ([]Field, error) {
	out := make([]Field, 0, len(in))
	seen := map[string]bool{}
	for i, fl := range in {
		path := fmt.Sprintf("fields[%d]", i)
		if _, ok := DefaultLabels[fl.Name]; !ok {
			return nil, invalid(path+".name", "%q isn't a field a form can ask for; use email, first_name or last_name", fl.Name)
		}
		if seen[fl.Name] {
			return nil, invalid(path+".name", "%s is in the list twice", fl.Name)
		}
		seen[fl.Name] = true
		fl.Label = strings.TrimSpace(fl.Label)
		if fl.Label == "" {
			fl.Label = DefaultLabels[fl.Name]
		}
		if utf8.RuneCountInString(fl.Label) > MaxLabelChars || strings.ContainsAny(fl.Label, "\r\n") {
			return nil, invalid(path+".label", "a label is one line of up to %d characters", MaxLabelChars)
		}
		if fl.Name == FieldEmail && !fl.Required {
			return nil, invalid(path+".required", "email is always required")
		}
		out = append(out, fl)
	}
	if !seen[FieldEmail] {
		return nil, invalid("fields", "every form asks for email")
	}
	return out, nil
}

func checkRedirect(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || len(raw) > MaxRedirectChars || strings.ContainsAny(raw, " \t\r\n") {
		return invalid("redirect_url", "the redirect is a full https:// address of up to %d characters", MaxRedirectChars)
	}
	return nil
}

// newForm is a form with every default, before changes.
func newForm(accountID string) *Form {
	return &Form{
		AccountID:      accountID,
		Fields:         []Field{{Name: FieldEmail, Label: DefaultLabels[FieldEmail], Required: true}},
		ButtonText:     DefaultButtonText,
		DoubleOptIn:    true,
		ConfirmSubject: DefaultConfirmSubject,
		ConfirmText:    DefaultConfirmText,
	}
}

// Create adds a form. Parts the changes leave out take their defaults:
// email only, a Subscribe button, double opt-in on.
func (s *Service) Create(ctx context.Context, accountID string, c Changes) (*Form, error) {
	f := newForm(accountID)
	if c.SuccessMessage == nil {
		// The default message fits the double opt-in setting.
		f.SuccessMessage = DefaultSuccessConfirm
		if c.DoubleOptIn != nil && !*c.DoubleOptIn {
			f.SuccessMessage = DefaultSuccess
		}
	}
	if err := apply(f, c); err != nil {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	var id string
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		fields, _ := json.Marshal(f.Fields)
		if err := tx.QueryRow(ctx, `
			INSERT INTO forms (account_id, name, fields, button_text, success_message, redirect_url, double_opt_in,
				confirm_subject, confirm_text, signing_key)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`,
			accountID, f.Name, fields, f.ButtonText, f.SuccessMessage, f.RedirectURL, f.DoubleOptIn,
			f.ConfirmSubject, f.ConfirmText, key).Scan(&id); err != nil {
			return err
		}
		if c.TagIDs != nil {
			return setTags(ctx, tx, accountID, id, *c.TagIDs)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, accountID, id)
}

// Update changes a form. Saving a draft form, even with no changes, makes
// it live.
func (s *Service) Update(ctx context.Context, accountID, id string, c Changes) (*Form, error) {
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		f, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM forms WHERE account_id = $1 AND id = $2 FOR UPDATE`, accountID, id))
		if err != nil {
			return err
		}
		if err := apply(f, c); err != nil {
			return err
		}
		fields, _ := json.Marshal(f.Fields)
		if _, err := tx.Exec(ctx, `
			UPDATE forms SET name = $3, fields = $4, button_text = $5, success_message = $6, redirect_url = $7,
				double_opt_in = $8, confirm_subject = $9, confirm_text = $10, draft = false, updated_at = now()
			WHERE account_id = $1 AND id = $2`,
			accountID, id, f.Name, fields, f.ButtonText, f.SuccessMessage, f.RedirectURL, f.DoubleOptIn,
			f.ConfirmSubject, f.ConfirmText); err != nil {
			return err
		}
		if c.TagIDs != nil {
			return setTags(ctx, tx, accountID, id, *c.TagIDs)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, accountID, id)
}

// setTags replaces a form's tags, refusing any that isn't the account's.
func setTags(ctx context.Context, tx pgx.Tx, accountID, formID string, ids []string) error {
	uniq := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			uniq = append(uniq, id)
		}
	}
	if len(uniq) > MaxTags {
		return invalid("tag_ids", "a form can put up to %d tags on people", MaxTags)
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM tags WHERE account_id = $1 AND id::text = ANY($2)`, accountID, uniq).Scan(&n); err != nil {
		return err
	}
	if n != len(uniq) {
		return ErrUnknownTag
	}
	if _, err := tx.Exec(ctx, `DELETE FROM form_tags WHERE form_id = $1`, formID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO form_tags (form_id, tag_id) SELECT $1, unnest($2::uuid[])`, formID, uniq)
	return err
}

// Get returns one form with its tags.
func (s *Service) Get(ctx context.Context, accountID, id string) (*Form, error) {
	f, err := scan(s.DB.QueryRow(ctx, `SELECT `+columns+` FROM forms WHERE account_id = $1 AND id = $2`, accountID, id))
	if err != nil {
		return nil, err
	}
	if err := s.loadTags(ctx, []*Form{f}); err != nil {
		return nil, err
	}
	return f, nil
}

// Public returns a form by id alone, for its public signup endpoint, with
// its tags. A form of an account that isn't active is ErrNotFound.
func (s *Service) Public(ctx context.Context, id string) (*Form, error) {
	f, err := scan(s.DB.QueryRow(ctx, `SELECT `+columns+` FROM forms
		WHERE id = $1 AND EXISTS (SELECT 1 FROM accounts a WHERE a.id = forms.account_id AND a.status = 'active')`, id))
	if err != nil {
		return nil, err
	}
	if err := s.loadTags(ctx, []*Form{f}); err != nil {
		return nil, err
	}
	return f, nil
}

// List returns up to limit forms, newest first, after the cursor.
func (s *Service) List(ctx context.Context, accountID string, after *Cursor, limit int) ([]*Form, error) {
	q := `SELECT ` + columns + ` FROM forms WHERE account_id = $1`
	args := []any{accountID}
	if after != nil {
		q += ` AND (created_at, id) < ($2, $3)`
		args = append(args, after.CreatedAt, after.ID)
	}
	q += fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT %d`, limit)
	rows, err := s.DB.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Form
	for rows.Next() {
		f, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, s.loadTags(ctx, out)
}

func (s *Service) loadTags(ctx context.Context, fs []*Form) error {
	if len(fs) == 0 {
		return nil
	}
	byID := make(map[string]*Form, len(fs))
	ids := make([]string, len(fs))
	for i, f := range fs {
		byID[f.ID] = f
		ids[i] = f.ID
		f.Tags = []Tag{}
	}
	rows, err := s.DB.Query(ctx, `
		SELECT ft.form_id, t.id, t.name FROM form_tags ft JOIN tags t ON t.id = ft.tag_id
		WHERE ft.form_id::text = ANY($1) ORDER BY lower(t.name), t.id`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var formID string
		var t Tag
		if err := rows.Scan(&formID, &t.ID, &t.Name); err != nil {
			return err
		}
		byID[formID].Tags = append(byID[formID].Tags, t)
	}
	return rows.Err()
}

// Delete removes a form. Contacts who signed up with it stay.
func (s *Service) Delete(ctx context.Context, accountID, id string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM forms WHERE account_id = $1 AND id = $2`, accountID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Service) inTx(ctx context.Context, f func(pgx.Tx) error) error {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := f(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
