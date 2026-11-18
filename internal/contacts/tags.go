package contacts

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/gremlin-technologies/mailgoose/internal/auth"
)

// MaxTagNameLen is the most characters a tag name may have.
const MaxTagNameLen = 50

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// MaxBulkIDs is the most contact ids one tag or untag request may name.
const MaxBulkIDs = 1000

var (
	// ErrTagNotFound means no tag with that id in the account.
	ErrTagNotFound = errors.New("tag not found")
	// ErrTagExists means the account already has a tag with that name,
	// ignoring case.
	ErrTagExists = errors.New("tag exists")
)

// Tag is one of an account's tags.
type Tag struct {
	ID        string
	Name      string
	CreatedAt time.Time
	// Contacts is how many contacts have the tag.
	Contacts int
}

func normalizeTagName(raw string) (string, error) {
	n := strings.TrimSpace(raw)
	if n == "" || utf8.RuneCountInString(n) > MaxTagNameLen || strings.ContainsAny(n, ",\r\n") {
		return "", &auth.ValidationError{Code: "invalid_tag_name", Field: "name",
			Message: fmt.Sprintf("A tag name is 1 to %d characters on one line, without commas.", MaxTagNameLen)}
	}
	return n, nil
}

const tagColumns = `id, name, created_at, (SELECT count(*) FROM contact_tags ct WHERE ct.tag_id = tags.id)`

func scanTag(row pgx.Row) (*Tag, error) {
	var t Tag
	err := row.Scan(&t.ID, &t.Name, &t.CreatedAt, &t.Contacts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTagNotFound
	}
	return &t, err
}

// CreateTag adds a tag.
func (s *Service) CreateTag(ctx context.Context, accountID, name string) (*Tag, error) {
	name, err := normalizeTagName(name)
	if err != nil {
		return nil, err
	}
	t, err := scanTag(s.DB.QueryRow(ctx, `INSERT INTO tags (account_id, name, created_at) VALUES ($1, $2, $3) RETURNING `+tagColumns,
		accountID, name, s.now()))
	if isUniqueViolation(err) {
		return nil, ErrTagExists
	}
	return t, err
}

// RenameTag changes a tag's name; contacts keep it.
func (s *Service) RenameTag(ctx context.Context, accountID, id, name string) (*Tag, error) {
	name, err := normalizeTagName(name)
	if err != nil {
		return nil, err
	}
	t, err := scanTag(s.DB.QueryRow(ctx, `UPDATE tags SET name = $3 WHERE account_id = $1 AND id = $2 RETURNING `+tagColumns,
		accountID, id, name))
	if isUniqueViolation(err) {
		return nil, ErrTagExists
	}
	return t, err
}

// TagDeleted says what deleting a tag changed.
type TagDeleted struct {
	// ContactsUntagged is how many contacts had the tag.
	ContactsUntagged int
	// CampaignsUpdated is how many draft or scheduled campaigns lost the tag
	// from their recipients.
	CampaignsUpdated int
}

// DeleteTag removes a tag from every contact and deletes it.
func (s *Service) DeleteTag(ctx context.Context, accountID, id string) (*TagDeleted, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT true FROM tags WHERE account_id = $1 AND id = $2 FOR UPDATE`, accountID, id).Scan(&exists); errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTagNotFound
	} else if err != nil {
		return nil, err
	}
	res, err := tx.Exec(ctx, `DELETE FROM contact_tags WHERE tag_id = $1`, id)
	if err != nil {
		return nil, err
	}
	// Draft and scheduled campaigns that went to the tag change: count them
	// and move their version on, so a launch confirmation made before goes
	// stale. The tag comes off every campaign with it (ON DELETE CASCADE).
	camps, err := tx.Exec(ctx, `
		UPDATE campaigns SET version = version + 1, updated_at = now()
		WHERE status IN ('draft', 'scheduled') AND id IN (SELECT campaign_id FROM campaign_tags WHERE tag_id = $1)`, id)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM tags WHERE id = $1`, id); err != nil {
		return nil, err
	}
	return &TagDeleted{ContactsUntagged: int(res.RowsAffected()), CampaignsUpdated: int(camps.RowsAffected())}, tx.Commit(ctx)
}

// GetTag returns one tag of the account.
func (s *Service) GetTag(ctx context.Context, accountID, id string) (*Tag, error) {
	return scanTag(s.DB.QueryRow(ctx, `SELECT `+tagColumns+` FROM tags WHERE account_id = $1 AND id = $2`, accountID, id))
}

// TagCursor is the keyset position after a tag in List order.
type TagCursor struct {
	Name string
	ID   string
}

// ListTags returns up to limit tags of the account, by name, after the
// cursor when one is given. A non-empty name returns only the tag with that
// name, ignoring case.
func (s *Service) ListTags(ctx context.Context, accountID, name string, after *TagCursor, limit int) ([]*Tag, error) {
	q := `SELECT ` + tagColumns + ` FROM tags WHERE account_id = $1`
	args := []any{accountID}
	if n := strings.TrimSpace(name); n != "" {
		args = append(args, n)
		q += fmt.Sprintf(` AND lower(name) = lower($%d)`, len(args))
	}
	if after != nil {
		args = append(args, after.Name, after.ID)
		q += fmt.Sprintf(` AND (lower(name), id) > (lower($%d), $%d::uuid)`, len(args)-1, len(args))
	}
	args = append(args, limit)
	q += fmt.Sprintf(` ORDER BY lower(name), id LIMIT $%d`, len(args))
	rows, err := s.DB.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Tag
	for rows.Next() {
		t, err := scanTag(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Selection picks the contacts a bulk tag change applies to: the listed
// ids, or every contact matching a filter. Exactly one is used; ids that
// aren't the account's are skipped.
type Selection struct {
	IDs    []string
	Filter *Filter
}

// TagChange says what a bulk tag change did.
type TagChange struct {
	// Matched is how many of the account's contacts were selected.
	Matched int
	// Changed is how many gained or lost the tag; the rest already had it
	// or didn't have it.
	Changed int
}

func (sel Selection) query(accountID string) (string, []any, error) {
	switch {
	case sel.Filter != nil && sel.IDs != nil:
		return "", nil, &auth.ValidationError{Code: "invalid_selection", Field: "contact_ids",
			Message: "Send contact_ids or filter, not both."}
	case sel.Filter != nil:
		q, args := sel.Filter.where(`SELECT id FROM contacts WHERE account_id = $1`, []any{accountID})
		return q, args, nil
	case len(sel.IDs) == 0:
		return "", nil, &auth.ValidationError{Code: "invalid_selection", Field: "contact_ids",
			Message: "Send contact_ids with at least one id, or a filter."}
	case len(sel.IDs) > MaxBulkIDs:
		return "", nil, &auth.ValidationError{Code: "invalid_selection", Field: "contact_ids",
			Message: fmt.Sprintf("Send at most %d contact_ids at a time, or use a filter.", MaxBulkIDs)}
	}
	for _, id := range sel.IDs {
		if !uuidPattern.MatchString(id) {
			return "", nil, &auth.ValidationError{Code: "invalid_selection", Field: "contact_ids",
				Message: fmt.Sprintf("%q isn't a contact id.", id)}
		}
	}
	return `SELECT id FROM contacts WHERE account_id = $1 AND id = ANY($2::uuid[])`, []any{accountID, sel.IDs}, nil
}

// TagContacts puts a tag on the selected contacts.
func (s *Service) TagContacts(ctx context.Context, accountID, tagID string, sel Selection) (*TagChange, error) {
	return s.changeTags(ctx, accountID, tagID, sel, true)
}

// UntagContacts takes a tag off the selected contacts.
func (s *Service) UntagContacts(ctx context.Context, accountID, tagID string, sel Selection) (*TagChange, error) {
	return s.changeTags(ctx, accountID, tagID, sel, false)
}

func (s *Service) changeTags(ctx context.Context, accountID, tagID string, sel Selection, add bool) (*TagChange, error) {
	selected, args, err := sel.query(accountID)
	if err != nil {
		return nil, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Locking the tag keeps a delete from racing the change.
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT true FROM tags WHERE account_id = $1 AND id = $2 FOR SHARE`, accountID, tagID).Scan(&exists); errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTagNotFound
	} else if err != nil {
		return nil, err
	}

	tagArg := fmt.Sprintf("$%d::uuid", len(args)+1)
	var q string
	if add {
		q = `WITH sel AS (` + selected + `), ins AS (
			INSERT INTO contact_tags (contact_id, tag_id, created_at)
			SELECT id, ` + tagArg + `, now() FROM sel
			ON CONFLICT DO NOTHING RETURNING 1)
			SELECT (SELECT count(*) FROM sel), (SELECT count(*) FROM ins)`
	} else {
		q = `WITH sel AS (` + selected + `), del AS (
			DELETE FROM contact_tags WHERE tag_id = ` + tagArg + ` AND contact_id IN (SELECT id FROM sel) RETURNING 1)
			SELECT (SELECT count(*) FROM sel), (SELECT count(*) FROM del)`
	}
	var ch TagChange
	if err := tx.QueryRow(ctx, q, append(args, tagID)...).Scan(&ch.Matched, &ch.Changed); err != nil {
		return nil, err
	}
	return &ch, tx.Commit(ctx)
}

// TagsExist reports whether every id is one of the account's tags.
func (s *Service) TagsExist(ctx context.Context, accountID string, ids []string) (bool, error) {
	uniq := map[string]bool{}
	for _, id := range ids {
		uniq[id] = true
	}
	var n int
	err := s.DB.QueryRow(ctx, `SELECT count(*) FROM tags WHERE account_id = $1 AND id::text = ANY($2)`, accountID, ids).Scan(&n)
	return n == len(uniq), err
}
