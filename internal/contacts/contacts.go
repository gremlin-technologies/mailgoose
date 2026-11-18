// Package contacts holds an account's audience: its contacts, their
// statuses, and the suppression list that outlives deleted contacts
// (CON-1 to CON-3, CON-8).
package contacts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/auth"
)

// Statuses (CON-2). Only StatusSubscribed receives campaigns.
const (
	StatusUnconfirmed  = "unconfirmed"
	StatusSubscribed   = "subscribed"
	StatusUnsubscribed = "unsubscribed"
	StatusBounced      = "bounced"
	StatusComplained   = "complained"
)

// Sources: how a contact was added.
const (
	SourceForm   = "form"
	SourceImport = "import"
	SourceAPI    = "api"
	SourceManual = "manual"
)

// MaxNameLen is the most characters a first or last name may have.
const MaxNameLen = 100

var (
	// ErrNotFound means no contact with that id in the account.
	ErrNotFound = errors.New("contact not found")
	// ErrExists means the account already has a contact with that address.
	ErrExists = errors.New("contact exists")
	// ErrSuppressed means the contact unsubscribed, bounced or complained, and
	// only the person, through a signup form, can subscribe it again.
	ErrSuppressed = errors.New("contact suppressed")
)

// Suppressing reports whether a status keeps the address suppressed: an
// unsubscribe, a bounce or a complaint.
func Suppressing(status string) bool {
	return status == StatusUnsubscribed || status == StatusBounced || status == StatusComplained
}

// CanReceive reports whether a contact with this status may be sent a
// campaign. Only subscribed contacts may.
func CanReceive(status string) bool { return status == StatusSubscribed }

// Contact is one person in an account's audience.
type Contact struct {
	ID              string
	AccountID       string
	Email           string
	FirstName       *string
	LastName        *string
	Status          string
	Source          string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	StatusChangedAt time.Time
	// Tags are the contact's tags, sorted by name.
	Tags []TagRef
}

// TagRef names one of a contact's tags.
type TagRef struct {
	ID   string
	Name string
}

// Service reads and changes contacts.
type Service struct {
	DB  *pgxpool.Pool
	Now func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

const columns = `id, account_id, email, first_name, last_name, status, source, created_at, updated_at, status_changed_at,
	ARRAY(SELECT t.id::text FROM contact_tags ct JOIN tags t ON t.id = ct.tag_id WHERE ct.contact_id = contacts.id ORDER BY lower(t.name), t.id),
	ARRAY(SELECT t.name FROM contact_tags ct JOIN tags t ON t.id = ct.tag_id WHERE ct.contact_id = contacts.id ORDER BY lower(t.name), t.id)`

func scan(row pgx.Row) (*Contact, error) {
	var c Contact
	var tagIDs, tagNames []string
	err := row.Scan(&c.ID, &c.AccountID, &c.Email, &c.FirstName, &c.LastName, &c.Status, &c.Source,
		&c.CreatedAt, &c.UpdatedAt, &c.StatusChangedAt, &tagIDs, &tagNames)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	c.Tags = make([]TagRef, len(tagIDs))
	for i := range tagIDs {
		c.Tags[i] = TagRef{ID: tagIDs[i], Name: tagNames[i]}
	}
	return &c, err
}

// NormalizeEmail checks an address and lower-cases it, as contacts store it.
func NormalizeEmail(raw string) (string, error) {
	e, err := auth.NormalizeEmail(raw)
	if err != nil {
		return "", err
	}
	return strings.ToLower(e), nil
}

func normalizeName(field string, v *string) (*string, error) {
	if v == nil {
		return nil, nil
	}
	n := strings.TrimSpace(*v)
	if n == "" {
		return nil, nil
	}
	if utf8.RuneCountInString(n) > MaxNameLen || strings.ContainsAny(n, "\r\n") {
		return nil, &auth.ValidationError{Code: "invalid_name", Field: field,
			Message: fmt.Sprintf("%s must be at most %d characters on one line.", strings.ReplaceAll(field, "_", " "), MaxNameLen)}
	}
	return &n, nil
}

// settable is the statuses a person or their agent may set. Bounced and
// complained come only from Amazon SES events.
func settable(field, status string) error {
	switch status {
	case StatusSubscribed, StatusUnconfirmed, StatusUnsubscribed:
		return nil
	}
	return &auth.ValidationError{Code: "invalid_status", Field: field,
		Message: "status must be subscribed, unconfirmed or unsubscribed; bounced and complained come from Amazon SES."}
}

// suppressionHash is what the suppression list keeps of an address.
func suppressionHash(accountID, email string) string {
	sum := sha256.Sum256([]byte(accountID + "\x00" + email))
	return hex.EncodeToString(sum[:])
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// takeSuppression removes email's suppression entry, if any, and returns its
// reason ("" when there was none). The caller puts the reason on the contact.
func takeSuppression(ctx context.Context, tx pgx.Tx, accountID, email string) (string, error) {
	var reason string
	err := tx.QueryRow(ctx, `DELETE FROM suppressions WHERE account_id = $1 AND email_hash = $2 RETURNING reason`,
		accountID, suppressionHash(accountID, email)).Scan(&reason)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return reason, err
}

// CreateInput is a new contact.
type CreateInput struct {
	Email     string
	FirstName *string
	LastName  *string
	// Status is subscribed when empty. A suppressed address keeps its
	// suppressed status whatever is asked.
	Status string
	// Source is api when empty.
	Source string
}

// Create adds a contact. An address on the suppression list comes back with
// the status it had when it was deleted.
func (s *Service) Create(ctx context.Context, accountID string, in CreateInput) (*Contact, error) {
	email, err := NormalizeEmail(in.Email)
	if err != nil {
		return nil, err
	}
	first, err := normalizeName("first_name", in.FirstName)
	if err != nil {
		return nil, err
	}
	last, err := normalizeName("last_name", in.LastName)
	if err != nil {
		return nil, err
	}
	status := in.Status
	if status == "" {
		status = StatusSubscribed
	}
	if err := settable("status", status); err != nil {
		return nil, err
	}
	source := in.Source
	if source == "" {
		source = SourceAPI
	}
	switch source {
	case SourceAPI, SourceManual, SourceForm, SourceImport:
	default:
		return nil, &auth.ValidationError{Code: "invalid_source", Field: "source", Message: "source must be api or manual."}
	}

	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	reason, err := takeSuppression(ctx, tx, accountID, email)
	if err != nil {
		return nil, err
	}
	if reason != "" {
		status = reason
	}
	if Counted(status) {
		if err := ensureRoom(ctx, tx, accountID, 1); err != nil {
			return nil, err
		}
	}
	now := s.now()
	c, err := scan(tx.QueryRow(ctx, `
		INSERT INTO contacts (account_id, email, first_name, last_name, status, source, created_at, updated_at, status_changed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $7, $7)
		RETURNING `+columns, accountID, email, first, last, status, source, now))
	if isUniqueViolation(err) {
		return nil, ErrExists
	}
	if err != nil {
		return nil, err
	}
	return c, tx.Commit(ctx)
}

// Get returns one contact of the account.
func (s *Service) Get(ctx context.Context, accountID, id string) (*Contact, error) {
	return scan(s.DB.QueryRow(ctx, `SELECT `+columns+` FROM contacts WHERE account_id = $1 AND id = $2`, accountID, id))
}

// UpdateInput is a partial update; fields that aren't Set are left alone.
// A null name removes it; email and status can't be null.
type UpdateInput struct {
	Email     auth.Field
	FirstName auth.Field
	LastName  auth.Field
	Status    auth.Field
}

// Update changes the fields set in u. A contact that unsubscribed, bounced or
// complained can't be moved back to subscribed or unconfirmed here, and a
// bounce or complaint stays (ErrSuppressed); the person can subscribe again
// through a signup form.
func (s *Service) Update(ctx context.Context, accountID, id string, u UpdateInput) (*Contact, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	c, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM contacts WHERE account_id = $1 AND id = $2 FOR UPDATE`, accountID, id))
	if err != nil {
		return nil, err
	}

	status := c.Status
	if u.Status.Set {
		if u.Status.Value == nil {
			return nil, &auth.ValidationError{Code: "invalid_status", Field: "status", Message: "status can't be removed."}
		}
		want := *u.Status.Value
		if err := settable("status", want); err != nil {
			return nil, err
		}
		switch {
		case want == c.Status:
		case c.Status == StatusBounced || c.Status == StatusComplained,
			c.Status == StatusUnsubscribed && want != StatusUnsubscribed:
			return nil, ErrSuppressed
		}
		status = want
	}
	email := c.Email
	if u.Email.Set {
		if u.Email.Value == nil {
			return nil, &auth.ValidationError{Code: "invalid_email", Field: "email", Message: "email can't be removed."}
		}
		if email, err = NormalizeEmail(*u.Email.Value); err != nil {
			return nil, err
		}
		if email != c.Email {
			// The old address stays suppressed when the contact leaves it,
			// or it could be added back as subscribed.
			if Suppressing(c.Status) {
				if err := suppress(ctx, tx, accountID, c.Email, c.Status, s.now()); err != nil {
					return nil, err
				}
			}
			// A new address that was suppressed keeps its suppression.
			reason, err := takeSuppression(ctx, tx, accountID, email)
			if err != nil {
				return nil, err
			}
			if reason != "" && !Suppressing(status) {
				status = reason
			}
		}
	}
	first, last := c.FirstName, c.LastName
	if u.FirstName.Set {
		if first, err = normalizeName("first_name", u.FirstName.Value); err != nil {
			return nil, err
		}
	}
	if u.LastName.Set {
		if last, err = normalizeName("last_name", u.LastName.Value); err != nil {
			return nil, err
		}
	}

	if !Counted(c.Status) && Counted(status) {
		if err := ensureRoom(ctx, tx, accountID, 1); err != nil {
			return nil, err
		}
	}
	now := s.now()
	changed := c.StatusChangedAt
	if status != c.Status {
		changed = now
	}
	c, err = scan(tx.QueryRow(ctx, `
		UPDATE contacts SET email = $3, first_name = $4, last_name = $5, status = $6, status_changed_at = $7, updated_at = $8
		WHERE account_id = $1 AND id = $2
		RETURNING `+columns, accountID, id, email, first, last, status, changed, now))
	if isUniqueViolation(err) {
		return nil, ErrExists
	}
	if err != nil {
		return nil, err
	}
	return c, tx.Commit(ctx)
}

// Delete removes a contact for good. If it had unsubscribed, bounced or
// complained, a hash of its address stays on the suppression list.
func (s *Service) Delete(ctx context.Context, accountID, id string) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var email, status string
	err = tx.QueryRow(ctx, `DELETE FROM contacts WHERE account_id = $1 AND id = $2 RETURNING email, status`, accountID, id).Scan(&email, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if Suppressing(status) {
		if err := suppress(ctx, tx, accountID, email, status, s.now()); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// suppress puts email on the account's suppression list with reason, or
// updates its entry.
func suppress(ctx context.Context, tx pgx.Tx, accountID, email, reason string, at time.Time) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO suppressions (account_id, email_hash, reason, created_at) VALUES ($1, $2, $3, $4)
		ON CONFLICT (account_id, email_hash) DO UPDATE SET reason = EXCLUDED.reason, created_at = EXCLUDED.created_at`,
		accountID, suppressionHash(accountID, email), reason, at)
	return err
}

// IsSuppressed reports whether a deleted contact left email on the
// account's suppression list.
func (s *Service) IsSuppressed(ctx context.Context, accountID, email string) (bool, error) {
	email, err := NormalizeEmail(email)
	if err != nil {
		return false, err
	}
	var ok bool
	err = s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM suppressions WHERE account_id = $1 AND email_hash = $2)`,
		accountID, suppressionHash(accountID, email)).Scan(&ok)
	return ok, err
}

// Filter narrows List. Empty fields don't filter.
type Filter struct {
	// Email matches addresses containing it, ignoring case.
	Email string
	// Name matches first, last or full names containing it, ignoring case.
	Name string
	// Status is one exact status.
	Status string
	// TagID is a tag the contact must have.
	TagID string
}

// where appends f's conditions to a query on contacts, numbering its
// parameters after those already in args.
func (f Filter) where(q string, args []any) (string, []any) {
	add := func(cond string, v any) {
		args = append(args, v)
		q += " AND " + strings.ReplaceAll(cond, "?", fmt.Sprintf("$%d", len(args)))
	}
	if e := strings.TrimSpace(f.Email); e != "" {
		add(`email LIKE ?`, likePattern(strings.ToLower(e)))
	}
	if n := strings.TrimSpace(f.Name); n != "" {
		add(`(coalesce(first_name, '') || ' ' || coalesce(last_name, '')) ILIKE ?`, likePattern(n))
	}
	if f.Status != "" {
		add(`status = ?`, f.Status)
	}
	if f.TagID != "" {
		add(`EXISTS (SELECT 1 FROM contact_tags ct WHERE ct.contact_id = contacts.id AND ct.tag_id = ?::uuid)`, f.TagID)
	}
	return q, args
}

// Cursor is the keyset position after a row: List returns rows after it.
type Cursor struct {
	CreatedAt time.Time
	ID        string
}

// CursorOf is the cursor just after c.
func CursorOf(c *Contact) Cursor { return Cursor{CreatedAt: c.CreatedAt, ID: c.ID} }

func likePattern(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(s) + "%"
}

// List returns up to limit contacts of the account, newest first, after
// the cursor when one is given.
func (s *Service) List(ctx context.Context, accountID string, f Filter, after *Cursor, limit int) ([]*Contact, error) {
	q, args := f.where(`SELECT `+columns+` FROM contacts WHERE account_id = $1`, []any{accountID})
	if after != nil {
		args = append(args, after.CreatedAt, after.ID)
		q += fmt.Sprintf(" AND (created_at, id) < ($%d, $%d::uuid)", len(args)-1, len(args))
	}
	args = append(args, limit)
	q += fmt.Sprintf(" ORDER BY created_at DESC, id DESC LIMIT $%d", len(args))

	rows, err := s.DB.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Contact
	for rows.Next() {
		c, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Recipients returns up to limit of the account's contacts that may be sent
// a campaign, in id order after afterID ("" for the first batch). Campaign
// sending (MG-30) selects its audience only through here, so an
// unconfirmed, unsubscribed, bounced or complained contact is never mailed.
func (s *Service) Recipients(ctx context.Context, accountID, afterID string, limit int) ([]*Contact, error) {
	q := `SELECT ` + columns + ` FROM contacts WHERE account_id = $1 AND status = '` + StatusSubscribed + `'`
	args := []any{accountID}
	if afterID != "" {
		args = append(args, afterID)
		q += ` AND id > $2::uuid`
	}
	args = append(args, limit)
	q += fmt.Sprintf(` ORDER BY id LIMIT $%d`, len(args))
	rows, err := s.DB.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Contact
	for rows.Next() {
		c, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
