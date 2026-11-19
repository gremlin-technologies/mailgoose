package contacts

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// list builds a CSV of n addresses named prefix0@domain ... plus extra rows.
func list(prefix, domain string, n int, extra ...string) []byte {
	var b strings.Builder
	b.WriteString("email\n")
	for i := range n {
		fmt.Fprintf(&b, "%s%d@%s\n", prefix, i, domain)
	}
	for _, e := range extra {
		b.WriteString(e + "\n")
	}
	return []byte(b.String())
}

func TestImportNeedsAListSource(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	csv := []byte("email\npat@example.com\n")
	for name, tc := range map[string]struct {
		in   ImportInput
		want string
	}{
		"none":            {ImportInput{CSV: csv}, "list_source_required"},
		"unknown":         {ImportInput{CSV: csv, ListSource: "bought"}, "invalid_list_source"},
		"other, no words": {ImportInput{CSV: csv, ListSource: ListSourceOther, ListSourceDetail: "  "}, "list_source_detail_required"},
		"other, too long": {ImportInput{CSV: csv, ListSource: ListSourceOther, ListSourceDetail: strings.Repeat("x", MaxListSourceDetail+1)},
			"invalid_list_source"},
	} {
		if _, err := h.svc.CreateImport(ctx, h.acc, tc.in); code(err) != tc.want {
			t.Errorf("%s: err = %v, want %s", name, err, tc.want)
		}
	}
	var n int
	if err := h.svc.DB.QueryRow(ctx, `SELECT count(*) FROM imports`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("imports = %d, %v; a refused import queues nothing", n, err)
	}
	for _, src := range ListSources {
		in := ImportInput{CSV: csv, ListSource: src}
		if src == ListSourceOther {
			in.ListSourceDetail = " a  trade show\nsign-up sheet "
		}
		im := h.importFile(t, in)
		if deref(im.ListSource) != src || im.Review != ReviewNone {
			t.Errorf("%s: import = %+v", src, im)
		}
		if src == ListSourceOther && deref(im.ListSourceDetail) != "a trade show sign-up sheet" {
			t.Errorf("detail = %q", deref(im.ListSourceDetail))
		}
	}
}

func TestImportListSourceIsEachContactsConsentRecord(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	im := h.importFile(t, ImportInput{CSV: []byte("email\npat@example.com\nsam@example.com\n"), FileName: "fair.csv",
		ListSource: ListSourceOther, ListSourceDetail: "Signed up at our stand", PermissionConfirmed: true})
	for _, email := range []string{"pat@example.com", "sam@example.com"} {
		c := h.byEmail(t, email)
		d, err := h.svc.Data(ctx, h.acc, c.ID)
		if err != nil {
			t.Fatal(err)
		}
		if d.Import == nil || deref(d.Import.ListSource) != ListSourceOther || deref(d.Import.ListSourceDetail) != "Signed up at our stand" {
			t.Fatalf("import = %+v", d.Import)
		}
		if len(d.Consents) != 1 {
			t.Fatalf("consents = %+v", d.Consents)
		}
		k := d.Consents[0]
		var w map[string]any
		_ = json.Unmarshal(k.Wording, &w)
		if k.Source != "import" || k.IP != nil || k.FormID != nil || deref(k.ImportID) != im.ID || !k.CreatedAt.Equal(im.CreatedAt) ||
			w["list_source"] != ListSourceOther || w["list_source_detail"] != "Signed up at our stand" || w["file_name"] != "fair.csv" {
			t.Errorf("consent = %+v, wording %v", k, w)
		}
	}
	// A form signup's export columns aren't filled from an import's record.
	var b strings.Builder
	if _, err := h.svc.ExportCSV(ctx, h.acc, Filter{}, &b); err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(strings.TrimSpace(b.String()), "\n"); len(lines) != 3 || !strings.HasSuffix(lines[1], ",true,,,,") {
		t.Errorf("export = %q", b.String())
	}
}

func TestImportChecksCountTheFile(t *testing.T) {
	h := newHarness(t)
	csv := list("pat", "example.com", 30,
		"info@shop.example", "Sales+news@shop.example", "x@mailinator.com", "y@sub.guerrillamail.com", "not-an-address")
	im := h.importFile(t, ImportInput{CSV: csv, ListSource: ListSourceCustomers})
	if im.Checks.InvalidRows != 1 || im.Checks.RoleAddresses != 2 || im.Checks.DisposableAddresses != 2 || im.Checks.SharedAddresses != 0 {
		t.Fatalf("checks = %+v", im.Checks)
	}
	// 4 of 34 valid rows are risky (over 10%).
	if im.Review != ReviewHeld || deref(im.HoldReason) != HoldRiskyAddresses {
		t.Errorf("review = %s %v", im.Review, im.HoldReason)
	}
	if !strings.Contains(HoldMessage(*im.HoldReason), "role addresses") {
		t.Errorf("message = %q", HoldMessage(*im.HoldReason))
	}
}

func TestImportUnderEveryThresholdIsntHeld(t *testing.T) {
	h := newHarness(t)
	// Another account imported 199 addresses; this file shares 198 of them,
	// under 200.
	if _, err := h.svc.CreateImport(context.Background(), h.acc2, ImportInput{CSV: list("p", "example.com", 199), ListSource: ListSourceForm}); err != nil {
		t.Fatal(err)
	}
	// 22 role addresses of 220 valid rows is 10%, not over it; 11 bad rows
	// of 231 is under 5%.
	var extra []string
	for i := range 22 {
		extra = append(extra, fmt.Sprintf("info@shop%d.example", i))
	}
	for i := range 11 {
		extra = append(extra, fmt.Sprintf("bad%d", i))
	}
	im := h.importFile(t, ImportInput{CSV: list("p", "example.com", 198, extra...), ListSource: ListSourceForm})
	if im.Review != ReviewNone || im.Checks.SharedAddresses != 198 || im.Checks.RoleAddresses != 22 || im.Checks.InvalidRows != 11 {
		t.Errorf("import = %s %v, checks %+v", im.Review, im.HoldReason, im.Checks)
	}
}

func TestImportOfAListOtherAccountsHaveIsHeld(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	// Paid, so the free plan's contact limit doesn't skip rows.
	if _, err := h.svc.DB.Exec(ctx, `UPDATE accounts SET plan = 'paid' WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.CreateImport(ctx, h.acc2, ImportInput{CSV: list("buy", "example.net", 300), ListSource: ListSourceForm}); err != nil {
		t.Fatal(err)
	}
	// Its own earlier import doesn't count.
	if im := h.importFile(t, ImportInput{CSV: list("mine", "example.org", 300), ListSource: ListSourceForm}); im.Review != ReviewNone {
		t.Fatalf("first = %s %v", im.Review, im.HoldReason)
	}
	if im := h.importFile(t, ImportInput{CSV: list("mine", "example.org", 300), ListSource: ListSourceForm}); im.Review != ReviewNone ||
		im.Checks.SharedAddresses != 0 {
		t.Fatalf("again = %s %v %+v", im.Review, im.HoldReason, im.Checks)
	}
	// 300 of 500 distinct addresses (60%) are in acc2's import.
	csv := list("buy", "example.net", 300)
	csv = append(csv, list("own", "example.org", 200)[len("email\n"):]...)
	csv = append(csv, "BUY1@example.net\n"...) // a repeat counts once
	im := h.importFile(t, ImportInput{CSV: csv, ListSource: ListSourceExport})
	if im.Review != ReviewHeld || deref(im.HoldReason) != HoldSharedList || im.Checks.SharedAddresses != 300 ||
		deref(im.Checks.SharedAccountID) != h.acc2 {
		t.Fatalf("import = %s %v, checks %+v", im.Review, im.HoldReason, im.Checks)
	}
	// Its contacts are added but held, like any held import.
	var held int
	if err := h.svc.DB.QueryRow(ctx, `SELECT count(*) FROM contacts WHERE held_import_id = $1`, im.ID).Scan(&held); err != nil || held != 500 {
		t.Fatalf("held contacts = %d, %v", held, err)
	}
	// The hashes are kept per import, and go with it.
	var hashes int
	if err := h.svc.DB.QueryRow(ctx, `SELECT count(*) FROM import_address_hashes WHERE import_id = $1`, im.ID).Scan(&hashes); err != nil || hashes != 500 {
		t.Fatalf("hashes = %d, %v", hashes, err)
	}
	if strings.Contains(HoldMessage(HoldSharedList), h.acc2) {
		t.Error("the customer's message names another account")
	}
}

func TestImportWithManyBadRowsIsHeld(t *testing.T) {
	h := newHarness(t)
	csv := list("p", "example.com", 94, "a", "b", "c", "d", "e", "f") // 6 of 100
	im := h.importFile(t, ImportInput{CSV: csv, ListSource: ListSourcePersonal})
	if im.Review != ReviewHeld || deref(im.HoldReason) != HoldInvalidAddresses || im.Checks.InvalidRows != 6 {
		t.Fatalf("import = %s %v %+v", im.Review, im.HoldReason, im.Checks)
	}
	// A list check outranks a large first import as the reason.
	h2 := newHarness(t)
	var bad []string
	for i := range 120 { // 120 of 2,120 rows
		bad = append(bad, fmt.Sprintf("bad%d", i))
	}
	im = h2.importFile(t, ImportInput{CSV: list("q", "example.com", LargeFirstImport, bad...), ListSource: ListSourceForm})
	if im.Review != ReviewHeld || deref(im.HoldReason) != HoldInvalidAddresses {
		t.Fatalf("large and bad = %s %v %+v", im.Review, im.HoldReason, im.Checks)
	}
}

func TestImportThresholdsComeFromConfig(t *testing.T) {
	c := ImportChecks{RiskyPercent: 50}
	f := &fileScan{total: 10, valid: 10, counts: CheckCounts{RoleAddresses: 4}}
	if r := c.holdFor(f); r != "" {
		t.Errorf("4 of 10 under 50%% held for %s", r)
	}
	if r := (ImportChecks{}).holdFor(f); r != HoldRiskyAddresses {
		t.Errorf("defaults: %q", r)
	}
	f = &fileScan{total: 100, valid: 100, hashes: make([][]byte, 100), counts: CheckCounts{SharedAddresses: 60}}
	if r := (ImportChecks{SharedMin: 50}).holdFor(f); r != HoldSharedList {
		t.Errorf("shared 60 of 100 with min 50: %q", r)
	}
	if r := (ImportChecks{}).holdFor(f); r != "" {
		t.Errorf("shared 60 under the default 200: %q", r)
	}
	for _, r := range []string{HoldLargeFirstImport, HoldOverContactLimit, HoldSharedList, HoldRiskyAddresses, HoldInvalidAddresses} {
		if HoldMessage(r) == "" {
			t.Errorf("no message for %s", r)
		}
	}
}
