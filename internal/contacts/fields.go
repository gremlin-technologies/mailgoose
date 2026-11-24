package contacts

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gremlin-technologies/mailgoose/internal/auth"
)

// Custom contact fields (MG-130): text fields an account defines beside
// email, first name, last name and tags, shown on the contacts table and
// exported under their names.
const (
	// MaxFieldNameLen is the most characters a field's name may have.
	MaxFieldNameLen = 50
	// MaxFieldValueLen is the most characters one value may have.
	MaxFieldValueLen = 500
	// FreeFieldLimit and PaidFieldLimit are the most fields an account on
	// each plan may define. They're the same today; a higher paid limit can
	// become an upgrade later. FieldLimit picks between them.
	FreeFieldLimit = 20
	PaidFieldLimit = 20
)

// FieldLimit is the most contact fields an account on plan ("free" or
// "paid") may define.
func FieldLimit(plan string) int {
	if plan == "free" {
		return FreeFieldLimit
	}
	return PaidFieldLimit
}

// ReservedFieldKeys are keys a field can't have: the contact's own columns
// and the contacts export's columns, so neither the API's contacts nor an
// export's header can be read two ways.
var ReservedFieldKeys = func() []string {
	keys := []string{"id", "contact_id", "email", "first_name", "last_name", "status", "tags", "source",
		"created_at", "updated_at", "status_changed_at", "permission_confirmed"}
	for _, h := range ExportHeader {
		if !slices.Contains(keys, h) {
			keys = append(keys, h)
		}
	}
	return keys
}()

var (
	// ErrFieldNotFound means no field with that id in the account.
	ErrFieldNotFound = errors.New("field not found")
	// ErrFieldExists means the account already has a field with that name,
	// ignoring case.
	ErrFieldExists = errors.New("field exists")
)

// FieldKeyExistsError means the account already has a field whose key is
// the one the new name works out to.
type FieldKeyExistsError struct{ Key string }

func (e *FieldKeyExistsError) Error() string { return "field key exists: " + e.Key }

// FieldLimitError means the account has as many fields as its plan allows.
type FieldLimitError struct {
	Limit int
	Count int
}

func (e *FieldLimitError) Error() string {
	return fmt.Sprintf("field limit: %d of %d fields", e.Count, e.Limit)
}

// Field is one of an account's contact fields.
type Field struct {
	ID string
	// Key is what the API and CLI call the field. It's worked out from the
	// name when the field is made, and a rename keeps it.
	Key string
	// Name is the field's label, and its column's header in an export.
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

var notKeyChars = regexp.MustCompile(`[^a-z0-9]+`)

// FieldKey works a field's key out of its name: lower case, each run of
// anything but a to z and 0 to 9 one "_", no "_" at either end, and "f_" in
// front when it doesn't start with a letter. It's "" when the name has no
// letter or digit from a to z or 0 to 9.
func FieldKey(name string) string {
	k := strings.Trim(notKeyChars.ReplaceAllString(strings.ToLower(name), "_"), "_")
	if k != "" && (k[0] < 'a' || k[0] > 'z') {
		k = "f_" + k
	}
	return k
}

// normalizeFieldName checks a field's name and returns it trimmed, with the
// key it works out to.
func normalizeFieldName(raw string) (name, key string, err error) {
	name = strings.TrimSpace(raw)
	if name == "" || utf8.RuneCountInString(name) > MaxFieldNameLen || strings.ContainsAny(name, "\r\n") {
		return "", "", &auth.ValidationError{Code: "invalid_field_name", Field: "name",
			Message: fmt.Sprintf("A field name is 1 to %d characters on one line.", MaxFieldNameLen)}
	}
	key = FieldKey(name)
	if key == "" {
		return "", "", &auth.ValidationError{Code: "invalid_field_name", Field: "name",
			Message: "A field name needs at least one letter (a to z) or digit, to make its key from."}
	}
	if slices.Contains(ReservedFieldKeys, key) {
		return "", "", &auth.ValidationError{Code: "reserved_field_name", Field: "name",
			Message: fmt.Sprintf("%q would have the key %s, which is one of a contact's own columns. Pick another name.", name, key)}
	}
	return name, key, nil
}

const fieldColumns = `id, key, name, created_at, updated_at`

func scanField(row pgx.Row) (*Field, error) {
	var f Field
	err := row.Scan(&f.ID, &f.Key, &f.Name, &f.CreatedAt, &f.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrFieldNotFound
	}
	return &f, err
}

// fieldConflict turns a unique violation into ErrFieldExists or a
// *FieldKeyExistsError.
func fieldConflict(err error, key string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		if pgErr.ConstraintName == "contact_fields_account_key" {
			return &FieldKeyExistsError{Key: key}
		}
		return ErrFieldExists
	}
	return err
}

// CreateField adds a contact field. An account has at most its plan's
// FieldLimit fields (*FieldLimitError past it).
func (s *Service) CreateField(ctx context.Context, accountID, name string) (*Field, error) {
	name, key, err := normalizeFieldName(name)
	if err != nil {
		return nil, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Locking the account row keeps two creates from both slipping under
	// the limit.
	var plan string
	if err := tx.QueryRow(ctx, `SELECT plan FROM accounts WHERE id = $1 FOR UPDATE`, accountID).Scan(&plan); err != nil {
		return nil, fmt.Errorf("read plan: %w", err)
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM contact_fields WHERE account_id = $1`, accountID).Scan(&n); err != nil {
		return nil, err
	}
	if limit := FieldLimit(plan); n >= limit {
		return nil, &FieldLimitError{Limit: limit, Count: n}
	}
	// The same name has the same key too; say it's the name.
	var taken bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM contact_fields WHERE account_id = $1 AND lower(name) = lower($2))`,
		accountID, name).Scan(&taken); err != nil {
		return nil, err
	}
	if taken {
		return nil, ErrFieldExists
	}
	now := s.now()
	f, err := scanField(tx.QueryRow(ctx, `
		INSERT INTO contact_fields (account_id, key, name, created_at, updated_at) VALUES ($1, $2, $3, $4, $4)
		RETURNING `+fieldColumns, accountID, key, name, now))
	if err != nil {
		return nil, fieldConflict(err, key)
	}
	return f, tx.Commit(ctx)
}

// RenameField changes a field's name. Its key and every contact's value
// stay. The new name follows the same rules as a new field's.
func (s *Service) RenameField(ctx context.Context, accountID, id, name string) (*Field, error) {
	name, _, err := normalizeFieldName(name)
	if err != nil {
		return nil, err
	}
	f, err := scanField(s.DB.QueryRow(ctx, `
		UPDATE contact_fields SET name = $3, updated_at = $4 WHERE account_id = $1 AND id = $2
		RETURNING `+fieldColumns, accountID, id, name, s.now()))
	if err != nil {
		return nil, fieldConflict(err, "")
	}
	return f, nil
}

// FieldDeleted says what deleting a field changed.
type FieldDeleted struct {
	// ValuesDeleted is how many contacts had a value for the field.
	ValuesDeleted int
}

// DeleteField deletes a field and every contact's value for it, in one
// transaction.
func (s *Service) DeleteField(ctx context.Context, accountID, id string) (*FieldDeleted, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Locking the field keeps a contact change that sets it from racing
	// the delete; see lockFields.
	var key string
	err = tx.QueryRow(ctx, `SELECT key FROM contact_fields WHERE account_id = $1 AND id = $2 FOR UPDATE`, accountID, id).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrFieldNotFound
	}
	if err != nil {
		return nil, err
	}
	res, err := tx.Exec(ctx, `UPDATE contacts SET fields = fields - $2 WHERE account_id = $1 AND fields ? $2`, accountID, key)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM contact_fields WHERE id = $1`, id); err != nil {
		return nil, err
	}
	return &FieldDeleted{ValuesDeleted: int(res.RowsAffected())}, tx.Commit(ctx)
}

// GetField returns one field of the account.
func (s *Service) GetField(ctx context.Context, accountID, id string) (*Field, error) {
	return scanField(s.DB.QueryRow(ctx, `SELECT `+fieldColumns+` FROM contact_fields WHERE account_id = $1 AND id = $2`, accountID, id))
}

// FieldCursor is the keyset position after a field in ListFields order.
type FieldCursor struct {
	CreatedAt time.Time
	ID        string
}

// ListFields returns up to limit of the account's fields in the order they
// were made, after the cursor when one is given.
func (s *Service) ListFields(ctx context.Context, accountID string, after *FieldCursor, limit int) ([]*Field, error) {
	return listFields(ctx, s.DB, accountID, after, limit)
}

// AllFields returns every field of the account, in the order they were
// made.
func (s *Service) AllFields(ctx context.Context, accountID string) ([]*Field, error) {
	return listFields(ctx, s.DB, accountID, nil, 0)
}

func listFields(ctx context.Context, db querier, accountID string, after *FieldCursor, limit int) ([]*Field, error) {
	q := `SELECT ` + fieldColumns + ` FROM contact_fields WHERE account_id = $1`
	args := []any{accountID}
	if after != nil {
		args = append(args, after.CreatedAt, after.ID)
		q += fmt.Sprintf(` AND (created_at, id) > ($%d, $%d::uuid)`, len(args)-1, len(args))
	}
	q += ` ORDER BY created_at, id`
	if limit > 0 {
		args = append(args, limit)
		q += fmt.Sprintf(` LIMIT $%d`, len(args))
	}
	rows, err := db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Field
	for rows.Next() {
		f, err := scanField(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// lockFields checks that every key is one of the account's fields, and
// locks those fields until tx ends so none is deleted before the values are
// written.
func lockFields(ctx context.Context, tx pgx.Tx, accountID string, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT key FROM contact_fields WHERE account_id = $1 AND key = ANY($2) FOR SHARE`, accountID, keys)
	if err != nil {
		return err
	}
	found := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return err
		}
		found[k] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, k := range keys {
		if !found[k] {
			return &auth.ValidationError{Code: "unknown_field", Field: "fields",
				Message: fmt.Sprintf("This account has no contact field with the key %q.", k)}
		}
	}
	return nil
}

// normalizeFieldValue checks one value and returns it trimmed; "" means no
// value.
func normalizeFieldValue(key string, v *string) (string, error) {
	if v == nil {
		return "", nil
	}
	t := strings.TrimSpace(*v)
	if utf8.RuneCountInString(t) > MaxFieldValueLen || strings.ContainsAny(t, "\r\n") {
		return "", &auth.ValidationError{Code: "invalid_field_value", Field: "fields." + key,
			Message: fmt.Sprintf("The value for %s must be at most %d characters on one line.", key, MaxFieldValueLen)}
	}
	return t, nil
}

// applyFields checks changes against the account's fields and returns
// current with them applied: a nil or empty value takes the key off, any
// other value sets it. Keys left out of changes stay as they are.
func applyFields(ctx context.Context, tx pgx.Tx, accountID string, current map[string]string, changes map[string]*string) (map[string]string, error) {
	out := maps.Clone(current)
	if out == nil {
		out = map[string]string{}
	}
	if len(changes) == 0 {
		return out, nil
	}
	keys := slices.Sorted(maps.Keys(changes))
	if err := lockFields(ctx, tx, accountID, keys); err != nil {
		return nil, err
	}
	for _, k := range keys {
		v, err := normalizeFieldValue(k, changes[k])
		if err != nil {
			return nil, err
		}
		if v == "" {
			delete(out, k)
		} else {
			out[k] = v
		}
	}
	return out, nil
}
