package contacts

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ExportHeader is the header row of a contacts export (CON-7). An import
// reads email, first_name, last_name, status and tags back, so an export
// imports as the same contacts and tags. permission_confirmed is true or
// false for contacts an import added, saying whether the account confirmed
// it had their permission, and empty for others. The signup_ columns are
// from the contact's latest signup through a form (CMP-3): which form,
// when, from which IP, and when they confirmed; empty when none.
var ExportHeader = []string{
	"email", "first_name", "last_name", "status", "tags", "source", "created_at", "status_changed_at", "permission_confirmed",
	"signup_form_id", "signup_at", "signup_ip", "signup_confirmed_at",
}

// guardFormula puts a ' before a cell a spreadsheet would read as a formula,
// so opening an export can't run one. An import takes it off again.
func guardFormula(v string) string {
	if v != "" && strings.ContainsRune(formulaStart, rune(v[0])) {
		return "'" + v
	}
	return v
}

// ExportCSV writes the account's contacts matching f to w as CSV, oldest
// first, and returns how many it wrote. Contacts held by an import are
// included; the export is everything the account holds.
func (s *Service) ExportCSV(ctx context.Context, accountID string, f Filter, w io.Writer) (int, error) {
	q, args := f.where(`
		SELECT email, coalesce(first_name, ''), coalesce(last_name, ''), status,
			coalesce((SELECT string_agg(t.name, ', ' ORDER BY lower(t.name), t.id)
				FROM contact_tags ct JOIN tags t ON t.id = ct.tag_id WHERE ct.contact_id = contacts.id), ''),
			source, created_at, status_changed_at,
			(SELECT i.permission_confirmed FROM imports i WHERE i.id = contacts.import_id),
			coalesce(sc.signup_form::text, ''), sc.signup_at, coalesce(sc.signup_ip, ''), sc.signup_confirmed
		FROM contacts
		LEFT JOIN LATERAL (SELECT k.form_id AS signup_form, k.created_at AS signup_at, k.ip AS signup_ip,
				k.confirmed_at AS signup_confirmed
			FROM consents k WHERE k.contact_id = contacts.id AND k.source = 'form' ORDER BY k.created_at DESC, k.id DESC LIMIT 1) sc ON true
		WHERE account_id = $1`, []any{accountID})
	q += ` ORDER BY created_at, id`
	rows, err := s.DB.Query(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	cw := csv.NewWriter(w)
	if err := cw.Write(ExportHeader); err != nil {
		return 0, err
	}
	n := 0
	for rows.Next() {
		var email, first, last, status, tags, source, signupForm, signupIP string
		var created, changed time.Time
		var permission *bool
		var signupAt, signupConfirmed *time.Time
		if err := rows.Scan(&email, &first, &last, &status, &tags, &source, &created, &changed, &permission,
			&signupForm, &signupAt, &signupIP, &signupConfirmed); err != nil {
			return n, err
		}
		perm := ""
		if permission != nil {
			perm = strconv.FormatBool(*permission)
		}
		var guarded []string
		for _, t := range strings.Split(tags, ", ") {
			guarded = append(guarded, guardFormula(t))
		}
		if err := cw.Write([]string{
			guardFormula(email), guardFormula(first), guardFormula(last), status, strings.Join(guarded, ", "), source,
			created.UTC().Format(time.RFC3339), changed.UTC().Format(time.RFC3339), perm,
			signupForm, formatTime(signupAt), guardFormula(signupIP), formatTime(signupConfirmed),
		}); err != nil {
			return n, err
		}
		n++
		if n%1000 == 0 {
			cw.Flush()
			if err := cw.Error(); err != nil {
				return n, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return n, err
	}
	cw.Flush()
	return n, cw.Error()
}

// formatTime is t in UTC as RFC 3339, or empty for nil.
func formatTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// ContactData is everything stored about one contact, for a data request
// (CMP-9).
type ContactData struct {
	Contact *Contact
	// Tags are the contact's tags with when each was put on it.
	Tags []TagAdded
	// Import is the import that added the contact, if one did.
	Import *ImportRef
	// SkippedRows are rows of the account's imports that held the contact's
	// address and weren't added, as the skip report keeps them.
	SkippedRows []ContactSkippedRow
	// Consents are the contact's consent records, oldest first: signups
	// through forms, and the list source of the import that added it.
	Consents []Consent
}

// Consent is the record of one signup through a form (CMP-3), or of the
// list source an import gave for the contact (MG-63).
type Consent struct {
	ID string
	// FormID is nil once the form is deleted, and for an import; Wording
	// still says what it showed.
	FormID *string
	// Source is "form" or "import".
	Source string
	// IP is where a form signup was posted from; nil for an import.
	IP *string
	// ImportID is the import, for an import's record, while it exists.
	ImportID *string
	// Wording is what the form showed, as JSON; for an import, its
	// list_source, list_source_detail and file_name.
	Wording     json.RawMessage
	CreatedAt   time.Time
	ConfirmedAt *time.Time
}

// TagAdded is one of a contact's tags and when it was put on.
type TagAdded struct {
	ID      string
	Name    string
	AddedAt time.Time
}

// ImportRef is the import that added a contact.
type ImportRef struct {
	ID                  string
	FileName            *string
	CreatedAt           time.Time
	PermissionConfirmed bool
	// Held means the contact waits for the import's review (MG-34).
	Held bool
	// ListSource and ListSourceDetail are where the account said the list
	// came from (MG-63); nil for imports from before then.
	ListSource       *string
	ListSourceDetail *string
}

// ContactSkippedRow is a skipped import row with a contact's address.
type ContactSkippedRow struct {
	ImportID string
	FileName *string
	Row      int
	Reason   string
}

// skipMatches is the condition on import_skips s for rows whose email cell is
// the address $2, written in any case or with spaces around it.
const skipMatches = `s.import_id IN (SELECT id FROM imports WHERE account_id = $1) AND lower(btrim(s.email)) = $2`

// Data returns everything stored about one contact.
func (s *Service) Data(ctx context.Context, accountID, id string) (*ContactData, error) {
	c, err := s.Get(ctx, accountID, id)
	if err != nil {
		return nil, err
	}
	d := &ContactData{Contact: c, Tags: []TagAdded{}, SkippedRows: []ContactSkippedRow{}, Consents: []Consent{}}
	rows, err := s.DB.Query(ctx, `
		SELECT t.id, t.name, ct.created_at FROM contact_tags ct JOIN tags t ON t.id = ct.tag_id
		WHERE ct.contact_id = $1 ORDER BY lower(t.name), t.id`, c.ID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var t TagAdded
		if err := rows.Scan(&t.ID, &t.Name, &t.AddedAt); err != nil {
			rows.Close()
			return nil, err
		}
		d.Tags = append(d.Tags, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var im ImportRef
	err = s.DB.QueryRow(ctx, `
		SELECT i.id, i.file_name, i.created_at, i.permission_confirmed, c.held_import_id IS NOT NULL, i.list_source, i.list_source_detail
		FROM contacts c JOIN imports i ON i.id = c.import_id WHERE c.id = $1`, c.ID).
		Scan(&im.ID, &im.FileName, &im.CreatedAt, &im.PermissionConfirmed, &im.Held, &im.ListSource, &im.ListSourceDetail)
	switch {
	case err == nil:
		d.Import = &im
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, err
	}

	rows, err = s.DB.Query(ctx, `
		SELECT s.import_id, i.file_name, s.row_number, s.reason FROM import_skips s JOIN imports i ON i.id = s.import_id
		WHERE `+skipMatches+` ORDER BY i.created_at, s.import_id, s.row_number`, accountID, c.Email)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var r ContactSkippedRow
		if err := rows.Scan(&r.ImportID, &r.FileName, &r.Row, &r.Reason); err != nil {
			rows.Close()
			return nil, err
		}
		d.SkippedRows = append(d.SkippedRows, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = s.DB.Query(ctx, `
		SELECT id, form_id, source, ip, import_id, wording, created_at, confirmed_at FROM consents
		WHERE contact_id = $1 ORDER BY created_at, id`, c.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k Consent
		if err := rows.Scan(&k.ID, &k.FormID, &k.Source, &k.IP, &k.ImportID, &k.Wording, &k.CreatedAt, &k.ConfirmedAt); err != nil {
			return nil, err
		}
		d.Consents = append(d.Consents, k)
	}
	return d, rows.Err()
}

// Erase deletes a contact and every trace of its address, for a data request
// (CMP-9): the contact, its tags and consent records, the address in import
// skip reports and its hash among the account's imports. Only
// a hash of the address stays, on the suppression list, so the address isn't
// added back and mailed by mistake: with the contact's status if it
// unsubscribed, bounced or complained, and as unsubscribed otherwise.
func (s *Service) Erase(ctx context.Context, accountID, id string) error {
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
	reason := StatusUnsubscribed
	if Suppressing(status) {
		reason = status
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO suppressions (account_id, email_hash, reason, created_at) VALUES ($1, $2, $3, $4)
		ON CONFLICT (account_id, email_hash) DO UPDATE SET reason = EXCLUDED.reason, created_at = EXCLUDED.created_at`,
		accountID, suppressionHash(accountID, email), reason, s.now()); err != nil {
		return err
	}
	// The skip report keeps the row and its reason, without the address.
	if _, err := tx.Exec(ctx, `UPDATE import_skips s SET email = '' WHERE `+skipMatches, accountID, email); err != nil {
		return err
	}
	// So does the hash kept to compare imports across accounts (MG-63).
	if _, err := tx.Exec(ctx, `DELETE FROM import_address_hashes WHERE account_id = $1 AND hash = $2`, accountID, importHash(email)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
