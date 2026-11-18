package emails

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	// ErrNotFound means no email with that id in the account.
	ErrNotFound = errors.New("email not found")
	// ErrInvalidName means a name that's empty or over 200 characters.
	ErrInvalidName = errors.New("invalid email name")
	// ErrInvalidKind means a kind other than blocks or html.
	ErrInvalidKind = errors.New("invalid email kind")
)

// VersionError means an update named a version that isn't the email's
// current one: someone saved it since it was read.
type VersionError struct {
	Current int
}

func (e *VersionError) Error() string {
	return fmt.Sprintf("email version is %d", e.Current)
}

// MaxNameChars is the longest email name.
const MaxNameChars = 200

// Email is one of an account's emails.
type Email struct {
	ID        string
	AccountID string
	Name      string
	Kind      string
	Version   int
	Document  *Document
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Cursor is the keyset position of the last email on a page.
type Cursor struct {
	CreatedAt time.Time
	ID        string
}

// Service stores emails.
type Service struct {
	DB *pgxpool.Pool
}

const columns = `id, account_id, name, kind, version, document, created_at, updated_at`

func scan(row pgx.Row) (*Email, error) {
	var e Email
	var raw []byte
	err := row.Scan(&e.ID, &e.AccountID, &e.Name, &e.Kind, &e.Version, &raw, &e.CreatedAt, &e.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if e.Document, err = ParseDocument(e.Kind, raw); err != nil {
		return nil, fmt.Errorf("stored document of email %s: %w", e.ID, err)
	}
	return &e, nil
}

func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > MaxNameChars || strings.ContainsAny(name, "\r\n") {
		return "", ErrInvalidName
	}
	return name, nil
}

// Create adds an email. A nil doc starts it with NewDocument(kind); a doc
// must be one ParseDocument made for the same kind.
func (s *Service) Create(ctx context.Context, accountID, name, kind string, doc *Document) (*Email, error) {
	name, err := cleanName(name)
	if err != nil {
		return nil, err
	}
	if kind != KindBlocks && kind != KindHTML {
		return nil, ErrInvalidKind
	}
	if doc == nil {
		doc = NewDocument(kind)
	} else if doc.Kind() != kind {
		return nil, ErrInvalidKind
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return scan(s.DB.QueryRow(ctx, `
		INSERT INTO emails (account_id, name, kind, document) VALUES ($1, $2, $3, $4)
		RETURNING `+columns, accountID, name, kind, raw))
}

// Get returns one email.
func (s *Service) Get(ctx context.Context, accountID, id string) (*Email, error) {
	return scan(s.DB.QueryRow(ctx, `SELECT `+columns+` FROM emails WHERE account_id = $1 AND id = $2`, accountID, id))
}

// List returns up to limit of the account's emails, newest first, after
// the cursor (nil for the first page).
func (s *Service) List(ctx context.Context, accountID string, after *Cursor, limit int) ([]*Email, error) {
	q := `SELECT ` + columns + ` FROM emails WHERE account_id = $1`
	args := []any{accountID}
	if after != nil {
		args = append(args, after.CreatedAt, after.ID)
		q += ` AND (created_at, id) < ($2, $3::uuid)`
	}
	args = append(args, limit)
	q += fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT $%d`, len(args))
	rows, err := s.DB.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Email
	for rows.Next() {
		e, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Change is an update to an email's name or settings. Settings is raw JSON
// for some or all of the settings; fields it leaves out keep their values.
type Change struct {
	Name     *string
	Settings json.RawMessage
}

// Update renames an email or changes its settings, if version is still the
// current one.
func (s *Service) Update(ctx context.Context, accountID, id string, version int, c Change) (*Email, error) {
	var name string
	if c.Name != nil {
		var err error
		if name, err = cleanName(*c.Name); err != nil {
			return nil, err
		}
	}
	return s.save(ctx, accountID, id, version, func(e *Email) error {
		if c.Name != nil {
			e.Name = name
		}
		if c.Settings != nil {
			settings, err := ParseSettings(e.Document.Settings, c.Settings)
			if err != nil {
				return err
			}
			e.Document.Settings = settings
		}
		return nil
	})
}

// Replace swaps an email's whole document, if version is still the current
// one. The document must be one ParseDocument made for the email's kind.
func (s *Service) Replace(ctx context.Context, accountID, id string, version int, doc *Document) (*Email, error) {
	return s.save(ctx, accountID, id, version, func(e *Email) error {
		if doc.Kind() != e.Kind {
			return ErrInvalidKind
		}
		e.Document = doc
		return nil
	})
}

// save loads an email under a row lock, checks version, applies change and
// stores the result as the next version. MG-20's block operations use it
// too.
func (s *Service) save(ctx context.Context, accountID, id string, version int, change func(*Email) error) (*Email, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	e, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM emails WHERE account_id = $1 AND id = $2 FOR UPDATE`, accountID, id))
	if err != nil {
		return nil, err
	}
	if e.Version != version {
		return nil, &VersionError{Current: e.Version}
	}
	if err := change(e); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(e.Document)
	if err != nil {
		return nil, err
	}
	e, err = scan(tx.QueryRow(ctx, `
		UPDATE emails SET name = $3, document = $4, version = version + 1, updated_at = now()
		WHERE account_id = $1 AND id = $2
		RETURNING `+columns, accountID, id, e.Name, raw))
	if err != nil {
		return nil, err
	}
	return e, tx.Commit(ctx)
}

// Duplicate copies an email under a new name ("Copy of" the old one when
// name is ""). Block ids stay the same: they're unique within a document.
func (s *Service) Duplicate(ctx context.Context, accountID, id, name string) (*Email, error) {
	e, err := s.Get(ctx, accountID, id)
	if err != nil {
		return nil, err
	}
	if name == "" {
		name = "Copy of " + e.Name
		if r := []rune(name); len(r) > MaxNameChars {
			name = string(r[:MaxNameChars])
		}
	}
	return s.Create(ctx, accountID, name, e.Kind, e.Document)
}

// Delete removes an email.
func (s *Service) Delete(ctx context.Context, accountID, id string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM emails WHERE account_id = $1 AND id = $2`, accountID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
