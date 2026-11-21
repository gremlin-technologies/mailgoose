package contacts

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// snapshot is what an export keeps of each of an account's contacts: names,
// status and tags, by address.
func (h *harness) snapshot(t *testing.T, account string) map[string]string {
	t.Helper()
	cs, err := h.svc.List(context.Background(), account, Filter{}, nil, 1000)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, c := range cs {
		out[c.Email] = fmt.Sprintf("%q %q %s %v", deref(c.FirstName), deref(c.LastName), c.Status, tagNames(c))
	}
	return out
}

func (h *harness) export(t *testing.T, account string, f Filter) [][]string {
	t.Helper()
	var b bytes.Buffer
	n, err := h.svc.ExportCSV(context.Background(), account, f, &b)
	if err != nil {
		t.Fatal(err)
	}
	recs, err := csv.NewReader(&b).ReadAll()
	if err != nil {
		t.Fatalf("export isn't CSV: %v\n%s", err, b.String())
	}
	if n != len(recs)-1 {
		t.Errorf("ExportCSV said %d, wrote %d rows", n, len(recs)-1)
	}
	return recs
}

func (h *harness) setStatus(t *testing.T, id, status string) {
	t.Helper()
	if _, err := h.svc.DB.Exec(context.Background(), `UPDATE contacts SET status = $2 WHERE id = $1`, id, status); err != nil {
		t.Fatal(err)
	}
}

// The ticket's round trip: an export imports back as the same contacts and
// tags.
func TestExportImportsBackAsTheSameContacts(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	pat, err := h.svc.Create(ctx, h.acc, CreateInput{Email: "pat@example.com", FirstName: str("Pat"), LastName: str("=Lee"), Status: StatusSubscribed})
	if err != nil {
		t.Fatal(err)
	}
	sam := h.add(t, "sam@example.com", StatusUnconfirmed)
	una := h.add(t, "una@example.com", StatusSubscribed)
	h.setStatus(t, una.ID, StatusUnsubscribed)
	bo := h.add(t, "bo@example.com", StatusSubscribed)
	h.setStatus(t, bo.ID, StatusBounced)
	h.importFile(t, ImportInput{ListSource: ListSourceForm, CSV: []byte("email,first name\nimp@example.com,Ima\n"), PermissionConfirmed: true})
	vip, plus := h.tag(t, "VIP"), h.tag(t, "+plus")
	for _, tc := range []struct {
		tag string
		ids []string
	}{{vip.ID, []string{pat.ID, sam.ID}}, {plus.ID, []string{sam.ID, bo.ID}}} {
		if _, err := h.svc.TagContacts(ctx, h.acc, tc.tag, Selection{IDs: tc.ids}); err != nil {
			t.Fatal(err)
		}
	}

	recs := h.export(t, h.acc, Filter{})
	if !slices.Equal(recs[0], ExportHeader) || len(recs) != 6 {
		t.Fatalf("export = %q", recs)
	}
	if got := strings.Join(recs[1], "|"); !strings.HasPrefix(got, "pat@example.com|Pat|'=Lee|subscribed|VIP|api|") || !strings.HasSuffix(got, "|") {
		t.Errorf("pat's row = %s", got)
	}
	if got := recs[2][4]; got != "'+plus, VIP" {
		t.Errorf("sam's tags = %q", got)
	}
	if got := recs[5]; got[0] != "imp@example.com" || got[5] != "import" || got[8] != "true" {
		t.Errorf("imported contact's row = %q", got)
	}
	if got := h.export(t, h.acc, Filter{TagID: vip.ID}); len(got) != 3 || got[1][0] != "pat@example.com" || got[2][0] != "sam@example.com" {
		t.Errorf("export of one tag = %q", got)
	}

	var b bytes.Buffer
	if _, err := h.svc.ExportCSV(ctx, h.acc, Filter{}, &b); err != nil {
		t.Fatal(err)
	}
	im, err := h.svc.CreateImport(ctx, h.acc2, ImportInput{ListSource: ListSourceForm, CSV: b.Bytes(), FileName: "contacts.csv", PermissionConfirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	h.runImports(t)
	if im, err = h.svc.GetImport(ctx, h.acc2, im.ID); err != nil || im.Imported != 5 || im.Skipped != 0 {
		t.Fatalf("re-import = %+v, %v", im, err)
	}
	before, after := h.snapshot(t, h.acc), h.snapshot(t, h.acc2)
	if fmt.Sprint(before) != fmt.Sprint(after) {
		t.Errorf("round trip changed contacts:\nbefore %v\nafter  %v", before, after)
	}
}

func TestImportStatusColumnOnlyHoldsContactsBack(t *testing.T) {
	h := newHarness(t)
	im := h.importFile(t, ImportInput{ListSource: ListSourceForm, CSV: []byte("email,Status\n" +
		"a@example.com,subscribed\nb@example.com,Unsubscribed\nc@example.com,complained\nd@example.com,active\ne@example.com,\n")})
	if im.Imported != 5 || deref(im.StatusColumn) != "Status" {
		t.Fatalf("import = %+v", im)
	}
	want := map[string]string{"a": StatusUnconfirmed, "b": StatusUnsubscribed, "c": StatusComplained, "d": StatusUnconfirmed, "e": StatusUnconfirmed}
	for who, status := range want {
		if c := h.byEmail(t, who+"@example.com"); c.Status != status {
			t.Errorf("%s: %s, want %s", who, c.Status, status)
		}
	}

	// With permission, an unconfirmed row still stays unconfirmed.
	h.importFile(t, ImportInput{ListSource: ListSourceForm, CSV: []byte("email,state\nf@example.com,unconfirmed\ng@example.com,subscribed\n"),
		StatusColumn: "state", PermissionConfirmed: true})
	if f, g := h.byEmail(t, "f@example.com"), h.byEmail(t, "g@example.com"); f.Status != StatusUnconfirmed || g.Status != StatusSubscribed {
		t.Errorf("f %s, g %s", f.Status, g.Status)
	}
}

// Rows that won't count toward the limit go in even when a free account is
// full.
func TestImportAtTheLimitStillTakesUnsubscribedRows(t *testing.T) {
	h := newHarness(t)
	h.fill(t, h.acc, FreeContactLimit)
	im := h.importFile(t, ImportInput{ListSource: ListSourceForm, CSV: []byte("email,status\nnew@example.com,\ngone@example.com,unsubscribed\n"), PermissionConfirmed: true})
	if im.Imported != 1 || im.SkippedByReason[SkipOverLimit] != 1 || h.byEmail(t, "gone@example.com").Status != StatusUnsubscribed {
		t.Errorf("import = %+v", im)
	}
}

func TestImportTagsColumn(t *testing.T) {
	h := newHarness(t)
	h.tag(t, "VIP")
	news := h.tag(t, "news")
	im := h.importFile(t, ImportInput{ListSource: ListSourceForm, CSV: []byte("email,tags\n" +
		"a@example.com,\"vip, Customers ,customers\"\nb@example.com,\nc@example.com,'=sum\n"), Tags: []string{news.ID}})
	if im.Imported != 3 || deref(im.TagsColumn) != "tags" {
		t.Fatalf("import = %+v", im)
	}
	for who, want := range map[string][]string{"a": {"Customers", "news", "VIP"}, "b": {"news"}, "c": {"=sum", "news"}} {
		if got := tagNames(h.byEmail(t, who+"@example.com")); !slices.Equal(got, want) {
			t.Errorf("%s: tags %v, want %v", who, got, want)
		}
	}
	tags, err := h.svc.ListTags(context.Background(), h.acc, "", nil, 100)
	if err != nil || len(tags) != 4 {
		t.Errorf("tags = %d, %v", len(tags), err)
	}

	long := strings.Repeat("x", MaxTagNameLen+1)
	var many strings.Builder
	many.WriteString("email,tags\n")
	for i := range MaxImportFileTags + 1 {
		fmt.Fprintf(&many, "p%d@example.com,t%d\n", i, i)
	}
	for in, want := range map[string]string{"email,tags\na@example.com,ok\nb@example.com," + long + "\n": "invalid_tag_name", many.String(): "too_many_tags"} {
		if _, err := h.svc.CreateImport(context.Background(), h.acc, ImportInput{ListSource: ListSourceForm, CSV: []byte(in)}); code(err) != want {
			t.Errorf("%.40q: %v, want %s", in, err, want)
		}
	}
}

func TestContactData(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	vip := h.tag(t, "vip")
	im := h.importFile(t, ImportInput{ListSource: ListSourceForm, CSV: []byte("email\npat@example.com\n PAT@example.com\n"), FileName: "list.csv", Tags: []string{"vip"}})
	h.importFile(t, ImportInput{ListSource: ListSourceForm, CSV: []byte("email\npat@example.com\n")})
	pat := h.byEmail(t, "pat@example.com")

	d, err := h.svc.Data(ctx, h.acc, pat.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Contact.ID != pat.ID || len(d.Tags) != 1 || d.Tags[0].ID != vip.ID || d.Tags[0].AddedAt.IsZero() {
		t.Errorf("contact and tags = %+v %+v", d.Contact, d.Tags)
	}
	if d.Import == nil || d.Import.ID != im.ID || deref(d.Import.FileName) != "list.csv" || d.Import.PermissionConfirmed || d.Import.Held {
		t.Errorf("import = %+v", d.Import)
	}
	if got := fmt.Sprint(d.SkippedRows); !strings.Contains(got, "3 duplicate") || !strings.Contains(got, "2 exists") || len(d.SkippedRows) != 2 {
		t.Errorf("skipped rows = %s", got)
	}

	sam := h.add(t, "sam@example.com", StatusSubscribed)
	if d, err := h.svc.Data(ctx, h.acc, sam.ID); err != nil || d.Import != nil || len(d.Tags) != 0 || len(d.SkippedRows) != 0 {
		t.Errorf("sam = %+v, %v", d, err)
	}
	if _, err := h.svc.Data(ctx, h.acc2, pat.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("another account's contact: %v", err)
	}
}

func TestEraseLeavesOnlyTheSuppressionHash(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.tag(t, "vip")
	im := h.importFile(t, ImportInput{ListSource: ListSourceForm, CSV: []byte("email\npat@example.com\nPat@Example.com\nsam@example.com\n"), Tags: []string{"vip"}})
	pat := h.byEmail(t, "pat@example.com")

	if err := h.svc.Erase(ctx, h.acc, pat.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Get(ctx, h.acc, pat.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("contact still there: %v", err)
	}
	if got := fmt.Sprint(h.skipped(t, im.ID)); got != "[{3  duplicate }]" {
		t.Errorf("skip report = %s", got)
	}
	if ok, err := h.svc.IsSuppressed(ctx, h.acc, "pat@example.com"); !ok || err != nil {
		t.Errorf("suppressed = %v, %v", ok, err)
	}
	var stored int
	if err := h.svc.DB.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM contacts WHERE email = 'pat@example.com')
			+ (SELECT count(*) FROM import_skips WHERE lower(email) LIKE '%pat@%')
			+ (SELECT count(*) FROM import_address_hashes WHERE hash = $1)
			+ (SELECT count(*) FROM consents WHERE contact_id = $2)`, importHash("pat@example.com"), pat.ID).Scan(&stored); err != nil || stored != 0 {
		t.Errorf("address still stored %d times (%v)", stored, err)
	}
	// Added again from an old list, it stays unsubscribed.
	if again := h.add(t, "pat@example.com", StatusSubscribed); again.Status != StatusUnsubscribed {
		t.Errorf("re-added as %s", again.Status)
	}

	// A bounced contact keeps its bounce on the suppression list.
	sam := h.byEmail(t, "sam@example.com")
	h.setStatus(t, sam.ID, StatusBounced)
	if err := h.svc.Erase(ctx, h.acc, sam.ID); err != nil {
		t.Fatal(err)
	}
	if again := h.add(t, "sam@example.com", StatusSubscribed); again.Status != StatusBounced {
		t.Errorf("bounced re-added as %s", again.Status)
	}
	if err := h.svc.Erase(ctx, h.acc, sam.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("erasing a gone contact: %v", err)
	}
}
