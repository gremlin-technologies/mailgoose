package contacts

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// runImports runs every waiting import and fails the test on an error.
func (h *harness) runImports(t *testing.T) {
	t.Helper()
	for {
		ran, err := h.svc.RunNextImport(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !ran {
			return
		}
	}
}

func (h *harness) importFile(t *testing.T, in ImportInput) *Import {
	t.Helper()
	im, err := h.svc.CreateImport(context.Background(), h.acc, in)
	if err != nil {
		t.Fatalf("create import: %v", err)
	}
	h.runImports(t)
	im, err = h.svc.GetImport(context.Background(), h.acc, im.ID)
	if err != nil {
		t.Fatal(err)
	}
	return im
}

func (h *harness) skipped(t *testing.T, id string) []SkippedRow {
	t.Helper()
	var all []SkippedRow
	after := 0
	for {
		page, err := h.svc.ListSkippedRows(context.Background(), h.acc, id, after, 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			return all
		}
		all = append(all, page...)
		after = page[len(page)-1].Row
	}
}

func (h *harness) byEmail(t *testing.T, email string) *Contact {
	t.Helper()
	cs, err := h.svc.List(context.Background(), h.acc, Filter{Email: email}, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		if c.Email == email {
			return c
		}
	}
	return nil
}

// rows makes a CSV with an email header and n addresses n0@example.com ...
func rows(n int, extra ...string) []byte {
	var b strings.Builder
	b.WriteString("email\n")
	for i := range n {
		fmt.Fprintf(&b, "n%d@example.com\n", i)
	}
	for _, e := range extra {
		b.WriteString(e + "\n")
	}
	return []byte(b.String())
}

func TestImportAddsContactsAndReportsSkippedRows(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	tag, err := h.svc.CreateTag(ctx, h.acc, "vip")
	if err != nil {
		t.Fatal(err)
	}
	h.add(t, "old@example.com", StatusSubscribed)
	gone := h.add(t, "gone@example.com", StatusUnsubscribed)
	if err := h.svc.Delete(ctx, h.acc, gone.ID); err != nil {
		t.Fatal(err)
	}

	csv := "Email Address,First Name,Surname,Notes\n" + // line 1
		"pat@example.com, Pat ,Lee,a\n" + // 2: added
		"not-an-address,Sam,,b\n" + // 3: invalid
		"PAT@example.com,Pat,Again,c\n" + // 4: duplicate in the file
		"old@example.com,Old,,d\n" + // 5: already a contact
		"gone@example.com,Gone,,e\n" + // 6: suppressed
		"bad\"quote@example.com,X,,f\n" + // 7: unreadable
		",,,\n" + // 8: blank, left out
		"\"multi\nline@example.com\",M,,g\n" + // 9-10: invalid
		"kim@example.com,\"Kim\nJo\",,h\n" // 11-12: added, name on one line
	im := h.importFile(t, ImportInput{ListSource: ListSourceForm, CSV: []byte(csv), FileName: "list.csv", Tags: []string{"VIP"}})

	if im.Status != ImportDone || im.TotalRows != 8 || im.ProcessedRows != 8 || im.Imported != 2 || im.Skipped != 6 {
		t.Fatalf("import = %+v", im)
	}
	if im.EmailColumn != "Email Address" || deref(im.FirstNameColumn) != "First Name" || deref(im.LastNameColumn) != "Surname" {
		t.Errorf("columns = %q %v %v", im.EmailColumn, im.FirstNameColumn, im.LastNameColumn)
	}
	if im.ContactStatus() != StatusUnconfirmed || len(im.Tags) != 1 || im.Tags[0].ID != tag.ID {
		t.Errorf("import = %+v", im)
	}
	// 3 of 8 rows aren't addresses, over the 5% that holds an import (MG-63).
	if im.Review != ReviewHeld || deref(im.HoldReason) != HoldInvalidAddresses || im.Checks.InvalidRows != 3 {
		t.Errorf("review = %s %v, checks = %+v", im.Review, im.HoldReason, im.Checks)
	}
	want := map[string]int{SkipInvalidEmail: 2, SkipInvalidRow: 1, SkipDuplicate: 1, SkipExists: 1, SkipSuppressed: 1, SkipOverLimit: 0}
	for r, n := range want {
		if im.SkippedByReason[r] != n {
			t.Errorf("skipped %s = %d, want %d", r, im.SkippedByReason[r], n)
		}
	}
	got := h.skipped(t, im.ID)
	wantRows := []SkippedRow{
		{3, "not-an-address", SkipInvalidEmail},
		{4, "PAT@example.com", SkipDuplicate},
		{5, "old@example.com", SkipExists},
		{6, "gone@example.com", SkipSuppressed},
		{7, "", SkipInvalidRow},
		{9, "multi\nline@example.com", SkipInvalidEmail},
	}
	if fmt.Sprint(got) != fmt.Sprint(wantRows) {
		t.Errorf("skipped rows\n got %v\nwant %v", got, wantRows)
	}

	pat := h.byEmail(t, "pat@example.com")
	if pat == nil || pat.Status != StatusUnconfirmed || pat.Source != SourceImport || deref(pat.FirstName) != "Pat" ||
		deref(pat.LastName) != "Lee" || len(pat.Tags) != 1 || pat.Tags[0].Name != "vip" {
		t.Errorf("pat = %+v", pat)
	}
	if kim := h.byEmail(t, "kim@example.com"); kim == nil || deref(kim.FirstName) != "Kim Jo" {
		t.Errorf("kim = %+v", kim)
	}
	if old := h.byEmail(t, "old@example.com"); old.Status != StatusSubscribed || len(old.Tags) != 0 {
		t.Errorf("an existing contact changed: %+v", old)
	}
	if ok, _ := h.svc.IsSuppressed(ctx, h.acc, "gone@example.com"); !ok {
		t.Error("the suppression entry was removed")
	}
	var csvLeft bool
	if err := h.svc.DB.QueryRow(ctx, `SELECT csv IS NOT NULL FROM imports WHERE id = $1`, im.ID).Scan(&csvLeft); err != nil || csvLeft {
		t.Errorf("the file wasn't deleted: %v %v", csvLeft, err)
	}
}

func TestImportWithPermissionAndNamedColumns(t *testing.T) {
	h := newHarness(t)
	// A spreadsheet's semicolon CSV with a byte order mark.
	csv := "\xef\xbb\xbfWho;Given;Family\nana@example.com;Ana;Silva\n"
	im := h.importFile(t, ImportInput{ListSource: ListSourceForm, CSV: []byte(csv), EmailColumn: "who", FirstNameColumn: "Given", LastNameColumn: "family", PermissionConfirmed: true})
	if im.Imported != 1 || im.ContactStatus() != StatusSubscribed {
		t.Fatalf("import = %+v", im)
	}
	if c := h.byEmail(t, "ana@example.com"); c.Status != StatusSubscribed || deref(c.FirstName) != "Ana" || deref(c.LastName) != "Silva" {
		t.Errorf("contact = %+v", c)
	}
}

func TestImportRefusesFilesItCantRead(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for name, tc := range map[string]struct {
		in   ImportInput
		code string
	}{
		"empty":            {ImportInput{ListSource: ListSourceForm, CSV: []byte(" \n")}, "no_rows"},
		"header only":      {ImportInput{ListSource: ListSourceForm, CSV: []byte("email\n,\n")}, "no_rows"},
		"no email column":  {ImportInput{ListSource: ListSourceForm, CSV: []byte("name,phone\nPat,1\n")}, "no_email_column"},
		"named column":     {ImportInput{ListSource: ListSourceForm, CSV: []byte("email\na@example.com\n"), FirstNameColumn: "First"}, "invalid_mapping"},
		"same column":      {ImportInput{ListSource: ListSourceForm, CSV: []byte("email\na@example.com\n"), EmailColumn: "email", FirstNameColumn: "email"}, "invalid_mapping"},
		"not UTF-8":        {ImportInput{ListSource: ListSourceForm, CSV: []byte("email\n\xff@example.com\n")}, "invalid_encoding"},
		"unclosed quote":   {ImportInput{ListSource: ListSourceForm, CSV: []byte("email\n\"a@example.com\nb@example.com\n")}, "invalid_csv"},
		"unknown tag":      {ImportInput{ListSource: ListSourceForm, CSV: []byte("email\na@example.com\n"), Tags: []string{"nope"}}, "tag_not_found"},
		"too many rows":    {ImportInput{ListSource: ListSourceForm, CSV: rows(MaxImportRows + 1)}, "too_many_rows"},
		"too large a file": {ImportInput{ListSource: ListSourceForm, CSV: make([]byte, MaxImportBytes+1)}, "file_too_large"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := h.svc.CreateImport(ctx, h.acc, tc.in); code(err) != tc.code {
				t.Errorf("err = %v, want %s", err, tc.code)
			}
		})
	}
	if ims, err := h.svc.ListImports(ctx, h.acc, nil, 10); err != nil || len(ims) != 0 {
		t.Errorf("imports = %v, %v", ims, err)
	}
}

func TestLargeFirstImportIsHeld(t *testing.T) {
	h := newHarness(t)
	h.setPlan(t, h.acc, "paid")
	ctx := context.Background()
	im := h.importFile(t, ImportInput{ListSource: ListSourceForm, CSV: rows(LargeFirstImport + 1), PermissionConfirmed: true})
	if im.Review != ReviewHeld || deref(im.HoldReason) != HoldLargeFirstImport || im.Imported != LargeFirstImport+1 {
		t.Fatalf("import = %+v", im)
	}
	if rs, err := h.svc.Recipients(ctx, h.acc, Audience{All: true}, "", 10); err != nil || len(rs) != 0 {
		t.Errorf("held contacts are recipients: %d, %v", len(rs), err)
	}
	// A contact added another way is still mailed.
	h.add(t, "own@example.com", StatusSubscribed)
	if rs, _ := h.svc.Recipients(ctx, h.acc, Audience{All: true}, "", 10); len(rs) != 1 || rs[0].Email != "own@example.com" {
		t.Errorf("recipients = %v", rs)
	}
	// Only the first import is held for size.
	second := h.importFile(t, ImportInput{ListSource: ListSourceForm, CSV: rows(LargeFirstImport + 1)})
	if second.Review != ReviewNone {
		t.Errorf("second import = %+v", second)
	}
	// A first import of exactly the threshold isn't held.
	h2 := newHarness(t)
	h2.setPlan(t, h2.acc, "paid")
	if im := h2.importFile(t, ImportInput{ListSource: ListSourceForm, CSV: rows(LargeFirstImport)}); im.Review != ReviewNone {
		t.Errorf("import at the threshold = %+v", im)
	}
}

func TestImportOverTheFreeLimit(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.fill(t, h.acc, FreeContactLimit-10)
	csv := rows(20, "filler1@example.com")
	im := h.importFile(t, ImportInput{ListSource: ListSourceForm, CSV: csv, PermissionConfirmed: true})
	if im.Review != ReviewHeld || deref(im.HoldReason) != HoldOverContactLimit || im.Imported != 10 ||
		im.SkippedByReason[SkipOverLimit] != 10 || im.SkippedByReason[SkipExists] != 1 {
		t.Fatalf("import = %+v", im)
	}
	if n, _ := h.svc.CountedContacts(ctx, h.acc); n != FreeContactLimit {
		t.Errorf("counted = %d", n)
	}
	last := h.skipped(t, im.ID)
	if last[len(last)-2].Reason != SkipOverLimit || last[len(last)-2].Email != "n19@example.com" {
		t.Errorf("skipped = %v", last)
	}

	// Addresses already in the account don't count toward the hold.
	h2 := newHarness(t)
	h2.fill(t, h2.acc, FreeContactLimit-1)
	if im := h2.importFile(t, ImportInput{ListSource: ListSourceForm, CSV: []byte("email\nfiller1@example.com\nnew@example.com\n")}); im.Review != ReviewNone || im.Imported != 1 {
		t.Errorf("import within the limit = %+v", im)
	}

	// Paid accounts have no cap.
	h3 := newHarness(t)
	h3.setPlan(t, h3.acc, "paid")
	h3.fill(t, h3.acc, FreeContactLimit)
	if im := h3.importFile(t, ImportInput{ListSource: ListSourceForm, CSV: rows(20)}); im.Review != ReviewNone || im.Imported != 20 {
		t.Errorf("paid import = %+v", im)
	}
}

func TestImportCarriesOnAfterAStop(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	stops := 0
	afterImportBatch = func(int) error {
		if stops == 0 {
			stops++
			return errors.New("worker stopped")
		}
		return nil
	}
	t.Cleanup(func() { afterImportBatch = nil })
	h.setPlan(t, h.acc, "paid")

	// The first batch has n0 ... n999; the second repeats n5.
	csv := rows(importBatch+500, "N5@example.com")
	im, err := h.svc.CreateImport(ctx, h.acc, ImportInput{ListSource: ListSourceForm, CSV: csv})
	if err != nil {
		t.Fatal(err)
	}
	if ran, err := h.svc.RunNextImport(ctx); !ran || err == nil {
		t.Fatalf("first run: %v %v", ran, err)
	}
	mid, _ := h.svc.GetImport(ctx, h.acc, im.ID)
	if mid.Status != ImportRunning || mid.ProcessedRows != importBatch || mid.Imported != importBatch {
		t.Fatalf("after the stop: %+v", mid)
	}
	// Nobody takes it over while the lease holds.
	if ran, _ := h.svc.RunNextImport(ctx); ran {
		t.Fatal("ran an import under another worker's lease")
	}
	h.now = h.now.Add(ImportLease)
	h.runImports(t)
	done, _ := h.svc.GetImport(ctx, h.acc, im.ID)
	if done.Status != ImportDone || done.Imported != importBatch+500 || done.Skipped != 1 || done.SkippedByReason[SkipDuplicate] != 1 {
		t.Errorf("after carrying on: %+v", done)
	}
}

func TestImportFailsAfterRepeatedStops(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	afterImportBatch = func(int) error { return errors.New("worker stopped") }
	t.Cleanup(func() { afterImportBatch = nil })
	h.setPlan(t, h.acc, "paid")
	im, err := h.svc.CreateImport(ctx, h.acc, ImportInput{ListSource: ListSourceForm, CSV: rows(importBatch * 4)})
	if err != nil {
		t.Fatal(err)
	}
	for i := range maxImportAttempts {
		if ran, err := h.svc.RunNextImport(ctx); !ran || err == nil {
			t.Fatalf("run %d: %v %v", i+1, ran, err)
		}
		h.now = h.now.Add(ImportLease)
	}
	if ran, err := h.svc.RunNextImport(ctx); ran || err != nil {
		t.Fatalf("after the last try: %v %v", ran, err)
	}
	failed, _ := h.svc.GetImport(ctx, h.acc, im.ID)
	if failed.Status != ImportFailed || failed.Error == nil || failed.FinishedAt == nil || failed.Imported != importBatch*maxImportAttempts {
		t.Errorf("import = %+v", failed)
	}
}

func TestImportsAreKeptPerAccount(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	im := h.importFile(t, ImportInput{ListSource: ListSourceForm, CSV: rows(3, "x")})
	if _, err := h.svc.GetImport(ctx, h.acc2, im.ID); !errors.Is(err, ErrImportNotFound) {
		t.Errorf("get from another account: %v", err)
	}
	if _, err := h.svc.ListSkippedRows(ctx, h.acc2, im.ID, 0, 10); !errors.Is(err, ErrImportNotFound) {
		t.Errorf("skipped rows from another account: %v", err)
	}
	if ims, _ := h.svc.ListImports(ctx, h.acc2, nil, 10); len(ims) != 0 {
		t.Errorf("another account's list = %v", ims)
	}
	second := h.importFile(t, ImportInput{ListSource: ListSourceForm, CSV: rows(1)})
	page, err := h.svc.ListImports(ctx, h.acc, nil, 1)
	if err != nil || len(page) != 1 || page[0].ID != second.ID {
		t.Fatalf("first page = %v, %v", page, err)
	}
	cur := ImportCursor(page[0])
	if page, _ := h.svc.ListImports(ctx, h.acc, &cur, 10); len(page) != 1 || page[0].ID != im.ID {
		t.Errorf("second page = %v", page)
	}
}

// The ticket's bar: 10,000 rows with 3% bad ones in under a minute.
func TestImportOf10000RowsWithBadRowsUnderAMinute(t *testing.T) {
	h := newHarness(t)
	h.setPlan(t, h.acc, "paid")
	var b strings.Builder
	b.WriteString("email,first_name,last_name\n")
	for i := range 10_000 {
		switch i % 100 {
		case 1:
			fmt.Fprintf(&b, "broken-%d,Bad,Row\n", i)
		case 2:
			fmt.Fprintf(&b, "p%d@example.com,Twice,Row\n", i-2)
		case 3:
			fmt.Fprintf(&b, "p%d@localhost,No,Dot\n", i)
		default:
			fmt.Fprintf(&b, "p%d@example.com,First%d,Last%d\n", i, i, i)
		}
	}
	start := time.Now()
	im := h.importFile(t, ImportInput{ListSource: ListSourceForm, CSV: []byte(b.String()), PermissionConfirmed: true})
	took := time.Since(start)
	if took > time.Minute {
		t.Errorf("took %s", took)
	}
	t.Logf("10,000 rows in %s", took)
	if im.Status != ImportDone || im.Imported != 9_700 || im.Skipped != 300 ||
		im.SkippedByReason[SkipInvalidEmail] != 200 || im.SkippedByReason[SkipDuplicate] != 100 {
		t.Errorf("import = %+v", im)
	}
	if got := h.skipped(t, im.ID); len(got) != 300 {
		t.Errorf("%d skipped rows listed", len(got))
	}
}
