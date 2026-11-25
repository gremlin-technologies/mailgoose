package contacts

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
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
	// MaxImportFileTags is the most different tags a file's tags column may
	// name; tags it names that the account lacks are created.
	MaxImportFileTags = 200
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
var SkipReasons = []string{SkipInvalidEmail, SkipInvalidRow, SkipDuplicate, SkipExists, SkipSuppressed, SkipOverLimit,
	SkipNotSubscribed, SkipArchived}

// ErrImportNotFound means no import with that id in the account.
var ErrImportNotFound = errors.New("import not found")

// errLeaseLost means another worker took the import over.
var errLeaseLost = errors.New("import lease lost")

// Import is one CSV import and how far it got.
type Import struct {
	ID        string
	AccountID string
	// FileName is nil for pasted contacts (MG-125).
	FileName *string
	// HasHeader is false when the file's first row is a contact, not a
	// header (MG-125).
	HasHeader           bool
	Status              string
	PermissionConfirmed bool
	// The header of the column each field came from; nil when the file has
	// no such column.
	EmailColumn     string
	FirstNameColumn *string
	LastNameColumn  *string
	StatusColumn    *string
	TagsColumn      *string
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
	// ListSource is where the customer said the list came from, and
	// ListSourceDetail their words for "other"; nil before MG-63.
	ListSource       *string
	ListSourceDetail *string
	// Checks is what the import's checks found (MG-63).
	Checks     CheckCounts
	Error      *string
	CreatedAt  time.Time
	StartedAt  *time.Time
	FinishedAt *time.Time
	// Kind is ImportKindCSV or ImportKindMailchimp.
	Kind string
	// ExistingTagged counts addresses of a Mailchimp import that were
	// already contacts and only gained tags, and ExistingSuppressed those
	// of them that the import took off the mailing list. UnlistedSkipped
	// counts skipped rows that aren't listed one by one, and are counted
	// as invalid_row in SkippedByReason.
	ExistingTagged     int
	ExistingSuppressed int
	UnlistedSkipped    int
	// Mailchimp is the bundle's summary, for a Mailchimp import.
	Mailchimp *MailchimpSummary
	// FieldColumns are the contact fields the import fills and the column
	// each comes from, in the fields' order (MG-131). A field deleted since
	// is left out.
	FieldColumns []ImportFieldColumn
	// ExistingUpdated counts contacts already in the account whose contact
	// field values the import changed.
	ExistingUpdated int
}

// ImportFieldColumn is a contact field an import fills, and its column.
type ImportFieldColumn struct {
	ID, Key, Name string
	Column        string
	// Created says the import made the field.
	Created bool
}

// storedFieldColumn is how an import keeps a contact field's column.
type storedFieldColumn struct {
	FieldID string `json:"field_id"`
	Column  string `json:"column"`
	Created bool   `json:"created,omitempty"`
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
	// Row is the file's line number where the row starts; the first line,
	// the header if there is one, is 1.
	Row int
	// Email is the email cell as written, empty when the row couldn't be
	// read.
	Email  string
	Reason string
	// Source is where a Mailchimp import's row came from: the export file
	// and its row there. Empty for a CSV import.
	Source string
}

// ImportInput is a new import.
type ImportInput struct {
	CSV      []byte
	FileName string
	// NoHeader says the first row is a contact, not a header, as when
	// contacts are pasted from a spreadsheet (MG-125). Columns are then
	// named by position ("#1", "#2" ...), and the email column is found
	// from the first row's addresses when it isn't named.
	NoHeader bool
	// EmailColumn, FirstNameColumn, LastNameColumn, StatusColumn and
	// TagsColumn name the header of each field's column. Empty ones are found
	// from common headers. A status cell can only hold a contact back: an
	// unsubscribed, bounced or complained row is added with that status, and
	// an unconfirmed one stays unconfirmed. A tags cell lists tags separated
	// by commas; tags the account doesn't have yet are created.
	//
	// A column is named by its header, or by its position as "#3" (counting
	// from 1) when no header is exactly "#3"; a blank or repeated header can
	// only be named by position.
	EmailColumn     string
	FirstNameColumn string
	LastNameColumn  string
	StatusColumn    string
	TagsColumn      string
	// SkipFields are fields not to import, even when a column's header
	// looks like one (MG-124). Email can't be skipped.
	SkipFields []string
	// FieldColumns names the column each of the account's contact fields
	// comes from (MG-131), keyed by the field's key or id; an empty column
	// leaves that field out even when a header matches it. Fields left out
	// of the map are found from a column whose header matches the field's
	// name or key, compared loosely.
	FieldColumns map[string]string
	// NewFields are contact fields to make from columns when the import is
	// created, and to fill from them.
	NewFields []NewImportField
	// Tags are tag names or ids, put on every contact the import adds.
	Tags []string
	// PermissionConfirmed says the account has the people's permission to
	// mail them: contacts are added subscribed, not unconfirmed.
	PermissionConfirmed bool
	// ListSource is where the people came from, one of ListSources;
	// required. ListSourceDetail describes an "other" source.
	ListSource       string
	ListSourceDetail string
}

// NewImportField is a contact field an import makes from one of the file's
// columns (MG-131).
type NewImportField struct {
	// Column names the column as the other columns are named.
	Column string
	// Name is the new field's name; empty means the column's header.
	Name string
}

// choice is how the import picks its columns: the ones named, none for a
// skipped field, and common headers for the rest. af is the account's
// contact fields, which FieldColumns names and headers can match.
func (in ImportInput) choice(af AccountFields) columnChoice {
	skip := map[string]bool{}
	for _, f := range in.SkipFields {
		skip[f] = true
	}
	c := fieldChoice(af, in.FieldColumns, in.NewFields)
	c.named = map[string]string{fieldEmail: in.EmailColumn, fieldFirstName: in.FirstNameColumn, fieldLastName: in.LastNameColumn,
		fieldStatus: in.StatusColumn, fieldTags: in.TagsColumn}
	c.skip = skip
	return c
}

// fieldChoice is how an import picks its contact fields' columns (MG-131):
// the ones fieldColumns names by field key or id, guesses for the rest,
// and the new fields to make. af is the account's contact fields.
func fieldChoice(af AccountFields, fieldColumns map[string]string, newFields []NewImportField) columnChoice {
	c := columnChoice{
		guess:      true,
		fields:     af.Fields,
		fieldLimit: af.Limit,
		fieldNamed: map[string]string{},
		newFields:  newFields,
	}
	for _, ref := range slices.Sorted(maps.Keys(fieldColumns)) {
		r := strings.TrimSpace(ref)
		i := slices.IndexFunc(af.Fields, func(f *Field) bool { return f.Key == r || f.ID == r })
		if i < 0 {
			c.unknownFields = append(c.unknownFields, ref)
			continue
		}
		c.fieldNamed[af.Fields[i].ID] = strings.TrimSpace(fieldColumns[ref])
	}
	return c
}

// columnChoice is how to pick each field's column.
type columnChoice struct {
	// named maps a field to the column it comes from; empty when not named.
	named map[string]string
	// skip are fields not imported at all.
	skip map[string]bool
	// guess finds fields that aren't named or skipped from common headers,
	// and contact fields from headers matching their names or keys. A queued
	// import's columns were all picked when it was created, so the worker
	// doesn't guess again.
	guess bool
	// fields are the account's contact fields, and fieldLimit the most it
	// may have (MG-131).
	fields     []*Field
	fieldLimit int
	// fieldNamed maps a contact field's id to the column it comes from; ""
	// leaves the field out. A field missing from it is guessed.
	fieldNamed map[string]string
	// unknownFields are contact fields named that the account doesn't have.
	unknownFields []string
	// newFields are contact fields to make from columns.
	newFields []NewImportField
}

// newFieldCol is a contact field an import will make, and its column.
type newFieldCol struct {
	name, key string
	col       int
}

func invalidImport(code, field, msg string) error {
	return &auth.ValidationError{Code: code, Field: field, Message: msg}
}

// --- Reading the file ---

var utf8BOM = []byte("\xef\xbb\xbf")

// Import fields, the keys of a file's column mapping.
const (
	fieldEmail     = "email"
	fieldFirstName = "first_name"
	fieldLastName  = "last_name"
	fieldStatus    = "status"
	fieldTags      = "tags"
)

// importFields are the fields an import reads, in the order their columns
// are found.
var importFields = []string{fieldEmail, fieldFirstName, fieldLastName, fieldStatus, fieldTags}

// csvLayout is how to read a file: its delimiter and each field's column
// (-1 when absent).
type csvLayout struct {
	comma rune
	// hasHeader is false when the first row is a contact. header is then
	// all blank, so every column is named by position, and first holds
	// that row.
	hasHeader bool
	header    []string
	first     []string
	// col is each field's column index.
	col map[string]int
	// fieldCol is the column index of each contact field the import fills,
	// by the field's id, and newCols the fields it will make (MG-131).
	fieldCol map[string]int
	newCols  []newFieldCol
	// merge is true for a Mailchimp import's merge fields (MG-132), whose
	// "columns" are merge fields: messages name them so.
	merge bool
}

// column is how field's column is stored on the import, nil when the file
// has none.
func (l *csvLayout) column(field string) *string {
	i := l.col[field]
	if i < 0 {
		return nil
	}
	n := l.name(i)
	return &n
}

// name is how column i is named so findColumn finds it again: its header,
// or its position ("#3") when the header is blank or an earlier column has
// the same one.
func (l *csvLayout) name(i int) string {
	if h := strings.TrimSpace(l.header[i]); h != "" && findColumn(l.header, h) == i {
		return h
	}
	return "#" + strconv.Itoa(i+1)
}

// sniffComma picks the delimiter that appears most in the first line:
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
// named for it, compared with headerKey. Status and tags are found only under
// their own names, the way an export writes them, so an unrelated column
// isn't read as either.
var commonHeaders = map[string][]string{
	fieldEmail:     {"email", "emailaddress", "mail", "mailaddress", "subscriberemail", "contactemail"},
	fieldFirstName: {"firstname", "first", "givenname", "fname", "forename"},
	fieldLastName:  {"lastname", "last", "surname", "familyname", "lname"},
	fieldStatus:    {"status"},
	fieldTags:      {"tags"},
}

// findColumn returns the column named name: a position such as "#3"
// (counting from 1) when there is such a column, then an exact header match,
// then a loose match. It returns -1 when there's none.
func findColumn(header []string, name string) int {
	// "#3" is the third column whenever there is one, even when another
	// column's header is literally "#3": a blank or repeated header's column
	// is stored by that name, and must be read back as the same column.
	if pos, ok := strings.CutPrefix(strings.TrimSpace(name), "#"); ok {
		if n, err := strconv.Atoi(pos); err == nil && n >= 1 && n <= len(header) && strconv.Itoa(n) == pos {
			return n - 1
		}
	}
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

// readLayout reads the header row, if the file has one, and finds each
// field's column. A named column must exist, and email must be found.
func readLayout(data []byte, hasHeader bool, c columnChoice) (*csvLayout, error) {
	l, err := readHeader(data, hasHeader)
	if err != nil {
		return nil, err
	}
	if err := l.choose(c); err != nil {
		return nil, err
	}
	return l, nil
}

// readHeader reads a file's separator and first row: its header, or with
// hasHeader false its first contact.
func readHeader(data []byte, hasHeader bool) (*csvLayout, error) {
	if len(bytes.TrimSpace(bytes.TrimPrefix(data, utf8BOM))) == 0 {
		return nil, invalidImport("no_rows", "csv", "The file is empty.")
	}
	if !utf8.Valid(data) {
		return nil, invalidImport("invalid_encoding", "csv", "The file isn't UTF-8 text. Save it from your spreadsheet as \"CSV UTF-8\" and try again.")
	}
	l := &csvLayout{comma: sniffComma(bytes.TrimPrefix(data, utf8BOM)), hasHeader: hasHeader, col: map[string]int{}, fieldCol: map[string]int{}}
	first, err := newCSVReader(data, l.comma).Read()
	if err != nil {
		return nil, invalidImport("invalid_csv", "csv", "The "+l.firstRowName()+" can't be read as CSV: "+err.Error())
	}
	if hasHeader {
		l.header = slices.Clone(first)
	} else {
		l.header = make([]string, len(first))
		l.first = slices.Clone(first)
	}
	for _, field := range importFields {
		l.col[field] = -1
	}
	return l, nil
}

// firstRowName is what to call the file's first row in a message.
func (l *csvLayout) firstRowName() string {
	if l.hasHeader {
		return "header row"
	}
	return "first row"
}

// headerList is the headers for an error message, with blank ones by
// position.
func (l *csvLayout) headerList() string {
	names := make([]string, len(l.header))
	for i := range l.header {
		names[i] = l.name(i)
	}
	return strings.Join(names, ", ")
}

// colWord is what a message calls a column: "column", or "merge field" for
// a Mailchimp import's merge fields.
func (l *csvLayout) colWord() string {
	if l.merge {
		return "merge field"
	}
	return "column"
}

// noColumn is the error for a column name that names none.
func (l *csvLayout) noColumn(field, name string) error {
	if l.merge && len(l.header) == 0 {
		return invalidImport("invalid_mapping", field, fmt.Sprintf("The export has no merge field %q; it has none beyond first and last name.", name))
	}
	if l.merge {
		return invalidImport("invalid_mapping", field, fmt.Sprintf("The export has no merge field %q. Its merge fields are: %s.", name, l.headerList()))
	}
	return invalidImport("invalid_mapping", field, fmt.Sprintf("No column is headed %q. The headers are: %s.", name, l.headerList()))
}

// columnTaken is the error for a column two fields would come from.
func (l *csvLayout) columnTaken() error {
	if l.merge {
		return invalidImport("invalid_mapping", "field_columns", "Each merge field can go into one field only.")
	}
	return invalidImport("invalid_mapping", "columns", "Each field needs its own column.")
}

// choose picks each field's column. It picks all it can and returns the
// first problem, so a preview can still show the rest.
func (l *csvLayout) choose(c columnChoice) error {
	var first error
	fail := func(err error) {
		if first == nil {
			first = err
		}
	}
	if c.skip[fieldEmail] {
		fail(invalidImport("invalid_mapping", "columns.email", "Email can't be left out: every contact needs an address."))
	}
	// Named columns first, so a column someone named is never taken by
	// another field's guess; then common headers for the rest.
	used := map[int]bool{}
	for _, field := range importFields {
		l.col[field] = -1
		name := c.named[field]
		if name == "" || c.skip[field] {
			continue
		}
		i := findColumn(l.header, name)
		if i < 0 {
			fail(invalidImport("invalid_mapping", "columns."+field,
				fmt.Sprintf("No column is headed %q. The headers are: %s.", name, l.headerList())))
			continue
		}
		if used[i] {
			fail(invalidImport("invalid_mapping", "columns", "Each field needs its own column."))
			continue
		}
		used[i], l.col[field] = true, i
	}
	l.chooseFields(c, used, fail)
	for _, field := range importFields {
		if !c.guess || c.named[field] != "" || c.skip[field] {
			continue
		}
	find:
		for _, want := range commonHeaders[field] {
			for i, h := range l.header {
				if headerKey(h) == want && !used[i] {
					used[i], l.col[field] = true, i
					break find
				}
			}
		}
	}
	// A contact field is found under its name or its key, after the
	// built-in fields, so a header such as "First" stays a first name.
	l.guessFields(c, used)
	// With no header, the email column is the first one whose first row
	// holds an address.
	if c.guess && !l.hasHeader && l.col[fieldEmail] < 0 && !c.skip[fieldEmail] && c.named[fieldEmail] == "" {
		for i, v := range l.first {
			if _, err := NormalizeEmail(unescapeCell(strings.TrimSpace(v))); err == nil && !used[i] {
				used[i], l.col[fieldEmail] = true, i
				break
			}
		}
	}
	if l.col[fieldEmail] < 0 && !c.skip[fieldEmail] && c.named[fieldEmail] == "" {
		msg := fmt.Sprintf("No column looks like email addresses. The headers are: %s. Name the email column, or add a header row if the file has none.", l.headerList())
		if !l.hasHeader {
			msg = "No column holds an email address in the first row. Choose the column that holds the email addresses."
		}
		fail(invalidImport("no_email_column", "columns.email", msg))
	}
	return first
}

// guessFields finds each contact field c doesn't name from a column, not
// yet used, whose header matches the field's name or key loosely.
func (l *csvLayout) guessFields(c columnChoice, used map[int]bool) {
	for _, f := range c.fields {
		if _, named := c.fieldNamed[f.ID]; !c.guess || named {
			continue
		}
		for i, h := range l.header {
			if hk := headerKey(h); hk != "" && !used[i] && (hk == headerKey(f.Name) || hk == headerKey(f.Key)) {
				used[i], l.fieldCol[f.ID] = true, i
				break
			}
		}
	}
}

// chooseFields picks the columns of the contact fields named in c and of
// the new fields to make, checking each new field's name against the
// account's fields and its plan's limit (MG-131).
func (l *csvLayout) chooseFields(c columnChoice, used map[int]bool, fail func(error)) {
	clear(l.fieldCol)
	l.newCols = nil
	for _, ref := range c.unknownFields {
		fail(invalidImport("unknown_field", "field_columns",
			fmt.Sprintf("This account has no contact field with the key or id %q.", strings.TrimSpace(ref))))
	}
	keyOf := func(id string) string {
		if i := slices.IndexFunc(c.fields, func(f *Field) bool { return f.ID == id }); i >= 0 {
			return c.fields[i].Key
		}
		return id
	}
	for _, id := range slices.Sorted(maps.Keys(c.fieldNamed)) {
		name := c.fieldNamed[id]
		if name == "" {
			continue
		}
		i := findColumn(l.header, name)
		if i < 0 {
			fail(l.noColumn("field_columns."+keyOf(id), name))
			continue
		}
		if used[i] {
			fail(l.columnTaken())
			continue
		}
		used[i], l.fieldCol[id] = true, i
	}

	names := newFieldNames(c.fields, l.colWord())
	for _, nf := range c.newFields {
		i := findColumn(l.header, nf.Column)
		if strings.TrimSpace(nf.Column) == "" || i < 0 {
			fail(l.noColumn("new_fields", nf.Column))
			continue
		}
		if used[i] {
			fail(l.columnTaken())
			continue
		}
		raw := nf.Name
		if strings.TrimSpace(raw) == "" {
			raw = l.header[i]
		}
		if strings.TrimSpace(raw) == "" {
			fail(invalidImport("invalid_field_name", "new_fields",
				fmt.Sprintf("Column %d has no header, so its new field needs a name.", i+1)))
			continue
		}
		name, key, err := names.add(raw)
		if err != nil {
			fail(err)
			continue
		}
		used[i] = true
		l.newCols = append(l.newCols, newFieldCol{name: name, key: key, col: i})
	}
	if len(c.newFields) > 0 && len(c.fields)+len(c.newFields) > c.fieldLimit {
		fail(importFieldLimit(c.fieldLimit, len(c.fields), len(c.newFields), l.colWord()))
	}
}

// fieldNames checks new fields' names against an account's fields and the
// new fields before them: "name:" and a lower-case name, or "key:" and a
// key, to the field that has it.
type fieldNames struct {
	taken map[string]takenName
	// word is what a message calls a column.
	word string
}

type takenName struct {
	name  string
	isNew bool
}

func newFieldNames(fields []*Field, word string) *fieldNames {
	n := &fieldNames{taken: map[string]takenName{}, word: word}
	for _, f := range fields {
		n.taken["name:"+strings.ToLower(f.Name)], n.taken["key:"+f.Key] = takenName{f.Name, false}, takenName{f.Name, false}
	}
	return n
}

// add checks a new field's name as written and, when it can be made,
// keeps it as taken.
func (n *fieldNames) add(raw string) (name, key string, err error) {
	if name, key, err = n.check(raw); err == nil {
		n.keep(name, key)
	}
	return name, key, err
}

// keep keeps a new field's name and key as taken.
func (n *fieldNames) keep(name, key string) {
	n.taken["name:"+strings.ToLower(name)], n.taken["key:"+key] = takenName{name, true}, takenName{name, true}
}

// check checks a new field's name as written, keeping nothing.
func (n *fieldNames) check(raw string) (name, key string, err error) {
	name, key, err = normalizeFieldName(raw)
	if err != nil {
		var ve *auth.ValidationError
		if errors.As(err, &ve) {
			err = invalidImport(ve.Code, "new_fields", ve.Message)
		}
		return "", "", err
	}
	exists := func(t takenName, msg string) error {
		if t.isNew {
			msg = fmt.Sprintf("Two new fields would both be named %q, or have the same key. Give one another name.", t.name)
		}
		return invalidImport("field_exists", "new_fields", msg)
	}
	if t, ok := n.taken["name:"+strings.ToLower(name)]; ok {
		return "", "", exists(t, fmt.Sprintf("There's already a contact field named %q. Match the %s to it instead of making a new one.", t.name, n.word))
	}
	if t, ok := n.taken["key:"+key]; ok {
		return "", "", exists(t, fmt.Sprintf("A new field named %q would have the key %s, which the field %q already has. "+
			"Pick another name, or match the %s to that field.", name, key, t.name, n.word))
	}
	return name, key, nil
}

// csvRow is one contact row of a file.
type csvRow struct {
	// index counts contact rows from 0, the way processed_rows does.
	index int
	// line is where the row starts in the file; the header is line 1.
	line                             int
	email, first, last, status, tags string
	// fields are the row's contact field values by field id, as written;
	// a blank cell is left out.
	fields     map[string]string
	unreadable bool
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

// eachRow calls fn with every contact row, after the header if there is
// one, in order. Blank
// rows are left out. A row with a stray quote is passed as unreadable; a
// quote that never closes, which would swallow the rest of the file, is an
// error.
func (l *csvLayout) eachRow(data []byte, fn func(csvRow) error) error {
	r := newCSVReader(data, l.comma)
	if l.hasHeader {
		if _, err := r.Read(); err != nil {
			return err
		}
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
			get := func(field string) string { return unescapeCell(cell(rec, l.col[field])) }
			row = csvRow{line: line, email: get(fieldEmail), first: get(fieldFirstName), last: get(fieldLastName),
				status: get(fieldStatus), tags: cell(rec, l.col[fieldTags])}
			for id, i := range l.fieldCol {
				if v := importFieldValue(cell(rec, i)); v != "" {
					if row.fields == nil {
						row.fields = map[string]string{}
					}
					row.fields[id] = v
				}
			}
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

// importFieldValue is a contact field cell as a value (MG-131): an
// export's formula guard taken off, line breaks become spaces and it's cut
// to MaxFieldValueLen characters, so a long value never costs a contact.
// "" is no value.
func importFieldValue(v string) string {
	v = strings.Join(strings.Fields(unescapeCell(strings.TrimSpace(v))), " ")
	if r := []rune(v); len(r) > MaxFieldValueLen {
		v = strings.TrimSpace(string(r[:MaxFieldValueLen]))
	}
	return v
}

// formulaStart are the characters a spreadsheet reads as the start of a
// formula. An export puts a ' before a cell starting with one (see
// ExportCSV), and an import takes it off again.
const formulaStart = "=+-@\t\r"

// unescapeCell undoes an export's guard against spreadsheet formulas.
func unescapeCell(v string) string {
	if len(v) >= 2 && v[0] == '\'' && strings.ContainsRune(formulaStart, rune(v[1])) {
		return v[1:]
	}
	return v
}

// rowStatus is the status a row's contact is added with: the import's,
// unless the row's status cell holds it back. A status cell can't subscribe
// anyone the import itself doesn't.
func rowStatus(cellValue, importStatus string) string {
	switch v := strings.ToLower(strings.TrimSpace(cellValue)); v {
	case StatusUnsubscribed, StatusBounced, StatusComplained, StatusUnconfirmed:
		return v
	}
	return importStatus
}

// rowTags splits a tags cell into tag names, without repeats ignoring case.
// It returns false when a name isn't a valid tag name.
func rowTags(cellValue string) ([]string, bool) {
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.FieldsFunc(cellValue, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' }) {
		part = strings.TrimSpace(unescapeCell(strings.TrimSpace(part)))
		if part == "" {
			continue
		}
		name, err := normalizeTagName(part)
		if err != nil {
			return nil, false
		}
		if k := strings.ToLower(name); !seen[k] {
			seen[k] = true
			out = append(out, name)
		}
	}
	return out, true
}

// --- Creating and reading imports ---

const importColumns = `i.id, i.account_id, i.file_name, i.status, i.permission_confirmed, i.email_column, i.first_name_column, i.last_name_column,
	i.status_column, i.tags_column,
	i.total_rows, i.processed_rows, i.imported, i.skipped, i.review, i.hold_reason, i.error, i.created_at, i.started_at, i.finished_at,
	i.list_source, i.list_source_detail, i.invalid_rows, i.role_addresses, i.disposable_addresses, i.shared_addresses, i.shared_account_id::text,
	ARRAY(SELECT t.id::text FROM tags t WHERE t.id = ANY(i.tag_ids) ORDER BY lower(t.name), t.id),
	ARRAY(SELECT t.name FROM tags t WHERE t.id = ANY(i.tag_ids) ORDER BY lower(t.name), t.id),
	ARRAY(SELECT s.reason || ':' || count(*) FROM import_skips s WHERE s.import_id = i.id GROUP BY s.reason),
	i.kind, i.existing_tagged, i.existing_suppressed, i.unlisted_skipped, i.mailchimp, i.has_header,
	coalesce((SELECT jsonb_agg(jsonb_build_object('id', f.id, 'key', f.key, 'name', f.name, 'column', fc->>'column',
			'created', coalesce((fc->>'created')::boolean, false)) ORDER BY f.created_at, f.id)
		FROM jsonb_array_elements(i.field_columns) AS fc JOIN contact_fields f ON f.id = (fc->>'field_id')::uuid), '[]'),
	i.existing_updated`

func scanImport(row pgx.Row) (*Import, error) {
	var im Import
	var tagIDs, tagNames, byReason []string
	var summary, fieldCols []byte
	err := row.Scan(&im.ID, &im.AccountID, &im.FileName, &im.Status, &im.PermissionConfirmed, &im.EmailColumn, &im.FirstNameColumn, &im.LastNameColumn,
		&im.StatusColumn, &im.TagsColumn, &im.TotalRows, &im.ProcessedRows, &im.Imported, &im.Skipped, &im.Review, &im.HoldReason, &im.Error, &im.CreatedAt, &im.StartedAt, &im.FinishedAt,
		&im.ListSource, &im.ListSourceDetail, &im.Checks.InvalidRows, &im.Checks.RoleAddresses, &im.Checks.DisposableAddresses,
		&im.Checks.SharedAddresses, &im.Checks.SharedAccountID,
		&tagIDs, &tagNames, &byReason,
		&im.Kind, &im.ExistingTagged, &im.ExistingSuppressed, &im.UnlistedSkipped, &summary, &im.HasHeader,
		&fieldCols, &im.ExistingUpdated)
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
	im.SkippedByReason[SkipInvalidRow] += im.UnlistedSkipped
	var fcs []struct {
		ID      string `json:"id"`
		Key     string `json:"key"`
		Name    string `json:"name"`
		Column  string `json:"column"`
		Created bool   `json:"created"`
	}
	if err := json.Unmarshal(fieldCols, &fcs); err != nil {
		return nil, fmt.Errorf("read import field columns: %w", err)
	}
	im.FieldColumns = make([]ImportFieldColumn, len(fcs))
	for i, fc := range fcs {
		im.FieldColumns[i] = ImportFieldColumn(fc)
	}
	if summary != nil {
		im.Mailchimp = &MailchimpSummary{}
		if err := json.Unmarshal(summary, im.Mailchimp); err != nil {
			return nil, fmt.Errorf("read import summary: %w", err)
		}
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
// first import of more than LargeFirstImport rows is held for review, and
// so is one the list checks (MG-63) find suspicious.
func (s *Service) CreateImport(ctx context.Context, accountID string, in ImportInput) (*Import, error) {
	detail, err := checkListSource(in.ListSource, in.ListSourceDetail)
	if err != nil {
		return nil, err
	}
	if len(in.CSV) > MaxImportBytes {
		return nil, invalidImport("file_too_large", "csv", fmt.Sprintf("The file is over %d MB. Split it into smaller files.", MaxImportBytes>>20))
	}
	af, err := s.accountFields(ctx, accountID)
	if err != nil {
		return nil, err
	}
	l, err := readLayout(in.CSV, !in.NoHeader, in.choice(af))
	if err != nil {
		return nil, err
	}
	scan := &fileScan{}
	seen := map[string]struct{}{}
	total, err := checkRows(in.CSV, l, func(r csvRow) { scan.add(r, seen) })
	if err != nil {
		return nil, err
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
	// The lock also keeps the new fields below, and fields made meanwhile,
	// from both slipping under the plan's limit (see CreateField).
	var first bool
	var plan string
	if err := tx.QueryRow(ctx, `
		SELECT NOT EXISTS (SELECT 1 FROM imports WHERE account_id = a.id AND status NOT IN ('failed', 'cancelled')), a.plan
		FROM accounts a WHERE a.id = $1 FOR UPDATE`, accountID).Scan(&first, &plan); err != nil {
		return nil, fmt.Errorf("lock account: %w", err)
	}
	// New fields are made now, before any row is imported, all or none
	// (MG-131).
	made, err := s.createImportFields(ctx, tx, accountID, plan, l.newCols, l.colWord())
	if err != nil {
		return nil, err
	}
	fieldCols, err := l.storedFieldColumns(made)
	if err != nil {
		return nil, err
	}
	if scan.counts.SharedAddresses, scan.counts.SharedAccountID, err = sharedWith(ctx, tx, accountID, scan.hashes); err != nil {
		return nil, err
	}
	review, holdReason := ReviewNone, (*string)(nil)
	if reason := s.Checks.holdFor(scan); reason != "" {
		review, holdReason = ReviewHeld, ptr(reason)
	} else if first && total > LargeFirstImport {
		review, holdReason = ReviewHeld, ptr(HoldLargeFirstImport)
	}
	c := scan.counts
	var id string
	if err := tx.QueryRow(ctx, `
		INSERT INTO imports (account_id, file_name, permission_confirmed, email_column, first_name_column, last_name_column,
			status_column, tags_column, tag_ids, csv, total_rows, review, hold_reason, created_at,
			list_source, list_source_detail, invalid_rows, role_addresses, disposable_addresses, shared_addresses, shared_account_id,
			has_header, field_columns)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23) RETURNING id`,
		accountID, fileName, in.PermissionConfirmed, *l.column(fieldEmail), l.column(fieldFirstName), l.column(fieldLastName),
		l.column(fieldStatus), l.column(fieldTags),
		tagIDs, in.CSV, total, review, holdReason, s.now(),
		in.ListSource, detail, c.InvalidRows, c.RoleAddresses, c.DisposableAddresses, c.SharedAddresses, c.SharedAccountID,
		!in.NoHeader, fieldCols).Scan(&id); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO import_address_hashes (import_id, account_id, hash)
		SELECT $1, $2, h FROM unnest($3::bytea[]) AS h ON CONFLICT DO NOTHING`, id, accountID, scan.hashes); err != nil {
		return nil, fmt.Errorf("store address hashes: %w", err)
	}
	im, err := scanImport(tx.QueryRow(ctx, `SELECT `+importColumns+` FROM imports i WHERE i.id = $1`, id))
	if err != nil {
		return nil, err
	}
	return im, tx.Commit(ctx)
}

// storedFieldColumns is how the import keeps its contact fields' columns,
// as JSON: the fields it matched, then made, the new ones by their ids.
func (l *csvLayout) storedFieldColumns(made []*Field) ([]byte, error) {
	out := []storedFieldColumn{}
	for _, id := range slices.Sorted(maps.Keys(l.fieldCol)) {
		out = append(out, storedFieldColumn{FieldID: id, Column: l.name(l.fieldCol[id])})
	}
	for i, f := range made {
		out = append(out, storedFieldColumn{FieldID: f.ID, Column: l.name(l.newCols[i].col), Created: true})
	}
	return json.Marshal(out)
}

// checkRows reads every contact row the way CreateImport checks a file,
// passing each to fn, and returns how many there are. It stops at the first
// problem.
func checkRows(data []byte, l *csvLayout, fn func(csvRow)) (int, error) {
	total := 0
	fileTags := map[string]bool{}
	err := l.eachRow(data, func(r csvRow) error {
		total++
		if total > MaxImportRows {
			return invalidImport("too_many_rows", "csv", fmt.Sprintf("The file has more than %d contacts. Split it into smaller files.", MaxImportRows))
		}
		names, ok := rowTags(r.tags)
		if !ok {
			return invalidImport("invalid_tag_name", "csv",
				fmt.Sprintf("Row %d has a tag that isn't 1 to %d characters. Fix that row's tags and try again.", r.line, MaxTagNameLen))
		}
		for _, n := range names {
			fileTags[strings.ToLower(n)] = true
		}
		if len(fileTags) > MaxImportFileTags {
			return invalidImport("too_many_tags", "csv",
				fmt.Sprintf("The tags column names more than %d different tags. Import the file in parts, or tag the contacts afterwards.", MaxImportFileTags))
		}
		if fn != nil {
			fn(r)
		}
		return nil
	})
	if err != nil {
		return total, err
	}
	if total == 0 {
		msg := "The file has a header row but no contacts."
		if !l.hasHeader {
			msg = "The file has no contacts."
		}
		return 0, invalidImport("no_rows", "csv", msg)
	}
	return total, nil
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
		SELECT row_number, email, reason, coalesce(source_row, '') FROM import_skips WHERE import_id = $1 AND row_number > $2
		ORDER BY row_number LIMIT $3`, importID, afterRow, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SkippedRow{}
	for rows.Next() {
		var sr SkippedRow
		if err := rows.Scan(&sr.Row, &sr.Email, &sr.Reason, &sr.Source); err != nil {
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
	named                map[string]string
	tagIDs               []string
	status               string
	processed            int
	review               string
	// consent is the consent record each added contact gets (MG-63): the
	// import's list source, as JSON, and when it was given; nil for an
	// import from before list sources.
	consent   []byte
	createdAt time.Time
	kind      string
	hasHeader bool
	// fieldCols are the contact fields the import fills and their columns
	// (MG-131).
	fieldCols []storedFieldColumn
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
	var emailCol string
	var firstCol, lastCol, statusCol, tagsCol *string
	var fieldCols []byte
	err := s.DB.QueryRow(ctx, `
		UPDATE imports SET status = 'running', lease_token = gen_random_uuid(), lease_until = $2,
			attempts = attempts + 1, started_at = coalesce(started_at, $1)
		WHERE id = (
			SELECT id FROM imports
			WHERE status = 'queued' OR (status = 'running' AND lease_until < $1)
				-- A Mailchimp import waiting for a plan that covers it,
				-- once the account has one.
				OR (status = 'waiting' AND EXISTS (SELECT 1 FROM accounts a WHERE a.id = imports.account_id
					AND a.plan <> 'free' AND a.tester_started_at IS NULL))
			ORDER BY created_at, id LIMIT 1 FOR UPDATE SKIP LOCKED)
		RETURNING id, account_id, lease_token, csv, email_column, first_name_column, last_name_column, status_column, tags_column,
			tag_ids::text[],
			permission_confirmed, processed_rows, review, created_at, kind, has_header,
			CASE WHEN list_source IS NULL THEN NULL ELSE
				jsonb_build_object('list_source', list_source, 'list_source_detail', list_source_detail, 'file_name', file_name) END,
			field_columns`,
		now, now.Add(ImportLease)).Scan(&j.id, &j.accountID, &j.token, &j.data, &emailCol, &firstCol, &lastCol, &statusCol, &tagsCol, &j.tagIDs,
		&permission, &j.processed, &j.review, &j.createdAt, &j.kind, &j.hasHeader, &j.consent, &fieldCols)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim import: %w", err)
	}
	j.named = map[string]string{fieldEmail: emailCol, fieldFirstName: deref(firstCol), fieldLastName: deref(lastCol),
		fieldStatus: deref(statusCol), fieldTags: deref(tagsCol)}
	if err := json.Unmarshal(fieldCols, &j.fieldCols); err != nil {
		return true, fmt.Errorf("import %s: read field columns: %w", j.id, err)
	}
	j.status = StatusUnconfirmed
	if permission {
		j.status = StatusSubscribed
	}
	run := s.runImport
	if j.kind == ImportKindMailchimp {
		run = s.runMailchimp
	}
	if err := run(ctx, &j); err != nil {
		return true, fmt.Errorf("import %s: %w", j.id, err)
	}
	return true, nil
}

func (s *Service) runImport(ctx context.Context, j *importJob) error {
	fieldNamed := map[string]string{}
	for _, fc := range j.fieldCols {
		fieldNamed[fc.FieldID] = fc.Column
	}
	l, err := readLayout(j.data, j.hasHeader, columnChoice{named: j.named, fieldNamed: fieldNamed})
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
		if !Counted(rowStatus(r.status, j.status)) {
			return nil
		}
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
	status      string
	tags        []string
	// fields are the row's contact field values by field id.
	fields map[string]string
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
		tags, _ := rowTags(r.tags) // checked when the import was created
		cands = append(cands, candidate{line: r.line, raw: r.email, email: e, first: importName(r.first), last: importName(r.last),
			status: rowStatus(r.status, j.status), tags: tags, fields: r.fields})
	}

	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	limit, err := lockedCount(ctx, tx, j.accountID)
	if err != nil {
		return err
	}
	count := limit.count
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
	// The import's contact fields that still exist, locked until the batch
	// commits so none is deleted between here and the write; a field
	// deleted since the import was created is skipped (MG-131).
	fieldKeys, err := liveFieldKeys(ctx, tx, j.accountID, j.fieldCols)
	if err != nil {
		return err
	}
	var add []candidate
	var updEmails, updFields []string
	for i, c := range cands {
		switch {
		case existing[c.email]:
			skip(c.line, c.raw, SkipExists)
			// An existing contact isn't added again, but its contact
			// fields take the row's non-blank values.
			if v := fieldValuesJSON(c.fields, fieldKeys); v != "{}" {
				updEmails, updFields = append(updEmails, c.email), append(updFields, v)
			}
		case suppressed[hashes[i]]:
			skip(c.line, c.raw, SkipSuppressed)
		case limit.capped() && Counted(c.status) && count >= limit.limit:
			skip(c.line, c.raw, SkipOverLimit)
		default:
			if Counted(c.status) {
				count++
			}
			add = append(add, c)
		}
	}

	// Whether the import is held is read here, under a share lock, so an
	// admin's approval (which updates the import first) waits for this
	// batch and then releases its contacts too.
	if err := tx.QueryRow(ctx, `SELECT review FROM imports WHERE id = $1 FOR SHARE`, j.id).Scan(&j.review); err != nil {
		return fmt.Errorf("read import review: %w", err)
	}
	var held *string
	if j.review == ReviewHeld {
		held = &j.id
	}
	addEmails := make([]string, len(add))
	firsts := make([]*string, len(add))
	lasts := make([]*string, len(add))
	statuses := make([]string, len(add))
	addFields := make([]string, len(add))
	for i, c := range add {
		addEmails[i], firsts[i], lasts[i], statuses[i] = c.email, c.first, c.last, c.status
		addFields[i] = fieldValuesJSON(c.fields, fieldKeys)
	}
	// A microsecond apart, so contacts keep the file's order in lists and
	// exports.
	now := s.now()
	rows2, err := tx.Query(ctx, `
		INSERT INTO contacts (account_id, email, first_name, last_name, status, source, created_at, updated_at, status_changed_at,
			held_import_id, import_id, fields)
		SELECT $1, x.email, x.first_name, x.last_name, x.status, 'import', x.at, x.at, x.at, $7, $8, x.fields::jsonb
		FROM (
			SELECT *, $6::timestamptz + (n - 1) * interval '1 microsecond' AS at
			FROM unnest($2::text[], $3::text[], $4::text[], $5::text[], $9::text[]) WITH ORDINALITY AS u(email, first_name, last_name, status, fields, n)
		) AS x
		ORDER BY x.n
		ON CONFLICT (account_id, email) DO NOTHING
		RETURNING id::text, email`, j.accountID, addEmails, firsts, lasts, statuses, now, held, j.id, addFields)
	if err != nil {
		return err
	}
	var ids []string
	added := map[string]string{} // email -> contact id
	for rows2.Next() {
		var id, email string
		if err := rows2.Scan(&id, &email); err != nil {
			rows2.Close()
			return err
		}
		ids = append(ids, id)
		added[email] = id
	}
	rows2.Close()
	if err := rows2.Err(); err != nil {
		return err
	}
	for _, c := range add {
		if added[c.email] == "" {
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
	if err := s.tagFromCells(ctx, tx, j.accountID, add, added, now); err != nil {
		return err
	}
	updated := 0
	if len(updEmails) > 0 {
		// Only contacts whose values change are touched and counted.
		res, err := tx.Exec(ctx, `
			UPDATE contacts c SET fields = c.fields || x.f::jsonb, updated_at = $4
			FROM unnest($2::text[], $3::text[]) AS x(email, f)
			WHERE c.account_id = $1 AND c.email = x.email AND NOT c.fields @> x.f::jsonb`, j.accountID, updEmails, updFields, now)
		if err != nil {
			return err
		}
		updated = int(res.RowsAffected())
	}
	if len(ids) > 0 && j.consent != nil {
		// Each added contact's consent record is the list source the
		// import gave (MG-63).
		if _, err := tx.Exec(ctx, `
			INSERT INTO consents (account_id, contact_id, source, import_id, wording, created_at)
			SELECT $1, c.id, 'import', $3, $4, $5 FROM unnest($2::uuid[]) AS c(id)`,
			j.accountID, ids, j.id, j.consent, j.createdAt); err != nil {
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
		UPDATE imports SET processed_rows = $3, imported = imported + $4, skipped = skipped + $5, lease_until = $6,
			existing_updated = existing_updated + $7`,
		processed, len(ids), len(skips), now.Add(ImportLease), updated); err != nil {
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

// liveFieldKeys returns the key of each of an import's contact fields that
// still exists, by field id, and locks those fields until tx ends.
func liveFieldKeys(ctx context.Context, tx pgx.Tx, accountID string, cols []storedFieldColumn) (map[string]string, error) {
	out := map[string]string{}
	if len(cols) == 0 {
		return out, nil
	}
	ids := make([]string, len(cols))
	for i, fc := range cols {
		ids[i] = fc.FieldID
	}
	rows, err := tx.Query(ctx, `SELECT id::text, key FROM contact_fields WHERE account_id = $1 AND id = ANY($2::uuid[]) FOR SHARE`, accountID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, key string
		if err := rows.Scan(&id, &key); err != nil {
			return nil, err
		}
		out[id] = key
	}
	return out, rows.Err()
}

// fieldValuesJSON is a row's contact field values as a JSON object by field
// key, leaving out fields that no longer exist.
func fieldValuesJSON(values map[string]string, keys map[string]string) string {
	out := map[string]string{}
	for id, v := range values {
		if k := keys[id]; k != "" {
			out[k] = v
		}
	}
	b, _ := json.Marshal(out) // a map of strings always marshals
	return string(b)
}

// tagFromCells puts each added contact's tags-column tags on it, creating the
// tags the account doesn't have yet.
func (s *Service) tagFromCells(ctx context.Context, tx pgx.Tx, accountID string, add []candidate, added map[string]string, now time.Time) error {
	var names []string
	seen := map[string]bool{}
	for _, c := range add {
		if added[c.email] == "" {
			continue
		}
		for _, n := range c.tags {
			if k := strings.ToLower(n); !seen[k] {
				seen[k] = true
				names = append(names, n)
			}
		}
	}
	if len(names) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO tags (account_id, name, created_at) SELECT $1, n, $3 FROM unnest($2::text[]) AS n
		ON CONFLICT (account_id, lower(name)) DO NOTHING`, accountID, names, now); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT lower(name), id::text FROM tags WHERE account_id = $1 AND lower(name) = ANY($2::text[])`,
		accountID, lowerAll(names))
	if err != nil {
		return err
	}
	tagID := map[string]string{}
	for rows.Next() {
		var k, id string
		if err := rows.Scan(&k, &id); err != nil {
			rows.Close()
			return err
		}
		tagID[k] = id
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	var contactIDs, tagIDs []string
	for _, c := range add {
		id := added[c.email]
		if id == "" {
			continue
		}
		for _, n := range c.tags {
			if t := tagID[strings.ToLower(n)]; t != "" {
				contactIDs, tagIDs = append(contactIDs, id), append(tagIDs, t)
			}
		}
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO contact_tags (contact_id, tag_id, created_at) SELECT x.c, x.t, $3 FROM unnest($1::uuid[], $2::uuid[]) AS x(c, t)
		ON CONFLICT DO NOTHING`, contactIDs, tagIDs, now)
	return err
}

func lowerAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, v := range ss {
		out[i] = strings.ToLower(v)
	}
	return out
}

// afterImportBatch, when set by a test, runs after each batch commits; an
// error stops the import there, as a crash would.
var afterImportBatch func(processed int) error

// collect runs a query of one text column and returns its values as a set.
func collect(ctx context.Context, tx querier, q string, args ...any) (map[string]bool, error) {
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
