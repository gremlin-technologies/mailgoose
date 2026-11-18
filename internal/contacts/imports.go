package contacts

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/gremlin-technologies/mailgoose/internal/auth"
)

// CSV import (CON-5, CON-6). The API stores the file; the worker imports it
// in batches with RunImports.
const (
	// MaxImportBytes is the largest CSV file an import takes.
	MaxImportBytes = 10 << 20
	// MaxImportRows is the most contact rows one import takes.
	MaxImportRows = 100_000
	// MaxImportTags is the most tags one import puts on its contacts.
	MaxImportTags = 20
	// LargeFirstImport is the most rows an account's first import may have
	// without being held for review.
	LargeFirstImport = 2_000
	// ImportLease is how long a worker holds an import between batches
	// before another worker may take it over.
	ImportLease = 2 * time.Minute

	importBatch       = 1_000
	maxImportAttempts = 3
	maxFileNameLen    = 255
)

// Import statuses.
const (
	ImportQueued  = "queued"
	ImportRunning = "running"
	ImportDone    = "done"
	ImportFailed  = "failed"
)

// Review states. A held import's contacts aren't mailed until an admin
// approves it (MG-34).
const (
	ReviewNone     = "none"
	ReviewHeld     = "held"
	ReviewApproved = "approved"
)

// Why an import is held.
const (
	HoldLargeFirstImport = "large_first_import"
	HoldOverContactLimit = "over_contact_limit"
)

// Why a row was skipped.
const (
	SkipInvalidEmail = "invalid_email"
	SkipInvalidRow   = "invalid_row"
	SkipDuplicate    = "duplicate"
	SkipExists       = "exists"
	SkipSuppressed   = "suppressed"
	SkipOverLimit    = "over_limit"
)

// SkipReasons lists every reason a row can be skipped for.
var SkipReasons = []string{SkipInvalidEmail, SkipInvalidRow, SkipDuplicate, SkipExists, SkipSuppressed, SkipOverLimit}

// ErrImportNotFound means no import with that id in the account.
var ErrImportNotFound = errors.New("import not found")

// errLeaseLost means another worker took the import over.
var errLeaseLost = errors.New("import lease lost")

// Import is one CSV import and how far it got.
type Import struct {
	ID                  string
	AccountID           string
	FileName            *string
	Status              string
	PermissionConfirmed bool
	// The header of the column each field came from; nil when the file has
	// no such column.
	EmailColumn     string
	FirstNameColumn *string
	LastNameColumn  *string
	// Tags are put on every contact the import adds, sorted by name. A tag
	// deleted since is left out.
	Tags          []TagRef
	TotalRows     int
	ProcessedRows int
	Imported      int
	Skipped       int
	// SkippedByReason counts skipped rows by reason, with every reason in
	// SkipReasons present.
	SkippedByReason map[string]int
	Review          string
	HoldReason      *string
	Error           *string
	CreatedAt       time.Time
	StartedAt       *time.Time
	FinishedAt      *time.Time
}

// ContactStatus is the status the import gives the contacts it adds.
func (im *Import) ContactStatus() string {
	if im.PermissionConfirmed {
		return StatusSubscribed
	}
	return StatusUnconfirmed
}

// SkippedRow is a row an import didn't add.
type SkippedRow struct {
	// Row is the file's line number where the row starts; the header is 1.
	Row int
	// Email is the email cell as written, empty when the row couldn't be
	// read.
	Email  string
	Reason string
}

// ImportInput is a new import.
type ImportInput struct {
	CSV      []byte
	FileName string
	// EmailColumn, FirstNameColumn and LastNameColumn name the header of each
	// field's column. Empty ones are found from common headers.
	EmailColumn     string
	FirstNameColumn string
	LastNameColumn  string
	// Tags are tag names or ids, put on every contact the import adds.
	Tags []string
	// PermissionConfirmed says the account has the people's permission to
	// mail them: contacts are added subscribed, not unconfirmed.
	PermissionConfirmed bool
}

func invalidImport(code, field, msg string) error {
	return &auth.ValidationError{Code: code, Field: field, Message: msg}
}

// --- Reading the file ---

var utf8BOM = []byte("\xef\xbb\xbf")

// csvLayout is how to read a file: its delimiter and each field's column
// (-1 when absent).
type csvLayout struct {
	comma               rune
	header              []string
	email, first, last  int
	emailName           string
	firstName, lastName *string
}

// sniffComma picks the delimiter that appears most in the header line:
// comma, semicolon (spreadsheets in many locales) or tab.
func sniffComma(data []byte) rune {
	line := data
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		line = data[:i]
	}
	best, n := byte(','), bytes.Count(line, []byte{','})
	for _, c := range []byte{';', '\t'} {
		if m := bytes.Count(line, []byte{c}); m > n {
			best, n = c, m
		}
	}
	return rune(best)
}

func newCSVReader(data []byte, comma rune) *csv.Reader {
	r := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(data, utf8BOM)))
	r.Comma = comma
	r.FieldsPerRecord = -1
	r.ReuseRecord = true
	return r
}

// headerKey is a header compared loosely: lower case, without spaces,
// underscores, dashes or dots.
func headerKey(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '_', '-', '.':
			return -1
		}
		return r
	}, strings.ToLower(strings.TrimSpace(s)))
}

// commonHeaders are the headers each field is found under when no column is
// named for it, compared with headerKey.
var commonHeaders = map[string][]string{
	"email":      {"email", "emailaddress", "mail", "mailaddress", "subscriberemail", "contactemail"},
	"first_name": {"firstname", "first", "givenname", "fname", "forename"},
	"last_name":  {"lastname", "last", "surname", "familyname", "lname"},
}

// findColumn returns the column headed name: an exact match first, then a
// loose one. It returns -1 when there's none.
func findColumn(header []string, name string) int {
	for i, h := range header {
		if strings.TrimSpace(h) == strings.TrimSpace(name) {
			return i
		}
	}
	for i, h := range header {
		if headerKey(h) == headerKey(name) {
			return i
		}
	}
	return -1
}

// readLayout reads the header row and finds each field's column. Named
// columns must exist; email must be found.
func readLayout(data []byte, emailCol, firstCol, lastCol string) (*csvLayout, error) {
	if len(bytes.TrimSpace(bytes.TrimPrefix(data, utf8BOM))) == 0 {
		return nil, invalidImport("no_rows", "csv", "The file is empty.")
	}
	if !utf8.Valid(data) {
		return nil, invalidImport("invalid_encoding", "csv", "The file isn't UTF-8 text. Save it from your spreadsheet as \"CSV UTF-8\" and try again.")
	}
	l := &csvLayout{comma: sniffComma(bytes.TrimPrefix(data, utf8BOM))}
	header, err := newCSVReader(data, l.comma).Read()
	if err != nil {
		return nil, invalidImport("invalid_csv", "csv", "The header row can't be read as CSV: "+err.Error())
	}
	l.header = slices.Clone(header)

	find := func(field, named string) (int, error) {
		if named != "" {
			i := findColumn(l.header, named)
			if i < 0 {
				return -1, invalidImport("invalid_mapping", "columns."+field,
					fmt.Sprintf("No column is headed %q. The headers are: %s.", named, strings.Join(l.header, ", ")))
			}
			return i, nil
		}
		for _, want := range commonHeaders[field] {
			for i, h := range l.header {
				if headerKey(h) == want {
					return i, nil
				}
			}
		}
		return -1, nil
	}
	if l.email, err = find("email", emailCol); err != nil {
		return nil, err
	}
	if l.email < 0 {
		return nil, invalidImport("no_email_column", "columns.email",
			fmt.Sprintf("No column looks like email addresses. The headers are: %s. Name the email column, or add a header row if the file has none.", strings.Join(l.header, ", ")))
	}
	if l.first, err = find("first_name", firstCol); err != nil {
		return nil, err
	}
	if l.last, err = find("last_name", lastCol); err != nil {
		return nil, err
	}
	if (l.first >= 0 && (l.first == l.email || l.first == l.last)) || (l.last >= 0 && l.last == l.email) {
		return nil, invalidImport("invalid_mapping", "columns", "Each field needs its own column.")
	}
	l.emailName = strings.TrimSpace(l.header[l.email])
	if l.first >= 0 {
		n := strings.TrimSpace(l.header[l.first])
		l.firstName = &n
	}
	if l.last >= 0 {
		n := strings.TrimSpace(l.header[l.last])
		l.lastName = &n
	}
	return l, nil
}

// csvRow is one contact row of a file.
type csvRow struct {
	// index counts contact rows from 0, the way processed_rows does.
	index int
	// line is where the row starts in the file; the header is line 1.
	line               int
	email, first, last string
	unreadable         bool
}

func cell(rec []string, i int) string {
	if i < 0 || i >= len(rec) {
		return ""
	}
	return strings.TrimSpace(rec[i])
}

func blank(rec []string) bool {
	for _, f := range rec {
		if strings.TrimSpace(f) != "" {
			return false
		}
	}
	return true
}

// eachRow calls fn with every contact row after the header, in order. Blank
// rows are left out. A row with a stray quote is passed as unreadable; a
// quote that never closes, which would swallow the rest of the file, is an
// error.
func (l *csvLayout) eachRow(data []byte, fn func(csvRow) error) error {
	r := newCSVReader(data, l.comma)
	if _, err := r.Read(); err != nil {
		return err
	}
	for i := 0; ; {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			return nil
		}
		var row csvRow
		var pe *csv.ParseError
		switch {
		case errors.As(err, &pe):
			if errors.Is(pe.Err, csv.ErrQuote) && pe.Line > pe.StartLine {
				return invalidImport("invalid_csv", "csv",
					fmt.Sprintf("Row %d opens a quote that doesn't close, so the rest of the file can't be read. Fix that row and try again.", pe.StartLine))
			}
			row = csvRow{line: pe.StartLine, unreadable: true}
		case err != nil:
			return err
		case blank(rec):
			continue
		default:
			line, _ := r.FieldPos(0)
			row = csvRow{line: line, email: cell(rec, l.email), first: cell(rec, l.first), last: cell(rec, l.last)}
		}
		row.index = i
		i++
		if err := fn(row); err != nil {
			return err
		}
	}
}

// clip cuts s to at most n bytes without splitting a character.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// importName is a name cell as a contact name: line breaks become spaces and
// it's cut to MaxNameLen characters, so a long name never costs a contact.
func importName(v string) *string {
	v = strings.Join(strings.Fields(v), " ")
	if v == "" {
		return nil
	}
	if r := []rune(v); len(r) > MaxNameLen {
		v = strings.TrimSpace(string(r[:MaxNameLen]))
	}
	return &v
}

// --- Creating and reading imports ---

const importColumns = `i.id, i.account_id, i.file_name, i.status, i.permission_confirmed, i.email_column, i.first_name_column, i.last_name_column,
	i.total_rows, i.processed_rows, i.imported, i.skipped, i.review, i.hold_reason, i.error, i.created_at, i.started_at, i.finished_at,
	ARRAY(SELECT t.id::text FROM tags t WHERE t.id = ANY(i.tag_ids) ORDER BY lower(t.name), t.id),
	ARRAY(SELECT t.name FROM tags t WHERE t.id = ANY(i.tag_ids) ORDER BY lower(t.name), t.id),
	ARRAY(SELECT s.reason || ':' || count(*) FROM import_skips s WHERE s.import_id = i.id GROUP BY s.reason)`

func scanImport(row pgx.Row) (*Import, error) {
	var im Import
	var tagIDs, tagNames, byReason []string
	err := row.Scan(&im.ID, &im.AccountID, &im.FileName, &im.Status, &im.PermissionConfirmed, &im.EmailColumn, &im.FirstNameColumn, &im.LastNameColumn,
		&im.TotalRows, &im.ProcessedRows, &im.Imported, &im.Skipped, &im.Review, &im.HoldReason, &im.Error, &im.CreatedAt, &im.StartedAt, &im.FinishedAt,
		&tagIDs, &tagNames, &byReason)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrImportNotFound
	}
	if err != nil {
		return nil, err
	}
	im.Tags = make([]TagRef, len(tagIDs))
	for i := range tagIDs {
		im.Tags[i] = TagRef{ID: tagIDs[i], Name: tagNames[i]}
	}
	im.SkippedByReason = make(map[string]int, len(SkipReasons))
	for _, r := range SkipReasons {
		im.SkippedByReason[r] = 0
	}
	for _, rc := range byReason {
		reason, n, _ := strings.Cut(rc, ":")
		im.SkippedByReason[reason], _ = strconv.Atoi(n)
	}
	return &im, nil
}

// resolveTags turns tag names or ids into the account's tag ids, in order and
// without repeats.
func (s *Service) resolveTags(ctx context.Context, accountID string, refs []string) ([]string, error) {
	ids := []string{}
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		var id string
		err := s.DB.QueryRow(ctx, `
			SELECT id FROM tags WHERE account_id = $1 AND (id::text = $2 OR lower(name) = lower($2))
			ORDER BY (id::text = $2) DESC LIMIT 1`, accountID, ref).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, invalidImport("tag_not_found", "tags", fmt.Sprintf("This account has no tag %q. Create it first.", ref))
		}
		if err != nil {
			return nil, err
		}
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	if len(ids) > MaxImportTags {
		return nil, invalidImport("too_many_tags", "tags", fmt.Sprintf("An import puts at most %d tags on its contacts.", MaxImportTags))
	}
	return ids, nil
}

// CreateImport checks the file and queues it for the worker. An account's
// first import of more than LargeFirstImport rows is held for review.
func (s *Service) CreateImport(ctx context.Context, accountID string, in ImportInput) (*Import, error) {
	if len(in.CSV) > MaxImportBytes {
		return nil, invalidImport("file_too_large", "csv", fmt.Sprintf("The file is over %d MB. Split it into smaller files.", MaxImportBytes>>20))
	}
	l, err := readLayout(in.CSV, in.EmailColumn, in.FirstNameColumn, in.LastNameColumn)
	if err != nil {
		return nil, err
	}
	total := 0
	err = l.eachRow(in.CSV, func(csvRow) error {
		total++
		if total > MaxImportRows {
			return invalidImport("too_many_rows", "csv", fmt.Sprintf("The file has more than %d contacts. Split it into smaller files.", MaxImportRows))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if total == 0 {
		return nil, invalidImport("no_rows", "csv", "The file has a header row but no contacts.")
	}
	tagIDs, err := s.resolveTags(ctx, accountID, in.Tags)
	if err != nil {
		return nil, err
	}
	var fileName *string
	if n := strings.Join(strings.Fields(in.FileName), " "); n != "" {
		n = clip(n, maxFileNameLen)
		fileName = &n
	}

	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Lock the account so two first imports can't both count as first.
	var first bool
	if err := tx.QueryRow(ctx, `
		SELECT NOT EXISTS (SELECT 1 FROM imports WHERE account_id = a.id AND status <> 'failed')
		FROM accounts a WHERE a.id = $1 FOR UPDATE`, accountID).Scan(&first); err != nil {
		return nil, fmt.Errorf("lock account: %w", err)
	}
	review, holdReason := ReviewNone, (*string)(nil)
	if first && total > LargeFirstImport {
		review, holdReason = ReviewHeld, ptr(HoldLargeFirstImport)
	}
	var id string
	if err := tx.QueryRow(ctx, `
		INSERT INTO imports (account_id, file_name, permission_confirmed, email_column, first_name_column, last_name_column,
			tag_ids, csv, total_rows, review, hold_reason, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) RETURNING id`,
		accountID, fileName, in.PermissionConfirmed, l.emailName, l.firstName, l.lastName,
		tagIDs, in.CSV, total, review, holdReason, s.now()).Scan(&id); err != nil {
		return nil, err
	}
	im, err := scanImport(tx.QueryRow(ctx, `SELECT `+importColumns+` FROM imports i WHERE i.id = $1`, id))
	if err != nil {
		return nil, err
	}
	return im, tx.Commit(ctx)
}

func ptr[T any](v T) *T { return &v }

// GetImport returns one import of the account.
func (s *Service) GetImport(ctx context.Context, accountID, id string) (*Import, error) {
	return scanImport(s.DB.QueryRow(ctx, `SELECT `+importColumns+` FROM imports i WHERE i.account_id = $1 AND i.id = $2`, accountID, id))
}

// ListImports returns the account's imports, newest first, after the cursor.
func (s *Service) ListImports(ctx context.Context, accountID string, after *Cursor, limit int) ([]*Import, error) {
	q := `SELECT ` + importColumns + ` FROM imports i WHERE i.account_id = $1`
	args := []any{accountID}
	if after != nil {
		args = append(args, after.CreatedAt, after.ID)
		q += ` AND (i.created_at, i.id) < ($2, $3::uuid)`
	}
	args = append(args, limit)
	q += fmt.Sprintf(` ORDER BY i.created_at DESC, i.id DESC LIMIT $%d`, len(args))
	rows, err := s.DB.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Import
	for rows.Next() {
		im, err := scanImport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, im)
	}
	return out, rows.Err()
}

// ImportCursor is the cursor just after im in ListImports.
func ImportCursor(im *Import) Cursor { return Cursor{CreatedAt: im.CreatedAt, ID: im.ID} }

// ListSkippedRows returns an import's skipped rows in file order, after the
// file line afterRow (0 for the first page).
func (s *Service) ListSkippedRows(ctx context.Context, accountID, importID string, afterRow, limit int) ([]SkippedRow, error) {
	var exists bool
	if err := s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM imports WHERE account_id = $1 AND id = $2)`, accountID, importID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrImportNotFound
	}
	rows, err := s.DB.Query(ctx, `
		SELECT row_number, email, reason FROM import_skips WHERE import_id = $1 AND row_number > $2
		ORDER BY row_number LIMIT $3`, importID, afterRow, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SkippedRow{}
	for rows.Next() {
		var sr SkippedRow
		if err := rows.Scan(&sr.Row, &sr.Email, &sr.Reason); err != nil {
			return nil, err
		}
		out = append(out, sr)
	}
	return out, rows.Err()
}

// --- Running imports ---

// RunImports runs waiting imports until ctx ends, looking for new ones every
// poll when there's nothing to do. An import that fails part way is picked up
// again once its lease runs out.
func (s *Service) RunImports(ctx context.Context, log *slog.Logger, poll time.Duration) {
	for ctx.Err() == nil {
		ran, err := s.RunNextImport(ctx)
		if err != nil && ctx.Err() == nil {
			log.ErrorContext(ctx, "import stopped; it will be retried", "err", err)
		}
		if ran && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
		case <-time.After(poll):
		}
	}
}

// importJob is a claimed import.
type importJob struct {
	id, accountID, token string
	data                 []byte
	emailCol             string
	firstCol, lastCol    *string
	tagIDs               []string
	status               string
	processed            int
	review               string
}

// RunNextImport claims the oldest waiting import, or one whose worker
// stopped, and runs it to the end. It reports whether there was one.
func (s *Service) RunNextImport(ctx context.Context) (bool, error) {
	now := s.now()
	// An import whose worker keeps stopping fails, so it can't loop forever.
	if _, err := s.DB.Exec(ctx, `
		UPDATE imports SET status = 'failed', finished_at = $1, csv = NULL, lease_token = NULL, lease_until = NULL,
			error = 'The import stopped before finishing three times. Try again, or split the file into smaller ones.'
		WHERE status = 'running' AND lease_until < $1 AND attempts >= $2`, now, maxImportAttempts); err != nil {
		return false, err
	}
	var j importJob
	var permission bool
	err := s.DB.QueryRow(ctx, `
		UPDATE imports SET status = 'running', lease_token = gen_random_uuid(), lease_until = $2,
			attempts = attempts + 1, started_at = coalesce(started_at, $1)
		WHERE id = (
			SELECT id FROM imports
			WHERE status = 'queued' OR (status = 'running' AND lease_until < $1)
			ORDER BY created_at, id LIMIT 1 FOR UPDATE SKIP LOCKED)
		RETURNING id, account_id, lease_token, csv, email_column, first_name_column, last_name_column, tag_ids::text[],
			permission_confirmed, processed_rows, review`,
		now, now.Add(ImportLease)).Scan(&j.id, &j.accountID, &j.token, &j.data, &j.emailCol, &j.firstCol, &j.lastCol, &j.tagIDs,
		&permission, &j.processed, &j.review)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim import: %w", err)
	}
	j.status = StatusUnconfirmed
	if permission {
		j.status = StatusSubscribed
	}
	if err := s.runImport(ctx, &j); err != nil {
		return true, fmt.Errorf("import %s: %w", j.id, err)
	}
	return true, nil
}

func (s *Service) runImport(ctx context.Context, j *importJob) error {
	l, err := readLayout(j.data, j.emailCol, deref(j.firstCol), deref(j.lastCol))
	if err != nil {
		return err
	}
	if j.processed == 0 && j.review == ReviewNone {
		over, err := s.wouldPassLimit(ctx, j, l)
		if err != nil {
			return err
		}
		if over {
			if err := s.leased(ctx, nil, j, `UPDATE imports SET review = 'held', hold_reason = 'over_contact_limit'`); err != nil {
				return err
			}
			j.review = ReviewHeld
		}
	}

	seen := map[string]struct{}{}
	var batch []csvRow
	err = l.eachRow(j.data, func(r csvRow) error {
		if r.index < j.processed {
			// Imported before a restart: only remember it for duplicates.
			if e, err := NormalizeEmail(r.email); err == nil && !r.unreadable {
				seen[e] = struct{}{}
			}
			return nil
		}
		batch = append(batch, r)
		if len(batch) < importBatch {
			return nil
		}
		err := s.importBatch(ctx, j, batch, seen)
		batch = batch[:0]
		return err
	})
	if err == nil && len(batch) > 0 {
		err = s.importBatch(ctx, j, batch, seen)
	}
	if err != nil {
		return err
	}
	return s.leased(ctx, nil, j, `UPDATE imports SET status = 'done', finished_at = $3, csv = NULL, lease_token = NULL, lease_until = NULL`, s.now())
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// leased runs update, which must end before its WHERE, on j's row while j
// still holds the lease; $1 and $2 are the id and lease token, then args.
func (s *Service) leased(ctx context.Context, tx pgx.Tx, j *importJob, update string, args ...any) error {
	q := update + ` WHERE id = $1 AND lease_token = $2`
	args = append([]any{j.id, j.token}, args...)
	var tag interface{ RowsAffected() int64 }
	var err error
	if tx != nil {
		tag, err = tx.Exec(ctx, q, args...)
	} else {
		tag, err = s.DB.Exec(ctx, q, args...)
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errLeaseLost
	}
	return nil
}

// wouldPassLimit reports whether a free account would go over its contact
// limit if every new address in the file were added.
func (s *Service) wouldPassLimit(ctx context.Context, j *importJob, l *csvLayout) (bool, error) {
	var plan string
	if err := s.DB.QueryRow(ctx, `SELECT plan FROM accounts WHERE id = $1`, j.accountID).Scan(&plan); err != nil {
		return false, err
	}
	if plan != "free" {
		return false, nil
	}
	seen := map[string]struct{}{}
	var emails, hashes []string
	if err := l.eachRow(j.data, func(r csvRow) error {
		if e, err := NormalizeEmail(r.email); err == nil && !r.unreadable {
			if _, dup := seen[e]; !dup {
				seen[e] = struct{}{}
				emails = append(emails, e)
				hashes = append(hashes, suppressionHash(j.accountID, e))
			}
		}
		return nil
	}); err != nil {
		return false, err
	}
	var adding, counted int
	if err := s.DB.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM unnest($2::text[], $3::text[]) AS e(email, hash)
			 WHERE NOT EXISTS (SELECT 1 FROM contacts c WHERE c.account_id = $1 AND c.email = e.email)
			   AND NOT EXISTS (SELECT 1 FROM suppressions s WHERE s.account_id = $1 AND s.email_hash = e.hash)),
			(SELECT count(*) FROM contacts WHERE account_id = $1 AND status IN ('unconfirmed', 'subscribed'))`,
		j.accountID, emails, hashes).Scan(&adding, &counted); err != nil {
		return false, err
	}
	return counted+adding > FreeContactLimit, nil
}

type candidate struct {
	line        int
	raw, email  string
	first, last *string
}

// importBatch adds one batch of rows in one transaction and records how far
// the import got, so a restart carries on after it.
func (s *Service) importBatch(ctx context.Context, j *importJob, rows []csvRow, seen map[string]struct{}) error {
	var skips []SkippedRow
	skip := func(line int, email, reason string) {
		skips = append(skips, SkippedRow{Row: line, Email: clip(email, 254), Reason: reason})
	}
	var cands []candidate
	for _, r := range rows {
		if r.unreadable {
			skip(r.line, "", SkipInvalidRow)
			continue
		}
		e, err := NormalizeEmail(r.email)
		if err != nil {
			skip(r.line, r.email, SkipInvalidEmail)
			continue
		}
		if _, dup := seen[e]; dup {
			skip(r.line, r.email, SkipDuplicate)
			continue
		}
		seen[e] = struct{}{}
		cands = append(cands, candidate{line: r.line, raw: r.email, email: e, first: importName(r.first), last: importName(r.last)})
	}

	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	count, capped, err := lockedCount(ctx, tx, j.accountID)
	if err != nil {
		return err
	}
	emails := make([]string, len(cands))
	hashes := make([]string, len(cands))
	for i, c := range cands {
		emails[i], hashes[i] = c.email, suppressionHash(j.accountID, c.email)
	}
	existing, err := collect(ctx, tx, `SELECT email FROM contacts WHERE account_id = $1 AND email = ANY($2)`, j.accountID, emails)
	if err != nil {
		return err
	}
	suppressed, err := collect(ctx, tx, `SELECT email_hash FROM suppressions WHERE account_id = $1 AND email_hash = ANY($2)`, j.accountID, hashes)
	if err != nil {
		return err
	}
	var add []candidate
	for i, c := range cands {
		switch {
		case existing[c.email]:
			skip(c.line, c.raw, SkipExists)
		case suppressed[hashes[i]]:
			skip(c.line, c.raw, SkipSuppressed)
		case capped && count+len(add) >= FreeContactLimit:
			skip(c.line, c.raw, SkipOverLimit)
		default:
			add = append(add, c)
		}
	}

	var held *string
	if j.review == ReviewHeld {
		held = &j.id
	}
	addEmails := make([]string, len(add))
	firsts := make([]*string, len(add))
	lasts := make([]*string, len(add))
	for i, c := range add {
		addEmails[i], firsts[i], lasts[i] = c.email, c.first, c.last
	}
	now := s.now()
	rows2, err := tx.Query(ctx, `
		INSERT INTO contacts (account_id, email, first_name, last_name, status, source, created_at, updated_at, status_changed_at, held_import_id)
		SELECT $1, x.email, x.first_name, x.last_name, $5, 'import', $6, $6, $6, $7
		FROM unnest($2::text[], $3::text[], $4::text[]) WITH ORDINALITY AS x(email, first_name, last_name, n)
		ORDER BY x.n
		ON CONFLICT (account_id, email) DO NOTHING
		RETURNING id::text, email`, j.accountID, addEmails, firsts, lasts, j.status, now, held)
	if err != nil {
		return err
	}
	var ids []string
	added := map[string]bool{}
	for rows2.Next() {
		var id, email string
		if err := rows2.Scan(&id, &email); err != nil {
			rows2.Close()
			return err
		}
		ids = append(ids, id)
		added[email] = true
	}
	rows2.Close()
	if err := rows2.Err(); err != nil {
		return err
	}
	for _, c := range add {
		if !added[c.email] {
			// Added by someone else since the check above.
			skip(c.line, c.raw, SkipExists)
		}
	}
	if len(ids) > 0 && len(j.tagIDs) > 0 {
		// Tags deleted since the import was created are left out.
		if _, err := tx.Exec(ctx, `
			INSERT INTO contact_tags (contact_id, tag_id, created_at)
			SELECT c.id, t.id, $4 FROM unnest($1::uuid[]) AS c(id)
			CROSS JOIN tags t WHERE t.account_id = $2 AND t.id = ANY($3::uuid[])
			ON CONFLICT DO NOTHING`, ids, j.accountID, j.tagIDs, now); err != nil {
			return err
		}
	}
	if len(skips) > 0 {
		lines := make([]int, len(skips))
		skipEmails := make([]string, len(skips))
		reasons := make([]string, len(skips))
		for i, sk := range skips {
			lines[i], skipEmails[i], reasons[i] = sk.Row, sk.Email, sk.Reason
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO import_skips (import_id, row_number, email, reason)
			SELECT $1, x.row_number, x.email, x.reason FROM unnest($2::int[], $3::text[], $4::text[]) AS x(row_number, email, reason)
			ON CONFLICT DO NOTHING`, j.id, lines, skipEmails, reasons); err != nil {
			return err
		}
	}
	processed := rows[len(rows)-1].index + 1
	if err := s.leased(ctx, tx, j, `
		UPDATE imports SET processed_rows = $3, imported = imported + $4, skipped = skipped + $5, lease_until = $6`,
		processed, len(ids), len(skips), now.Add(ImportLease)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	j.processed = processed
	if afterImportBatch != nil {
		return afterImportBatch(processed)
	}
	return nil
}

// afterImportBatch, when set by a test, runs after each batch commits; an
// error stops the import there, as a crash would.
var afterImportBatch func(processed int) error

// collect runs a query of one text column and returns its values as a set.
func collect(ctx context.Context, tx pgx.Tx, q string, args ...any) (map[string]bool, error) {
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out[v] = true
	}
	return out, rows.Err()
}
