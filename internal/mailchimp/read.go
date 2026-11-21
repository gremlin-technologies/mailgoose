package mailchimp

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Limits on what Read takes.
const (
	// DefaultMaxMembers is the most members one import takes during the
	// beta (decision 4); MAILCHIMP_IMPORT_MAX_CONTACTS sets it.
	DefaultMaxMembers = 250_000
	// DefaultMaxBytes is the most bytes Read decompresses and reads from
	// the files it uses. Files it ignores, such as an account export's
	// gallery images, aren't read and don't count.
	DefaultMaxBytes = 1 << 30
	// MaxHTMLFileBytes is the largest template or campaign file Read
	// takes; a larger one is ignored with the reason too_large.
	MaxHTMLFileBytes = 5 << 20
	// DefaultAudienceName names an audience when the export doesn't.
	DefaultAudienceName = "Mailchimp"
	// MaxColumns is the most columns a member file's header may have, and
	// the most different column names one audience's files may have
	// between them. Mailchimp allows about 80 merge fields, so this is
	// generous; it keeps a crafted header from costing time and memory.
	MaxColumns = 1000
	// MaxSkippedRows is how many skipped rows an audience lists in
	// Skipped. Every skipped row is still counted in SkippedByReason.
	MaxSkippedRows = 1000
	// MaxListNames is the most names Read takes from one tags, group or
	// permission cell; the rest are left out and the member's NamesCut is
	// set.
	MaxListNames = 500
)

// Options changes how Read reads an export. The zero value reads with the
// defaults.
type Options struct {
	// AudienceName names the audience when the export doesn't: an audience
	// export or a single CSV. Default DefaultAudienceName.
	AudienceName string
	// GroupColumns are the member-file columns to read as group categories.
	// A CSV doesn't say which columns are groups and which are merge
	// fields, so the reader takes none unless named here; matched ignoring
	// case.
	GroupColumns []string
	// MaxMembers and MaxBytes default to DefaultMaxMembers and
	// DefaultMaxBytes.
	MaxMembers int
	MaxBytes   int64
}

func (o Options) withDefaults() Options {
	if strings.TrimSpace(o.AudienceName) == "" {
		o.AudienceName = DefaultAudienceName
	}
	o.AudienceName = strings.TrimSpace(o.AudienceName)
	if o.MaxMembers <= 0 {
		o.MaxMembers = DefaultMaxMembers
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = DefaultMaxBytes
	}
	return o
}

// Error is an export Read refuses. Code is one of the Err* codes and
// Message says what to do, for the customer.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

// Why Read refuses an export.
const (
	ErrNotZipOrCSV    = "not_zip_or_csv"
	ErrBadZip         = "bad_zip"
	ErrUnsafePath     = "unsafe_path"
	ErrTooLarge       = "too_large"
	ErrTooManyMembers = "too_many_members"
	ErrTooManyColumns = "too_many_columns"
	ErrNoEmailColumn  = "no_email_column"
	ErrUnknownStatus  = "unknown_status"
	ErrNothingToRead  = "nothing_to_read"
)

func refuse(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// IgnoredFile is a file in the export that Read didn't use.
type IgnoredFile struct {
	Path string
	// Reason is IgnoreUnknown or IgnoreTooLarge.
	Reason string
}

// Why a file was ignored.
const (
	IgnoreUnknown  = "unknown_file"
	IgnoreTooLarge = "too_large"
)

var zipMagic = []byte("PK\x03\x04")

// Read reads one Mailchimp export: the account export ZIP, an audience
// export ZIP or a single audience CSV. name is the uploaded file's name; a
// single CSV's status comes from it when the CSV has no status column.
func Read(r io.ReaderAt, size int64, name string, opts Options) (*Bundle, error) {
	opts = opts.withDefaults()
	head := make([]byte, 4)
	n, err := r.ReadAt(head, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	head = head[:n]
	if bytes.Equal(head, zipMagic) || bytes.HasPrefix(head, []byte("PK\x05\x06")) {
		return readZip(r, size, opts)
	}
	return readSingleCSV(io.NewSectionReader(r, 0, size), name, opts)
}

func readSingleCSV(r io.Reader, name string, opts Options) (*Bundle, error) {
	rd := &reader{opts: opts, budget: opts.MaxBytes}
	file := path.Base(strings.ReplaceAll(name, `\`, "/"))
	if file == "." || file == "/" || file == "" {
		file = "audience.csv"
	}
	sniff := bufio.NewReaderSize(rd.limit(r), 64<<10)
	peek, _ := sniff.Peek(4096)
	if !looksLikeText(peek) {
		return nil, refuse(ErrNotZipOrCSV, "That file isn't a ZIP or a CSV. Upload the ZIP Mailchimp gave you, or one audience CSV from it.")
	}
	aud := &Audience{Name: opts.AudienceName}
	if err := rd.readMembers(sniff, file, statusFromName(file), aud, true); err != nil {
		return nil, err
	}
	b := &Bundle{Source: SourceAudienceCSV, Audiences: []*Audience{aud}}
	return b, nil
}

// looksLikeText reports whether the start of a file could be a CSV: no NUL
// bytes, and valid UTF-8 apart from a rune cut off at the end.
func looksLikeText(p []byte) bool {
	if len(p) == 0 || bytes.IndexByte(p, 0) >= 0 {
		return false
	}
	for i := 0; i < len(p); {
		r, w := utf8.DecodeRune(p[i:])
		if r == utf8.RuneError && w == 1 {
			return len(p)-i < utf8.UTFMax
		}
		i += w
	}
	return true
}

// zipEntry is one file of a ZIP, with its path cleaned and the common
// root folder taken off.
type zipEntry struct {
	f    *zip.File
	path string // as in the ZIP, cleaned
	rel  string // without the common root folder
}

func readZip(r io.ReaderAt, size int64, opts Options) (*Bundle, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, refuse(ErrBadZip, "That ZIP can't be opened. Download the export from Mailchimp again and upload it as it is.")
	}
	var entries []*zipEntry
	for _, f := range zr.File {
		p, err := safePath(f)
		if err != nil {
			return nil, err
		}
		if f.FileInfo().IsDir() || p == "" {
			continue
		}
		entries = append(entries, &zipEntry{f: f, path: p, rel: p})
	}
	stripRoot(entries)

	rd := &reader{opts: opts, budget: opts.MaxBytes}
	b := &Bundle{}
	audiences := map[string]*Audience{}
	var campaignCSV *zipEntry
	texts := map[string]*zipEntry{}
	nested := false
	var members []*zipEntry
	for _, e := range entries {
		dir, base := path.Split(e.rel)
		dir = strings.TrimSuffix(dir, "/")
		ext := strings.ToLower(path.Ext(base))
		top := firstElem(e.rel)
		switch {
		case ignoredJunk(e.rel) || otherExportFolder(dir):
			b.Ignored = append(b.Ignored, IgnoredFile{Path: e.path, Reason: IgnoreUnknown})
		case top == "templates" && (ext == ".html" || ext == ".htm"):
			nested = true
			h, err := rd.readHTML(e)
			if err != nil {
				return nil, err
			}
			if h == nil {
				b.Ignored = append(b.Ignored, IgnoredFile{Path: e.path, Reason: IgnoreTooLarge})
				continue
			}
			b.Templates = append(b.Templates, &HTMLFile{Name: stem(base), Path: e.path, HTML: h.HTML})
		case hasElem(dir, "campaigns_content") && (ext == ".html" || ext == ".htm"):
			nested = true
			h, err := rd.readHTML(e)
			if err != nil {
				return nil, err
			}
			if h == nil {
				b.Ignored = append(b.Ignored, IgnoredFile{Path: e.path, Reason: IgnoreTooLarge})
				continue
			}
			b.Campaigns = append(b.Campaigns, &Campaign{Name: stem(base), Path: e.path, HTML: h.HTML})
		case hasElem(dir, "campaigns_content") && ext == ".txt":
			texts[stem(base)] = e
		case ext == ".csv" && statusFromName(base) != "":
			members = append(members, e)
			if dir != "" {
				nested = true
			}
		case ext == ".csv" && campaignCSV == nil && strings.Contains(strings.ToLower(base), "campaign"):
			nested = true
			campaignCSV = e
		default:
			b.Ignored = append(b.Ignored, IgnoredFile{Path: e.path, Reason: IgnoreUnknown})
		}
	}

	sort.Slice(members, func(i, j int) bool { return members[i].path < members[j].path })
	for _, e := range members {
		name := audienceName(e.rel, opts.AudienceName)
		aud := audiences[name]
		if aud == nil {
			aud = &Audience{Name: name}
			audiences[name] = aud
			b.Audiences = append(b.Audiences, aud)
		}
		rc, err := e.f.Open()
		if err != nil {
			return nil, refuse(ErrBadZip, "%s in the ZIP can't be read. Download the export from Mailchimp again.", e.path)
		}
		err = rd.readMembers(rd.limit(rc), e.path, statusFromName(path.Base(e.rel)), aud, false)
		_ = rc.Close()
		if err != nil {
			return nil, err
		}
	}

	for _, c := range b.Campaigns {
		if t := texts[c.Name]; t != nil {
			h, err := rd.readHTML(t)
			if err != nil {
				return nil, err
			}
			if h != nil {
				c.Text = h.HTML
			}
			delete(texts, c.Name)
		}
	}
	for _, t := range texts {
		b.Ignored = append(b.Ignored, IgnoredFile{Path: t.path, Reason: IgnoreUnknown})
	}
	if campaignCSV != nil {
		if err := rd.readCampaignCSV(campaignCSV, b.Campaigns); err != nil {
			return nil, err
		}
	}

	if len(b.Audiences) == 0 && len(b.Templates) == 0 && len(b.Campaigns) == 0 {
		return nil, refuse(ErrNothingToRead, "That ZIP has no audiences, templates or emails Mailchimp exports. Upload the account export or an audience export from Mailchimp.")
	}
	b.Source = SourceAudienceExport
	if nested {
		b.Source = SourceAccountExport
	}
	sort.Slice(b.Audiences, func(i, j int) bool { return b.Audiences[i].Name < b.Audiences[j].Name })
	sort.Slice(b.Templates, func(i, j int) bool { return b.Templates[i].Path < b.Templates[j].Path })
	sort.Slice(b.Campaigns, func(i, j int) bool { return b.Campaigns[i].Path < b.Campaigns[j].Path })
	sort.Slice(b.Ignored, func(i, j int) bool { return b.Ignored[i].Path < b.Ignored[j].Path })
	return b, nil
}

// safePath cleans a ZIP entry's name and refuses one that could escape the
// folder it's unpacked in: absolute, with a drive letter, with a ".."
// element, or a symbolic link.
func safePath(f *zip.File) (string, error) {
	name := strings.ReplaceAll(f.Name, `\`, "/")
	bad := strings.HasPrefix(name, "/") || (len(name) >= 2 && name[1] == ':') || strings.ContainsRune(name, 0)
	for _, el := range strings.Split(name, "/") {
		if el == ".." {
			bad = true
		}
	}
	if f.Mode()&fs.ModeSymlink != 0 {
		bad = true
	}
	if bad {
		return "", refuse(ErrUnsafePath, "That ZIP has a file with an unsafe path (%q), so it wasn't opened. Upload the ZIP Mailchimp gave you, unchanged.", f.Name)
	}
	return strings.TrimPrefix(path.Clean("/"+name), "/"), nil
}

// stripRoot takes off a folder every entry is inside, as when a ZIP holds
// one folder named for the export.
func stripRoot(entries []*zipEntry) {
	if len(entries) == 0 {
		return
	}
	root := ""
	for i, e := range entries {
		top, rest, ok := strings.Cut(e.rel, "/")
		if !ok || rest == "" {
			return
		}
		if i == 0 {
			root = top
		} else if top != root {
			return
		}
	}
	// A root that is itself a known folder stays.
	switch strings.ToLower(root) {
	case "templates", "campaigns_content", "audiences", "lists":
		return
	}
	for _, e := range entries {
		e.rel = strings.TrimPrefix(e.rel, root+"/")
	}
}

// audienceName is the audience a member file belongs to: its folder,
// unless that's the top or a folder of audiences.
func audienceName(rel, fallback string) string {
	dir := path.Dir(rel)
	if dir == "." || dir == "" {
		return fallback
	}
	name := path.Base(dir)
	switch strings.ToLower(name) {
	case "audiences", "lists", "audience", "list":
		return fallback
	}
	return strings.TrimSpace(name)
}

func firstElem(p string) string {
	top, _, _ := strings.Cut(p, "/")
	return strings.ToLower(top)
}

func hasElem(dir, elem string) bool {
	for _, el := range strings.Split(strings.ToLower(dir), "/") {
		if el == elem {
			return true
		}
	}
	return false
}

func stem(base string) string {
	return strings.TrimSuffix(base, path.Ext(base))
}

// ignoredJunk reports files an archiver adds: macOS resource forks and
// dot files.
func ignoredJunk(rel string) bool {
	if strings.HasPrefix(rel, "__MACOSX/") {
		return true
	}
	return strings.HasPrefix(path.Base(rel), ".")
}

// otherExportFolder reports folders of an account export that hold no
// audiences, templates or emails: reports, gallery files, ecommerce, SMS,
// events and appointments. Their CSVs can have names like a member file's.
func otherExportFolder(dir string) bool {
	for _, el := range strings.Split(strings.ToLower(dir), "/") {
		switch el {
		case "reports", "aggregate_activity", "geo_data", "granular_activity", "gallery", "gallery_files",
			"ecommerce", "sms", "events", "appointments":
			return true
		}
	}
	return false
}

// statusFromName finds the member status a file name names, or "" when it
// names none. Longer words are checked first, since "unsubscribed" and
// "nonsubscribed" both contain "subscribed".
func statusFromName(name string) Status {
	n := strings.ToLower(stem(name))
	n = nameSeparators.Replace(n)
	switch {
	case strings.Contains(n, "unsubscribed"):
		return Unsubscribed
	case strings.Contains(n, "nonsubscribed"), strings.Contains(n, "transactional"):
		return Transactional
	case strings.Contains(n, "cleaned"):
		return Cleaned
	case strings.Contains(n, "archived"):
		return Archived
	case strings.Contains(n, "pending"):
		return Pending
	case strings.Contains(n, "subscribed"):
		return Subscribed
	}
	return ""
}

// Replacers are costly to build, so these are built once.
var (
	nameSeparators  = strings.NewReplacer("-", "", "_", "", " ", "", ".", "")
	valueSeparators = strings.NewReplacer("-", "", "_", "", " ", "")
)

// statusFromValue reads a status column's value.
func statusFromValue(v string) (Status, bool) {
	switch valueSeparators.Replace(normalize(v)) {
	case "subscribed":
		return Subscribed, true
	case "unsubscribed":
		return Unsubscribed, true
	case "cleaned":
		return Cleaned, true
	case "pending":
		return Pending, true
	case "nonsubscribed", "transactional":
		return Transactional, true
	case "archived":
		return Archived, true
	}
	return "", false
}

// normalize lowercases, trims and folds runs of spaces and underscores into
// one space, so "OPTIN_TIME" and "Optin Time" match.
func normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "_", " ")
	return strings.Join(strings.Fields(s), " ")
}

// reader carries the limits shared by every file of one export.
type reader struct {
	opts    Options
	budget  int64
	members int
}

// limit counts what's read from r against the export's byte budget.
func (rd *reader) limit(r io.Reader) io.Reader { return &budgetReader{r: r, rd: rd} }

type budgetReader struct {
	r  io.Reader
	rd *reader
}

var errOverBudget = errors.New("over budget")

// Read reads at most one byte past the budget, so that a file ending
// exactly on it still reaches EOF and only a byte more is refused.
func (b *budgetReader) Read(p []byte) (int, error) {
	if b.rd.budget < 0 {
		return 0, errOverBudget
	}
	if int64(len(p)) > b.rd.budget+1 {
		p = p[:b.rd.budget+1]
	}
	n, err := b.r.Read(p)
	b.rd.budget -= int64(n)
	if b.rd.budget < 0 {
		return n, errOverBudget
	}
	return n, err
}

func (rd *reader) tooLarge() error {
	return refuse(ErrTooLarge, "That export is over %s once unpacked, more than one import takes. Export one audience at a time from Mailchimp and import each.", humanBytes(rd.opts.MaxBytes))
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30 && n%(1<<30) == 0:
		return fmt.Sprintf("%d GB", n>>30)
	case n >= 1<<20 && n%(1<<20) == 0:
		return fmt.Sprintf("%d MB", n>>20)
	}
	return fmt.Sprintf("%d bytes", n)
}

// readHTML reads a template or campaign file, or returns nil when it's over
// MaxHTMLFileBytes.
func (rd *reader) readHTML(e *zipEntry) (*HTMLFile, error) {
	if e.f.UncompressedSize64 > MaxHTMLFileBytes {
		return nil, nil
	}
	rc, err := e.f.Open()
	if err != nil {
		return nil, refuse(ErrBadZip, "%s in the ZIP can't be read. Download the export from Mailchimp again.", e.path)
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rd.limit(rc), MaxHTMLFileBytes+1))
	if errors.Is(err, errOverBudget) {
		return nil, rd.tooLarge()
	}
	if err != nil {
		return nil, refuse(ErrBadZip, "%s in the ZIP can't be read. Download the export from Mailchimp again.", e.path)
	}
	if len(data) > MaxHTMLFileBytes {
		return nil, nil
	}
	data = bytes.TrimPrefix(data, utf8BOM)
	return &HTMLFile{HTML: strings.ToValidUTF8(string(data), "�")}, nil
}

var utf8BOM = []byte("\xef\xbb\xbf")

// columns maps a member file's header to the fields it holds.
type columns struct {
	email, first, last, tags, status     int
	optinTime, optinIP, confTime, confIP int
	groups                               map[int]string
	permissions                          []int
	dropped                              []string
}

func mapColumns(header []string, groupNames []string) *columns {
	c := &columns{email: -1, first: -1, last: -1, tags: -1, status: -1,
		optinTime: -1, optinIP: -1, confTime: -1, confIP: -1, groups: map[int]string{}}
	wantGroup := map[string]bool{}
	for _, g := range groupNames {
		wantGroup[normalize(g)] = true
	}
	set := func(field *int, i int) bool {
		if *field >= 0 {
			return false
		}
		*field = i
		return true
	}
	for i, h := range header {
		n := normalize(h)
		used := false
		switch n {
		case "email", "email address", "e-mail", "e-mail address":
			used = set(&c.email, i)
		case "fname", "first name":
			used = set(&c.first, i)
		case "lname", "last name":
			used = set(&c.last, i)
		case "tags":
			used = set(&c.tags, i)
		case "status", "email marketing", "email marketing status", "subscription status", "member status":
			used = set(&c.status, i)
		case "optin time":
			used = set(&c.optinTime, i)
		case "optin ip":
			used = set(&c.optinIP, i)
		case "confirm time":
			used = set(&c.confTime, i)
		case "confirm ip":
			used = set(&c.confIP, i)
		}
		switch {
		case used:
		case wantGroup[n]:
			c.groups[i] = strings.TrimSpace(h)
		case strings.Contains(n, "marketing permission") || strings.HasPrefix(n, "gdpr"):
			c.permissions = append(c.permissions, i)
		case n != "":
			c.dropped = append(c.dropped, strings.TrimSpace(h))
		}
	}
	return c
}

// readMembers reads one member file into aud. status is the file's status
// from its name, "" when its name names none. single is true for an upload
// of one CSV, whose refusals say so.
func (rd *reader) readMembers(r io.Reader, file string, status Status, aud *Audience, single bool) error {
	br := bufio.NewReader(r)
	if p, _ := br.Peek(3); bytes.Equal(p, utf8BOM) {
		_, _ = br.Discard(3)
	}
	cr := csv.NewReader(br)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	header, err := cr.Read()
	if errors.Is(err, errOverBudget) {
		return rd.tooLarge()
	}
	if errors.Is(err, io.EOF) {
		header = nil
	} else if err != nil {
		if single {
			return refuse(ErrNotZipOrCSV, "That file isn't a ZIP or a CSV. Upload the ZIP Mailchimp gave you, or one audience CSV from it.")
		}
		return refuse(ErrNoEmailColumn, "%s has no header row with an Email Address column.", file)
	}
	if len(header) > MaxColumns {
		return refuse(ErrTooManyColumns, "%s has %s columns, more than the %s one import takes. Upload the file as Mailchimp exported it.", file, commas(len(header)), commas(MaxColumns))
	}
	col := mapColumns(header, rd.opts.GroupColumns)
	if col.email < 0 {
		return refuse(ErrNoEmailColumn, "%s has no Email Address column. Upload the file as Mailchimp exported it.", file)
	}
	if status == "" && col.status < 0 {
		return refuse(ErrUnknownStatus, "%s doesn't say which contacts it holds. Mailchimp names each export file for its contacts (subscribed, unsubscribed, cleaned); upload it with that name, or upload the whole ZIP.", file)
	}
	aud.Files = append(aud.Files, file)
	for _, name := range col.dropped {
		aud.DroppedFields = insertSorted(aud.DroppedFields, name)
	}
	for _, i := range sortedKeys(col.groups) {
		aud.GroupColumns = insertSorted(aud.GroupColumns, col.groups[i])
	}
	for _, i := range col.permissions {
		aud.PermissionColumns = insertSorted(aud.PermissionColumns, strings.TrimSpace(header[i]))
	}
	if n := len(aud.DroppedFields) + len(aud.GroupColumns) + len(aud.PermissionColumns); n > MaxColumns {
		return refuse(ErrTooManyColumns, "The files of %s have more than %s different columns between them, more than one import takes. Upload the export as Mailchimp made it.", aud.Name, commas(MaxColumns))
	}

	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if errors.Is(err, errOverBudget) {
			return rd.tooLarge()
		}
		var pe *csv.ParseError
		if errors.As(err, &pe) {
			aud.skip(file, pe.StartLine, SkipBadRow)
			continue
		}
		if err != nil {
			return refuse(ErrBadZip, "%s can't be read to the end. Download the export from Mailchimp again.", file)
		}
		if len(rec) == 0 {
			continue
		}
		line, _ := cr.FieldPos(0)
		cell := func(i int) string {
			if i < 0 || i >= len(rec) {
				return ""
			}
			return strings.TrimSpace(rec[i])
		}
		email := cell(col.email)
		if email == "" {
			if allBlank(rec) {
				continue
			}
			aud.skip(file, line, SkipNoEmail)
			continue
		}
		st := status
		if v := cell(col.status); v != "" {
			s, ok := statusFromValue(v)
			if !ok {
				aud.skip(file, line, SkipUnknownStatus)
				continue
			}
			st = s
		}
		if st == "" {
			aud.skip(file, line, SkipUnknownStatus)
			continue
		}
		rd.members++
		if rd.members > rd.opts.MaxMembers {
			return refuse(ErrTooManyMembers, "That export has more than %s contacts, more than one import takes during the beta. Export one audience at a time from Mailchimp, or ask us to raise the limit.", commas(rd.opts.MaxMembers))
		}
		m := &Member{
			Email: email, FirstName: cell(col.first), LastName: cell(col.last), Status: st,
			OptinTime:   cell(col.optinTime),
			OptinIP:     cell(col.optinIP),
			ConfirmTime: cell(col.confTime),
			ConfirmIP:   cell(col.confIP),
			File:        file, Row: line,
		}
		m.OptinAt = parseTime(m.OptinTime)
		m.ConfirmAt = parseTime(m.ConfirmTime)
		list := func(v string) []string {
			names, cut := splitList(v)
			m.NamesCut = m.NamesCut || cut
			return names
		}
		m.Tags = list(cell(col.tags))
		for _, i := range sortedKeys(col.groups) {
			for _, g := range list(cell(i)) {
				m.Groups = append(m.Groups, Group{Category: col.groups[i], Name: g})
			}
		}
		for _, i := range col.permissions {
			if v := cell(i); v != "" {
				m.PermissionsKnown = true
				m.Permissions = append(m.Permissions, list(v)...)
			}
		}
		aud.Members = append(aud.Members, m)
	}
}

func allBlank(rec []string) bool {
	for _, v := range rec {
		if strings.TrimSpace(v) != "" {
			return false
		}
	}
	return true
}

// splitList splits a cell that lists names, such as tags or groups:
// separated by commas, each name optionally in double quotes so it can hold
// a comma ("Smith, Jones","VIP"). Names are trimmed; blanks and repeats are
// dropped. It keeps at most MaxListNames names, and cut is true when it
// left some out.
func splitList(v string) (names []string, cut bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, false
	}
	// Most cells have no quotes or line breaks, and split on commas as the
	// CSV rules would; reading each cell as CSV costs a buffer per cell.
	plain := !strings.ContainsAny(v, "\"\r\n")
	var cr *csv.Reader
	if !plain {
		cr = csv.NewReader(strings.NewReader(v))
		cr.LazyQuotes = true
		cr.TrimLeadingSpace = true
		cr.FieldsPerRecord = -1
	}
	seen := map[string]bool{}
	for done := false; !done; {
		var rec []string
		var err error
		if plain {
			rec, done = strings.Split(v, ","), true
		} else if rec, err = cr.Read(); errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// A cell the CSV rules can't split is split on commas.
			rec = strings.Split(v, ",")
		}
		for _, s := range rec {
			s = strings.TrimSpace(strings.Trim(strings.TrimSpace(s), `"`))
			if s == "" || seen[s] {
				continue
			}
			if len(names) == MaxListNames {
				return names, true
			}
			seen[s] = true
			// A copy, so a long cell isn't kept for a few names.
			names = append(names, strings.Clone(s))
		}
		if err != nil {
			break
		}
	}
	return names, false
}

// timeLayouts are the layouts parseTime tries. The public docs don't give
// the exports' layout; Mailchimp's own CSVs have been seen with the first.
var timeLayouts = []string{
	"2006-01-02 15:04:05",
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
	"01/02/2006 15:04:05",
	"01/02/2006 15:04",
	"1/2/2006 15:04",
	"01/02/2006",
}

// parseTime reads a timestamp, taken as UTC when it has no zone, or
// returns nil.
func parseTime(v string) *time.Time {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	for _, l := range timeLayouts {
		if t, err := time.ParseInLocation(l, v, time.UTC); err == nil {
			t = t.UTC()
			return &t
		}
	}
	return nil
}

// readCampaignCSV fills in campaigns' subjects, titles and send times from
// the account export's campaigns CSV, matching a row to a content file by
// the campaign's id or title.
func (rd *reader) readCampaignCSV(e *zipEntry, campaigns []*Campaign) error {
	rc, err := e.f.Open()
	if err != nil {
		return refuse(ErrBadZip, "%s in the ZIP can't be read. Download the export from Mailchimp again.", e.path)
	}
	defer rc.Close()
	br := bufio.NewReader(rd.limit(rc))
	if p, _ := br.Peek(3); bytes.Equal(p, utf8BOM) {
		_, _ = br.Discard(3)
	}
	cr := csv.NewReader(br)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	header, err := cr.Read()
	if errors.Is(err, errOverBudget) {
		return rd.tooLarge()
	}
	if err != nil {
		return nil // an empty or unreadable campaigns CSV only loses subjects
	}
	idCol, titleCol, subjCol, sentCol := -1, -1, -1, -1
	for i, h := range header {
		switch normalize(h) {
		case "unique id", "campaign id", "id":
			if idCol < 0 {
				idCol = i
			}
		case "title", "campaign title", "name", "campaign name":
			if titleCol < 0 {
				titleCol = i
			}
		case "subject", "subject line", "email subject":
			if subjCol < 0 {
				subjCol = i
			}
		case "send date", "sent", "send time", "sent at", "send date time", "date sent":
			if sentCol < 0 {
				sentCol = i
			}
		}
	}
	// Only rows naming a campaign whose content the export holds are kept,
	// so a long campaigns CSV costs no memory.
	wanted := map[string]bool{}
	for _, c := range campaigns {
		wanted[normalize(c.Name)] = true
	}
	type row struct{ title, subject, sent string }
	byKey := map[string]row{}
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if errors.Is(err, errOverBudget) {
			return rd.tooLarge()
		}
		if err != nil {
			continue
		}
		get := func(i int) string {
			if i < 0 || i >= len(rec) {
				return ""
			}
			return strings.TrimSpace(rec[i])
		}
		rw := row{title: get(titleCol), subject: get(subjCol), sent: get(sentCol)}
		for _, k := range []string{get(idCol), get(titleCol)} {
			if k = normalize(k); wanted[k] {
				if _, dup := byKey[k]; !dup {
					byKey[k] = row{title: strings.Clone(rw.title), subject: strings.Clone(rw.subject), sent: rw.sent}
				}
			}
		}
	}
	for _, c := range campaigns {
		rw, ok := byKey[normalize(c.Name)]
		if !ok {
			continue
		}
		c.Title, c.Subject, c.SentAt = rw.title, rw.subject, parseTime(rw.sent)
	}
	return nil
}

// insertSorted adds name to the sorted list have unless it's there.
func insertSorted(have []string, name string) []string {
	i, found := slices.BinarySearch(have, name)
	if found {
		return have
	}
	return slices.Insert(have, i, name)
}

func sortedKeys(m map[int]string) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func commas(n int) string {
	s := fmt.Sprint(n)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}
