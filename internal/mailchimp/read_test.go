package mailchimp

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// zipDir zips a testdata folder in memory, each path under prefix.
func zipDir(t *testing.T, dir, prefix string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		w, err := zw.Create(prefix + filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// zipFiles zips the named files in memory, in order.
func zipFiles(t *testing.T, files ...[2]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		w, err := zw.Create(f[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(f[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func read(t *testing.T, data []byte, name string, opts Options) (*Bundle, error) {
	t.Helper()
	return Read(bytes.NewReader(data), int64(len(data)), name, opts)
}

func mustRead(t *testing.T, data []byte, name string, opts Options) *Bundle {
	t.Helper()
	b, err := read(t, data, name, opts)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return b
}

func wantRefused(t *testing.T, err error, code string) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("err = %v, want a refusal %s", err, code)
	}
	if e.Code != code {
		t.Fatalf("code = %s (%s), want %s", e.Code, e.Message, code)
	}
	if e.Message == "" {
		t.Error("a refusal needs a message")
	}
}

func audience(t *testing.T, b *Bundle, name string) *Audience {
	t.Helper()
	for _, a := range b.Audiences {
		if a.Name == name {
			return a
		}
	}
	t.Fatalf("no audience %q in %v", name, audienceNames(b))
	return nil
}

func audienceNames(b *Bundle) []string {
	var names []string
	for _, a := range b.Audiences {
		names = append(names, a.Name)
	}
	return names
}

func member(t *testing.T, a *Audience, email string) *Member {
	t.Helper()
	for _, m := range a.Members {
		if m.Email == email {
			return m
		}
	}
	t.Fatalf("no member %s in %s", email, a.Name)
	return nil
}

func TestAccountExport(t *testing.T) {
	b := mustRead(t, zipDir(t, "testdata/account_export", ""), "mailchimp_export.zip",
		Options{GroupColumns: []string{"interests"}})
	if b.Source != SourceAccountExport {
		t.Errorf("Source = %s", b.Source)
	}
	if got := audienceNames(b); !reflect.DeepEqual(got, []string{"Newsletter", "Recipe Club"}) {
		t.Fatalf("audiences = %v", got)
	}
	if b.Members() != 7 {
		t.Errorf("Members() = %d, want 7", b.Members())
	}

	news := audience(t, b, "Newsletter")
	if len(news.Files) != 4 {
		t.Errorf("Newsletter files = %v", news.Files)
	}
	ada := member(t, news, "ada@example.com")
	if ada.Status != Subscribed || ada.FirstName != "Ada" || ada.LastName != "Lovelace" {
		t.Errorf("ada = %+v", ada)
	}
	if !reflect.DeepEqual(ada.Tags, []string{"Customer", "VIP"}) {
		t.Errorf("ada tags = %q", ada.Tags)
	}
	if want := []Group{{"Interests", "Recipes"}, {"Interests", "Travel"}}; !reflect.DeepEqual(ada.Groups, want) {
		t.Errorf("ada groups = %v", ada.Groups)
	}
	if ada.OptinTime != "2025-03-01 10:00:00" || ada.OptinIP != "192.0.2.10" || ada.ConfirmIP != "192.0.2.10" {
		t.Errorf("ada opt-in = %q %q %q", ada.OptinTime, ada.OptinIP, ada.ConfirmIP)
	}
	if ada.OptinAt == nil || !ada.OptinAt.Equal(time.Date(2025, 3, 1, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("ada OptinAt = %v", ada.OptinAt)
	}
	if ada.ConfirmAt == nil || !ada.ConfirmAt.Equal(time.Date(2025, 3, 1, 10, 5, 0, 0, time.UTC)) {
		t.Errorf("ada ConfirmAt = %v", ada.ConfirmAt)
	}
	if !ada.PermissionsKnown || !reflect.DeepEqual(ada.Permissions, []string{"Email"}) || ada.RefusesEmail() {
		t.Errorf("ada permissions = %v %v", ada.PermissionsKnown, ada.Permissions)
	}
	if ada.File != "audiences/Newsletter/subscribed_members_export_0a1b2c.csv" || ada.Row != 2 {
		t.Errorf("ada from %s row %d", ada.File, ada.Row)
	}

	grace := member(t, news, "grace@example.com")
	if !reflect.DeepEqual(grace.Tags, []string{"Smith, Jones"}) {
		t.Errorf("a quoted tag keeps its comma: %q", grace.Tags)
	}
	if grace.ConfirmTime != "" || grace.ConfirmAt != nil {
		t.Errorf("blank confirm time = %q %v", grace.ConfirmTime, grace.ConfirmAt)
	}
	if !reflect.DeepEqual(grace.Permissions, []string{"Email", "Direct Mail"}) {
		t.Errorf("grace permissions = %q", grace.Permissions)
	}
	if s := member(t, news, "linus@example.com").Status; s != Unsubscribed {
		t.Errorf("linus = %s", s)
	}
	if s := member(t, news, "bounce@example.com").Status; s != Cleaned {
		t.Errorf("bounce = %s", s)
	}
	if s := member(t, news, "receipt@example.com").Status; s != Transactional {
		t.Errorf("receipt = %s", s)
	}
	if want := []SkippedRow{{File: "audiences/Newsletter/subscribed_members_export_0a1b2c.csv", Row: 4, Reason: SkipNoEmail}}; !reflect.DeepEqual(news.Skipped, want) {
		t.Errorf("skipped = %+v", news.Skipped)
	}
	for _, f := range []string{"MEMBER_RATING", "LATITUDE", "NOTES", "LEID", "EUID", "UNSUB_TIME", "CLEAN_TIME"} {
		if !slices.Contains(news.DroppedFields, f) {
			t.Errorf("dropped fields %v lack %s", news.DroppedFields, f)
		}
	}
	for _, f := range []string{"Email Address", "First Name", "TAGS", "OPTIN_TIME", "Interests", "Marketing Permissions", "Phone Number"} {
		if slices.Contains(news.DroppedFields, f) {
			t.Errorf("%s is read, not dropped", f)
		}
	}
	// Phone Number is a merge field, kept with its values (MG-132).
	if !slices.Equal(news.MergeFields, []string{"Phone Number"}) {
		t.Errorf("merge fields = %v", news.MergeFields)
	}
	if m := member(t, news, "ada@example.com").Merge; !slices.Equal(m, []string{"555-0100"}) {
		t.Errorf("ada's merge values = %q", m)
	}
	if m := member(t, news, "grace@example.com").Merge; m != nil {
		t.Errorf("grace's blank phone = %q", m)
	}
	if !slices.IsSorted(news.DroppedFields) {
		t.Errorf("dropped fields aren't sorted: %v", news.DroppedFields)
	}
	if !reflect.DeepEqual(news.GroupColumns, []string{"Interests"}) || !reflect.DeepEqual(news.PermissionColumns, []string{"Marketing Permissions"}) {
		t.Errorf("group %v, permission %v columns", news.GroupColumns, news.PermissionColumns)
	}

	club := audience(t, b, "Recipe Club")
	if got := member(t, club, "ada@example.com"); got.Status != Subscribed || !reflect.DeepEqual(got.Tags, []string{"Baker"}) {
		t.Errorf("ada in the club = %+v", got)
	}
	if member(t, club, "grace@example.com").Status != Unsubscribed {
		t.Error("grace unsubscribed from the club")
	}

	if len(b.Templates) != 1 || b.Templates[0].Name != "Monthly_newsletter" || !strings.Contains(b.Templates[0].HTML, "*|FNAME|*") {
		t.Errorf("templates = %+v", b.Templates)
	}
	if len(b.Campaigns) != 2 {
		t.Fatalf("campaigns = %+v", b.Campaigns)
	}
	sale := b.Campaigns[0]
	if sale.Name != "abc123" || sale.Subject != "Spring sale: 20% off, this week" || sale.Title != "Spring sale" ||
		sale.SentAt == nil || !sale.SentAt.Equal(time.Date(2025, 4, 1, 14, 0, 0, 0, time.UTC)) ||
		!strings.Contains(sale.Text, "*|UNSUB|*") || !strings.Contains(sale.HTML, "<h1>Spring sale</h1>") {
		t.Errorf("sale = %+v", sale)
	}
	if b.Campaigns[1].Subject != "Welcome aboard" || b.Campaigns[1].Text != "" {
		t.Errorf("welcome = %+v", b.Campaigns[1])
	}

	var ignored []string
	for _, f := range b.Ignored {
		ignored = append(ignored, f.Path+" "+f.Reason)
	}
	want := []string{"events.csv unknown_file", "gallery/logo.gif unknown_file", "reports/granular_activity/unsubscribed.csv unknown_file"}
	if !reflect.DeepEqual(ignored, want) {
		t.Errorf("ignored = %v, want %v", ignored, want)
	}
}

func TestAccountExportWithoutGroupColumns(t *testing.T) {
	b := mustRead(t, zipDir(t, "testdata/account_export", ""), "x.zip", Options{})
	news := audience(t, b, "Newsletter")
	if ada := member(t, news, "ada@example.com"); ada.Groups != nil {
		t.Errorf("groups without GroupColumns = %v", ada.Groups)
	}
	if !slices.Contains(news.MergeFields, "Interests") {
		t.Errorf("an unnamed group column is read as a merge field: %v", news.MergeFields)
	}
}

func TestAccountExportInARootFolder(t *testing.T) {
	b := mustRead(t, zipDir(t, "testdata/account_export", "export-2026-11-20/"), "x.zip", Options{})
	if b.Source != SourceAccountExport || !reflect.DeepEqual(audienceNames(b), []string{"Newsletter", "Recipe Club"}) || len(b.Templates) != 1 {
		t.Errorf("source %s, audiences %v, %d templates", b.Source, audienceNames(b), len(b.Templates))
	}
	if p := b.Templates[0].Path; p != "export-2026-11-20/templates/Monthly_newsletter.html" {
		t.Errorf("paths keep the root folder: %s", p)
	}
}

func TestAudienceExport(t *testing.T) {
	b := mustRead(t, zipDir(t, "testdata/audience_export", ""), "audience.zip",
		Options{AudienceName: "Main list", GroupColumns: []string{"Interests"}})
	if b.Source != SourceAudienceExport || !reflect.DeepEqual(audienceNames(b), []string{"Main list"}) {
		t.Fatalf("source %s, audiences %v", b.Source, audienceNames(b))
	}
	a := b.Audiences[0]
	for email, want := range map[string]Status{"sub@example.com": Subscribed, "unsub@example.com": Unsubscribed,
		"clean@example.com": Cleaned, "trans@example.com": Transactional} {
		if got := member(t, a, email).Status; got != want {
			t.Errorf("%s = %s, want %s", email, got, want)
		}
	}
	sub := member(t, a, "sub@example.com")
	if sub.OptinAt == nil || !sub.OptinAt.Equal(time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Errorf("RFC 3339 opt-in = %v", sub.OptinAt)
	}
	if !reflect.DeepEqual(sub.Groups, []Group{{"Interests", "Recipes"}}) || sub.PermissionsKnown || sub.RefusesEmail() {
		t.Errorf("sub = %+v", sub)
	}
}

func TestAudienceExportDefaultName(t *testing.T) {
	b := mustRead(t, zipDir(t, "testdata/audience_export", ""), "audience.zip", Options{})
	if !reflect.DeepEqual(audienceNames(b), []string{DefaultAudienceName}) {
		t.Errorf("audiences = %v", audienceNames(b))
	}
}

func TestSingleCSV(t *testing.T) {
	data, err := os.ReadFile("testdata/audience_export/cleaned_members_export_77aa.csv")
	if err != nil {
		t.Fatal(err)
	}
	b := mustRead(t, data, `C:\Downloads\cleaned_members_export_77aa.csv`, Options{})
	if b.Source != SourceAudienceCSV || len(b.Audiences) != 1 || b.Audiences[0].Name != DefaultAudienceName {
		t.Fatalf("bundle = %+v", b)
	}
	a := b.Audiences[0]
	if len(a.Members) != 1 || a.Members[0].Status != Cleaned || a.Members[0].File != "cleaned_members_export_77aa.csv" {
		t.Errorf("members = %+v", a.Members)
	}
}

func TestStatusColumn(t *testing.T) {
	data, err := os.ReadFile("testdata/status_column.csv")
	if err != nil {
		t.Fatal(err)
	}
	// The status column wins over the file's name, and a byte-order mark
	// before the header is dropped.
	b := mustRead(t, data, "subscribed_export.csv", Options{})
	a := b.Audiences[0]
	for email, want := range map[string]Status{"P@Example.com": Pending, "A@example.com": Archived, "S@example.com": Subscribed} {
		if got := member(t, a, email).Status; got != want {
			t.Errorf("%s = %s, want %s", email, got, want)
		}
	}
	if p := member(t, a, "P@Example.com"); p.FirstName != "Pen" || p.LastName != "Ding" {
		t.Errorf("merge tags FNAME and LNAME: %+v", p)
	}
	if want := []SkippedRow{{File: "subscribed_export.csv", Row: 5, Reason: SkipUnknownStatus}}; !reflect.DeepEqual(a.Skipped, want) {
		t.Errorf("skipped = %+v", a.Skipped)
	}
	// With no status in its name, the column alone decides.
	b = mustRead(t, data, "contacts.csv", Options{})
	if len(b.Audiences[0].Members) != 3 {
		t.Errorf("members = %d", len(b.Audiences[0].Members))
	}
}

func TestStatusFromName(t *testing.T) {
	for name, want := range map[string]Status{
		"subscribed_members_export_1.csv":    Subscribed,
		"unsubscribed_members_export_1.csv":  Unsubscribed,
		"cleaned_members_export_1.csv":       Cleaned,
		"nonsubscribed_members_export_1.csv": Transactional,
		"non-subscribed members.csv":         Transactional,
		"transactional_members_export_1.csv": Transactional,
		"archived_members_export_1.csv":      Archived,
		"pending_members.csv":                Pending,
		"Subscribed Members Export (2).CSV":  Subscribed,
		"members_export_1.csv":               "",
		"campaigns.csv":                      "",
	} {
		if got := statusFromName(name); got != want {
			t.Errorf("statusFromName(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestSplitList(t *testing.T) {
	for in, want := range map[string][]string{
		"":                           nil,
		"VIP":                        {"VIP"},
		"VIP, Customer":              {"VIP", "Customer"},
		`"VIP","Customer"`:           {"VIP", "Customer"},
		`"Smith, Jones",VIP`:         {"Smith, Jones", "VIP"},
		`"Smith, Jones", "VIP", VIP`: {"Smith, Jones", "VIP"},
		" a ,, b ":                   {"a", "b"},
		`odd "quote, here`:           {`odd "quote`, "here"},
	} {
		if got, cut := splitList(in); !reflect.DeepEqual(got, want) || cut {
			t.Errorf("splitList(%q) = %q, %v, want %q", in, got, cut, want)
		}
	}
}

func TestSplitListKeepsAtMostMaxListNames(t *testing.T) {
	names := make([]string, MaxListNames+100)
	for i := range names {
		names[i] = fmt.Sprintf("t%d", i)
	}
	got, cut := splitList(strings.Join(names, ","))
	if len(got) != MaxListNames || !cut || got[0] != "t0" || got[MaxListNames-1] != names[MaxListNames-1] {
		t.Errorf("kept %d names, cut %v", len(got), cut)
	}
	// Exactly MaxListNames is not cut.
	if got, cut := splitList(strings.Join(names[:MaxListNames], ",")); len(got) != MaxListNames || cut {
		t.Errorf("kept %d names, cut %v", len(got), cut)
	}

	csv := "Email Address,TAGS\na@example.com,\"" + strings.Join(names, ",") + "\"\nb@example.com,VIP\n"
	a := mustRead(t, []byte(csv), "subscribed.csv", Options{}).Audiences[0]
	if m := member(t, a, "a@example.com"); len(m.Tags) != MaxListNames || !m.NamesCut {
		t.Errorf("a: %d tags, NamesCut %v", len(m.Tags), m.NamesCut)
	}
	if m := member(t, a, "b@example.com"); m.NamesCut {
		t.Error("b: NamesCut set")
	}
}

// A flood of rows that can't be read as members, which compresses to
// almost nothing in a ZIP, lists only the first MaxSkippedRows and counts
// the rest.
func TestSkippedRowsAreBounded(t *testing.T) {
	const rows = 50_000
	csv := "Email Address,X\n" + strings.Repeat(",a\n", rows) + "a@example.com,b\n"
	data := zipFiles(t, [2]string{"subscribed_members.csv", csv})
	a := mustRead(t, data, "x.zip", Options{}).Audiences[0]
	if len(a.Skipped) != MaxSkippedRows {
		t.Errorf("listed %d skipped rows, want %d", len(a.Skipped), MaxSkippedRows)
	}
	if a.SkippedByReason[SkipNoEmail] != rows || a.SkippedRows() != rows {
		t.Errorf("counted %v, want %d", a.SkippedByReason, rows)
	}
	if a.Skipped[0].Row != 2 || a.Skipped[MaxSkippedRows-1].Row != MaxSkippedRows+1 {
		t.Errorf("first and last listed rows = %d, %d", a.Skipped[0].Row, a.Skipped[MaxSkippedRows-1].Row)
	}
	if len(a.Members) != 1 {
		t.Errorf("members = %d", len(a.Members))
	}
}

func wideHeader(n int) string {
	cols := make([]string, n)
	cols[0] = "Email Address"
	for i := 1; i < n; i++ {
		cols[i] = fmt.Sprintf("FIELD%d", i)
	}
	return strings.Join(cols, ",")
}

func TestTooManyColumns(t *testing.T) {
	// MaxColumns is taken.
	// The first MaxMergeFields are merge fields; the rest are dropped.
	b := mustRead(t, []byte(wideHeader(MaxColumns)+"\na@example.com\n"), "subscribed.csv", Options{})
	if got := len(b.Audiences[0].DroppedFields) + len(b.Audiences[0].MergeFields); got != MaxColumns-1 ||
		len(b.Audiences[0].MergeFields) != MaxMergeFields || b.Audiences[0].MergeFields[0] != "FIELD1" {
		t.Errorf("dropped %d fields, %d merge fields", len(b.Audiences[0].DroppedFields), len(b.Audiences[0].MergeFields))
	}
	if !slices.IsSorted(b.Audiences[0].DroppedFields) {
		t.Error("dropped fields aren't sorted")
	}

	// One more is refused, and a very wide header is refused quickly
	// rather than read column by column.
	for _, n := range []int{MaxColumns + 1, 1_000_000} {
		// Only the read is timed, not making the header.
		data := []byte(wideHeader(n) + "\na@example.com\n")
		start := time.Now()
		_, err := read(t, data, "subscribed.csv", Options{})
		wantRefused(t, err, ErrTooManyColumns)
		if d := time.Since(start); d > 5*time.Second {
			t.Errorf("%d columns took %v", n, d)
		}
	}

	// Files of one audience can't add up to more than MaxColumns
	// different columns between them.
	half := func(from int) string {
		cols := []string{"Email Address"}
		for i := range MaxColumns/2 + 1 {
			cols = append(cols, fmt.Sprintf("F%d", from+i))
		}
		return strings.Join(cols, ",") + "\na@example.com\n"
	}
	data := zipFiles(t,
		[2]string{"subscribed.csv", half(0)},
		[2]string{"unsubscribed.csv", half(MaxColumns)},
	)
	_, err := read(t, data, "x.zip", Options{})
	wantRefused(t, err, ErrTooManyColumns)

	// The same columns in every file count once.
	data = zipFiles(t,
		[2]string{"subscribed.csv", half(0)},
		[2]string{"unsubscribed.csv", half(0)},
		[2]string{"cleaned.csv", half(0)},
	)
	if b := mustRead(t, data, "x.zip", Options{}); len(b.Audiences[0].DroppedFields)+len(b.Audiences[0].MergeFields) != MaxColumns/2+1 {
		t.Errorf("dropped %d fields", len(b.Audiences[0].DroppedFields))
	}
}

func TestByteBudgetIsExact(t *testing.T) {
	csv := []byte("Email Address\na@example.com\nb@example.com\n")
	if b := mustRead(t, csv, "subscribed.csv", Options{MaxBytes: int64(len(csv))}); b.Members() != 2 {
		t.Errorf("members = %d", b.Members())
	}
	_, err := read(t, csv, "subscribed.csv", Options{MaxBytes: int64(len(csv)) - 1})
	wantRefused(t, err, ErrTooLarge)

	data := zipFiles(t, [2]string{"subscribed.csv", string(csv)}, [2]string{"cleaned.csv", string(csv)})
	if b := mustRead(t, data, "x.zip", Options{MaxBytes: 2 * int64(len(csv))}); b.Members() != 4 {
		t.Errorf("members = %d", b.Members())
	}
	_, err = read(t, data, "x.zip", Options{MaxBytes: 2*int64(len(csv)) - 1})
	wantRefused(t, err, ErrTooLarge)
}

func TestParseTime(t *testing.T) {
	want := time.Date(2025, 3, 1, 10, 0, 0, 0, time.UTC)
	for _, v := range []string{"2025-03-01 10:00:00", "2025-03-01T10:00:00Z", "2025-03-01T05:00:00-05:00", "2025-03-01T10:00:00", "03/01/2025 10:00"} {
		if got := parseTime(v); got == nil || !got.Equal(want) {
			t.Errorf("parseTime(%q) = %v", v, got)
		}
	}
	for _, v := range []string{"", "  ", "yesterday", "2025-13-40 00:00:00"} {
		if got := parseTime(v); got != nil {
			t.Errorf("parseTime(%q) = %v, want nil", v, got)
		}
	}
}

func TestRefusesEmail(t *testing.T) {
	for _, c := range []struct {
		m    Member
		want bool
	}{
		{Member{}, false},
		{Member{PermissionsKnown: true, Permissions: []string{"Email"}}, false},
		{Member{PermissionsKnown: true, Permissions: []string{"Direct Mail", "e-mail newsletters"}}, false},
		{Member{PermissionsKnown: true, Permissions: []string{"Direct Mail"}}, true},
		{Member{PermissionsKnown: true}, true},
	} {
		if got := c.m.RefusesEmail(); got != c.want {
			t.Errorf("RefusesEmail(%v) = %v", c.m.Permissions, got)
		}
	}
}

func TestMissingColumns(t *testing.T) {
	// Only an email column: the rest are blank.
	b := mustRead(t, []byte("Email\nmin@example.com\n"), "subscribed.csv", Options{})
	m := b.Audiences[0].Members[0]
	if m.Email != "min@example.com" || m.FirstName != "" || m.Tags != nil || m.OptinAt != nil || len(b.Audiences[0].DroppedFields) != 0 ||
		len(b.Audiences[0].MergeFields) != 0 || m.Merge != nil {
		t.Errorf("member = %+v", m)
	}
	// A short row reads its missing cells as blank.
	b = mustRead(t, []byte("Email Address,First Name,Last Name,TAGS\nshort@example.com,Shorty\n"), "subscribed.csv", Options{})
	if m := b.Audiences[0].Members[0]; m.FirstName != "Shorty" || m.LastName != "" {
		t.Errorf("short row = %+v", m)
	}
	// A blank line is passed over without a skip.
	b = mustRead(t, []byte("Email Address\na@example.com\n,\nb@example.com\n"), "subscribed.csv", Options{})
	if len(b.Audiences[0].Members) != 2 || len(b.Audiences[0].Skipped) != 0 {
		t.Errorf("members %d, skipped %+v", len(b.Audiences[0].Members), b.Audiences[0].Skipped)
	}
	// A header with no rows is an empty audience, not an error.
	b = mustRead(t, []byte("Email Address\n"), "subscribed.csv", Options{})
	if len(b.Audiences[0].Members) != 0 {
		t.Error("no members expected")
	}
}

func TestRefusals(t *testing.T) {
	member := [2]string{"subscribed_members_export_1.csv", "Email Address\na@example.com\n"}
	for name, c := range map[string]struct {
		data []byte
		file string
		opts Options
		code string
	}{
		"path traversal":   {zipFiles(t, member, [2]string{"../../etc/subscribed.csv", "Email Address\n"}), "x.zip", Options{}, ErrUnsafePath},
		"traversal inside": {zipFiles(t, [2]string{"audiences/../../x.csv", "x"}), "x.zip", Options{}, ErrUnsafePath},
		"absolute path":    {zipFiles(t, [2]string{"/tmp/subscribed.csv", "Email Address\n"}), "x.zip", Options{}, ErrUnsafePath},
		"windows path":     {zipFiles(t, [2]string{`..\subscribed.csv`, "Email Address\n"}), "x.zip", Options{}, ErrUnsafePath},
		"drive letter":     {zipFiles(t, [2]string{"C:/subscribed.csv", "Email Address\n"}), "x.zip", Options{}, ErrUnsafePath},
		"symlink":          {symlinkZip(t), "x.zip", Options{}, ErrUnsafePath},
		"too large":        {zipFiles(t, [2]string{"subscribed.csv", "Email Address\n" + strings.Repeat("a@example.com\n", 100)}), "x.zip", Options{MaxBytes: 500}, ErrTooLarge},
		"large CSV":        {[]byte("Email Address\n" + strings.Repeat("a@example.com\n", 100)), "subscribed.csv", Options{MaxBytes: 500}, ErrTooLarge},
		"too many members": {zipDir(t, "testdata/account_export", ""), "x.zip", Options{MaxMembers: 6}, ErrTooManyMembers},
		"binary":           {[]byte{0x89, 'P', 'N', 'G', 0, 0, 1, 2}, "logo.png", Options{}, ErrNotZipOrCSV},
		"empty file":       {nil, "subscribed.csv", Options{}, ErrNotZipOrCSV},
		"broken zip":       {append([]byte("PK\x03\x04"), bytes.Repeat([]byte{7}, 64)...), "x.zip", Options{}, ErrBadZip},
		"nothing in zip":   {zipFiles(t, [2]string{"gallery/logo.gif", "GIF89a"}, [2]string{"notes.txt", "hi"}), "x.zip", Options{}, ErrNothingToRead},
		"no email column":  {[]byte("Name,Phone\nAda,555\n"), "subscribed.csv", Options{}, ErrNoEmailColumn},
		"no email in zip":  {zipFiles(t, [2]string{"subscribed.csv", "Name\nAda\n"}), "x.zip", Options{}, ErrNoEmailColumn},
		"unknown status":   {[]byte("Email Address\na@example.com\n"), "contacts.csv", Options{}, ErrUnknownStatus},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := read(t, c.data, c.file, c.opts)
			wantRefused(t, err, c.code)
		})
	}
}

func symlinkZip(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	h := &zip.FileHeader{Name: "templates/link.html"}
	h.SetMode(fs.ModeSymlink | 0o777)
	w, err := zw.CreateHeader(h)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte("/etc/passwd"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestMemberLimitIsExact(t *testing.T) {
	b := mustRead(t, zipDir(t, "testdata/account_export", ""), "x.zip", Options{MaxMembers: 7})
	if b.Members() != 7 {
		t.Errorf("Members() = %d", b.Members())
	}
}

func TestIgnoredAndOversizedFiles(t *testing.T) {
	big := "<html>" + strings.Repeat("x", MaxHTMLFileBytes) + "</html>"
	b := mustRead(t, zipFiles(t,
		[2]string{"templates/big.html", big},
		[2]string{"templates/ok.html", "<p>ok</p>"},
		[2]string{"templates/readme.pdf", "%PDF"},
		[2]string{"__MACOSX/templates/._ok.html", "junk"},
		[2]string{"templates/.DS_Store", "junk"},
		[2]string{"campaigns_content/zzz.txt", "text with no HTML"},
	), "x.zip", Options{})
	if len(b.Templates) != 1 || b.Templates[0].Name != "ok" {
		t.Errorf("templates = %+v", b.Templates)
	}
	got := map[string]string{}
	for _, f := range b.Ignored {
		got[f.Path] = f.Reason
	}
	want := map[string]string{
		"templates/big.html":           IgnoreTooLarge,
		"templates/readme.pdf":         IgnoreUnknown,
		"__MACOSX/templates/._ok.html": IgnoreUnknown,
		"templates/.DS_Store":          IgnoreUnknown,
		"campaigns_content/zzz.txt":    IgnoreUnknown,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ignored = %v", got)
	}
	if b.Source != SourceAccountExport || len(b.Audiences) != 0 {
		t.Errorf("source %s, audiences %d", b.Source, len(b.Audiences))
	}
}

func TestUnreadableRow(t *testing.T) {
	// A quote inside an unquoted cell is taken as written (lazy quotes); a
	// row too broken to read is skipped and the file read on.
	b := mustRead(t, []byte("Email Address,First Name\nq@example.com,O\"Brien\nok@example.com,Ok\n"), "subscribed.csv", Options{})
	a := b.Audiences[0]
	if len(a.Members) != 2 || a.Members[0].FirstName != `O"Brien` {
		t.Errorf("members = %+v, skipped %+v", a.Members, a.Skipped)
	}
}

func TestMergeFields(t *testing.T) {
	// Mailchimp's own columns are in capitals; a merge field with the same
	// words isn't one of them. Merge fields keep the header's order, and a
	// later file's new ones come after.
	data := zipFiles(t,
		[2]string{"subscribed.csv", "Email Address,Region,Company,REGION,MEMBER_RATING\na@example.com, North ,Acme,GA,3\nb@example.com,,,GA,2\n"},
		[2]string{"unsubscribed.csv", "Email Address,Company,Birthday,Region\nc@example.com,Initech,01/02,South\n"},
	)
	b := mustRead(t, data, "x.zip", Options{})
	a := b.Audiences[0]
	if want := []string{"Region", "Company", "Birthday"}; !slices.Equal(a.MergeFields, want) {
		t.Errorf("merge fields = %v, want %v", a.MergeFields, want)
	}
	if want := []string{"MEMBER_RATING", "REGION"}; !slices.Equal(a.DroppedFields, want) {
		t.Errorf("dropped = %v, want %v", a.DroppedFields, want)
	}
	for _, c := range []struct {
		email string
		want  []string
	}{
		{"a@example.com", []string{"North", "Acme"}},
		{"b@example.com", nil},
		{"c@example.com", []string{"South", "Initech", "01/02"}},
	} {
		if got := member(t, a, c.email).Merge; !slices.Equal(got, c.want) {
			t.Errorf("%s's merge values = %q, want %q", c.email, got, c.want)
		}
	}
	for h, want := range map[string]bool{"REGION": true, "Region": false, "LAST_CHANGED": true, "Last Changed": false, "PHONE": false} {
		if got := SystemColumn(h); got != want {
			t.Errorf("SystemColumn(%q) = %v", h, got)
		}
	}
}

// MG-134: a fetched bundle names each merge field's tag in MergeTagsFile;
// one without Tag and Name columns, or for no audience, is ignored.
func TestMergeTagsFile(t *testing.T) {
	members := "Email Address,Company,Fav bread\na@example.com,Acme,Rye\n"
	b := mustRead(t, zipFiles(t,
		[2]string{"audiences/News/subscribed_members.csv", members},
		[2]string{"audiences/News/" + MergeTagsFile, "Tag,Name\nmmerge3,Company\nBREAD,Other\n"},
		[2]string{"audiences/Gone/" + MergeTagsFile, "Tag,Name\nX,Y\n"},
		[2]string{"audiences/Club/subscribed_members.csv", members},
		[2]string{"audiences/Club/" + MergeTagsFile, "Something,Else\n"},
	), "x.zip", Options{})
	if got := audience(t, b, "News").MergeTags; !slices.Equal(got, []string{"MMERGE3", ""}) {
		t.Errorf("News merge tags = %q", got)
	}
	if got := audience(t, b, "Club").MergeTags; got != nil {
		t.Errorf("Club merge tags = %q", got)
	}
	var ignored []string
	for _, f := range b.Ignored {
		ignored = append(ignored, f.Path)
	}
	if want := []string{"audiences/Club/" + MergeTagsFile, "audiences/Gone/" + MergeTagsFile}; !slices.Equal(ignored, want) {
		t.Errorf("ignored = %v", ignored)
	}
}
