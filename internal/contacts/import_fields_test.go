package contacts

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/gremlin-technologies/mailgoose/internal/auth"
)

// Import columns to contact fields (MG-131).

func importCode(err error) string {
	var ve *auth.ValidationError
	if errors.As(err, &ve) {
		return ve.Code
	}
	return ""
}

func (h *harness) fieldCount(t *testing.T, account string) int {
	t.Helper()
	fs, err := h.svc.AllFields(context.Background(), account)
	if err != nil {
		t.Fatal(err)
	}
	return len(fs)
}

func (h *harness) importCount(t *testing.T, account string) int {
	t.Helper()
	ims, err := h.svc.ListImports(context.Background(), account, nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	return len(ims)
}

func TestPreviewMatchesContactFields(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	company := h.field(t, h.acc, "Company")
	plan := h.field(t, h.acc, "Plan tier")
	// Renamed, it keeps the key plan_tier, which "PLAN_TIER" matches.
	if _, err := h.svc.RenameField(ctx, h.acc, plan.ID, "Subscription"); err != nil {
		t.Fatal(err)
	}
	csv := []byte("Email,company,PLAN_TIER,Size,First Name\npat@example.com,Acme,Gold,12,Pat\n")

	p, err := h.svc.PreviewImport(ctx, h.acc, ImportInput{CSV: csv})
	if err != nil {
		t.Fatal(err)
	}
	if p.Problem != nil {
		t.Fatalf("problem = %+v", p.Problem)
	}
	got := func(p *ImportPreview) []string {
		var out []string
		for _, c := range p.Columns {
			switch {
			case c.CustomField != nil:
				out = append(out, "field:"+c.CustomField.Key)
			case c.NewField != nil:
				out = append(out, "new:"+c.NewField.Key+"/"+c.NewField.Name)
			default:
				out = append(out, c.Field)
			}
		}
		return out
	}
	// Matched by name ("company") and by key ("PLAN_TIER"), loosely.
	if g := strings.Join(got(p), " "); g != "email field:company field:plan_tier  first_name" {
		t.Errorf("columns = %q", g)
	}
	if p.Columns[1].CustomField.ID != company.ID || p.Columns[2].CustomField.ID != plan.ID {
		t.Errorf("custom fields = %+v %+v", p.Columns[1].CustomField, p.Columns[2].CustomField)
	}
	if s := p.Columns[1].Samples; len(s) != 1 || s[0] != "Acme" {
		t.Errorf("samples = %q", s)
	}

	// A field named empty isn't matched; a named one takes the column given;
	// a new field is made from Size under its header.
	p, err = h.svc.PreviewImport(ctx, h.acc, ImportInput{CSV: csv, FieldColumns: map[string]string{"company": "", plan.ID: "Size"},
		NewFields: []NewImportField{{Column: "PLAN_TIER", Name: "Tier"}}})
	if err != nil {
		t.Fatal(err)
	}
	if g := strings.Join(got(p), " "); g != "email  new:tier/Tier field:plan_tier first_name" || p.Problem != nil {
		t.Errorf("columns = %q, problem = %+v", g, p.Problem)
	}

	// An unknown field and a taken column are problems, not errors.
	p, err = h.svc.PreviewImport(ctx, h.acc, ImportInput{CSV: csv, FieldColumns: map[string]string{"nope": "Size"}})
	if err != nil || p.Problem == nil || p.Problem.Code != "unknown_field" {
		t.Errorf("unknown field: %v %+v", err, p.Problem)
	}
	p, err = h.svc.PreviewImport(ctx, h.acc, ImportInput{CSV: csv, EmailColumn: "Email", FieldColumns: map[string]string{"company": "Email"}})
	if err != nil || p.Problem == nil || p.Problem.Code != "invalid_mapping" {
		t.Errorf("taken column: %v %+v", err, p.Problem)
	}
}

func TestImportMakesFieldsAndWritesValues(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.field(t, h.acc, "Company")
	plan := h.field(t, h.acc, "Plan")
	old, err := h.svc.Create(ctx, h.acc, CreateInput{Email: "old@example.com", Fields: map[string]*string{"company": ptr("Old Co"), "plan": ptr("Gold")}})
	if err != nil {
		t.Fatal(err)
	}
	same, err := h.svc.Create(ctx, h.acc, CreateInput{Email: "same@example.com", Fields: map[string]*string{"company": ptr("Same")}})
	if err != nil {
		t.Fatal(err)
	}

	long := strings.Repeat("x", MaxFieldValueLen+20)
	csv := "email,Company,plan,City,Notes\n" +
		"pat@example.com,Acme,Silver,Paris,\"a\nb\"\n" + // new contact, every value
		"kim@example.com, ,,Rome," + long + "\n" + // blank company and plan set nothing
		"old@example.com,New Co,,Lyon,\n" + // existing: company changes, plan stays, city added
		"same@example.com,Same,,,\n" // existing, nothing changes
	im := h.importFile(t, ImportInput{ListSource: ListSourceForm, CSV: []byte(csv),
		NewFields: []NewImportField{{Column: "City"}, {Column: "Notes", Name: "Notes field"}}})
	if im.Status != ImportDone || im.Imported != 2 || im.SkippedByReason[SkipExists] != 2 || im.ExistingUpdated != 1 {
		t.Fatalf("import = %+v", im)
	}
	if h.fieldCount(t, h.acc) != 4 {
		t.Errorf("fields = %d, want 4", h.fieldCount(t, h.acc))
	}
	var cols []string
	for _, fc := range im.FieldColumns {
		cols = append(cols, fmt.Sprintf("%s<%s:%v", fc.Key, fc.Column, fc.Created))
	}
	if g := strings.Join(cols, " "); g != "company<Company:false plan<plan:false city<City:true notes_field<Notes:true" {
		t.Errorf("field columns = %q", g)
	}

	want := map[string]map[string]string{
		"pat@example.com":  {"company": "Acme", "plan": "Silver", "city": "Paris", "notes_field": "a b"},
		"kim@example.com":  {"city": "Rome", "notes_field": strings.Repeat("x", MaxFieldValueLen)},
		"old@example.com":  {"company": "New Co", "plan": "Gold", "city": "Lyon"},
		"same@example.com": {"company": "Same"},
	}
	for email, fields := range want {
		c := h.byEmail(t, email)
		if fmt.Sprint(c.Fields) != fmt.Sprint(fields) {
			t.Errorf("%s fields = %v, want %v", email, c.Fields, fields)
		}
	}
	if c := h.byEmail(t, "same@example.com"); !c.UpdatedAt.Equal(same.UpdatedAt) {
		t.Errorf("an unchanged contact was touched: %v, was %v", c.UpdatedAt, same.UpdatedAt)
	}
	if c := h.byEmail(t, "old@example.com"); c.FirstName != nil || c.Status != old.Status {
		t.Errorf("only fields change on an existing contact: %+v", c)
	}

	// A file without the plan column leaves plan out, and by id works too.
	im = h.importFile(t, ImportInput{ListSource: ListSourceForm, CSV: []byte("email,Tier\nold@example.com,Bronze\n"),
		FieldColumns: map[string]string{plan.ID: "Tier"}})
	if c := h.byEmail(t, "old@example.com"); c.Fields["plan"] != "Bronze" || im.ExistingUpdated != 1 {
		t.Errorf("by id: %v, updated %d", c.Fields, im.ExistingUpdated)
	}
}

func TestImportNewFieldsAllOrNothing(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.field(t, h.acc, "Company")
	csv := []byte("email,Company!,Size,Region\npat@example.com,Acme,12,EU\n")
	create := func(news ...NewImportField) error {
		_, err := h.svc.CreateImport(ctx, h.acc, ImportInput{ListSource: ListSourceForm, CSV: csv, FieldColumns: map[string]string{"company": ""},
			NewFields: news})
		return err
	}

	// A name or key that's taken, by an existing field or another new one.
	for _, news := range [][]NewImportField{
		{{Column: "Size"}, {Column: "Region", Name: "company"}},
		{{Column: "Size"}, {Column: "Company!"}},
		{{Column: "Size", Name: "Area"}, {Column: "Region", Name: "AREA"}},
	} {
		if err := create(news...); importCode(err) != "field_exists" {
			t.Errorf("%v: %v", news, err)
		}
	}
	// A name the rules refuse, and a column that isn't there.
	if err := create(NewImportField{Column: "Size", Name: "Email"}); importCode(err) != "reserved_field_name" {
		t.Errorf("reserved: %v", err)
	}
	if err := create(NewImportField{Column: "Size", Name: strings.Repeat("n", MaxFieldNameLen+1)}); importCode(err) != "invalid_field_name" {
		t.Errorf("long name: %v", err)
	}
	if err := create(NewImportField{Column: "Nope"}); importCode(err) != "invalid_mapping" {
		t.Errorf("no column: %v", err)
	}

	// Past the limit: 18 more fields make 19 of 20, so two new ones are one
	// too many.
	for i := range 18 {
		h.field(t, h.acc, fmt.Sprintf("Extra %d", i))
	}
	err := create(NewImportField{Column: "Size"}, NewImportField{Column: "Region"})
	if importCode(err) != "field_limit" || !strings.Contains(err.Error(), "this one has 19, so 1 more can be made") {
		t.Errorf("limit: %v", err)
	}
	if n := h.fieldCount(t, h.acc); n != 19 {
		t.Errorf("fields = %d after refusals, want 19", n)
	}
	if n := h.importCount(t, h.acc); n != 0 {
		t.Errorf("imports = %d after refusals, want 0", n)
	}
	p, err := h.svc.PreviewImport(ctx, h.acc, ImportInput{CSV: csv, NewFields: []NewImportField{{Column: "Size"}, {Column: "Region"}}})
	if err != nil || p.Problem == nil || p.Problem.Code != "field_limit" {
		t.Errorf("preview at the limit: %v %+v", err, p.Problem)
	}

	// One fits.
	if err := create(NewImportField{Column: "Size"}); err != nil {
		t.Fatalf("one new field: %v", err)
	}
	if n := h.fieldCount(t, h.acc); n != 20 {
		t.Errorf("fields = %d, want 20", n)
	}
}

func TestImportSkipsFieldDeletedBeforeTheWorker(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	company := h.field(t, h.acc, "Company")
	h.field(t, h.acc, "Plan")
	h.add(t, "old@example.com", StatusSubscribed)
	csv := "email,Company,Plan\npat@example.com,Acme,Gold\nold@example.com,Old Co,Silver\n"
	im, err := h.svc.CreateImport(ctx, h.acc, ImportInput{ListSource: ListSourceForm, CSV: []byte(csv)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.DeleteField(ctx, h.acc, company.ID); err != nil {
		t.Fatal(err)
	}
	h.runImports(t)
	im, err = h.svc.GetImport(ctx, h.acc, im.ID)
	if err != nil {
		t.Fatal(err)
	}
	if im.Status != ImportDone || im.Imported != 1 || len(im.FieldColumns) != 1 || im.FieldColumns[0].Key != "plan" {
		t.Fatalf("import = %+v", im)
	}
	if c := h.byEmail(t, "pat@example.com"); fmt.Sprint(c.Fields) != "map[plan:Gold]" {
		t.Errorf("new contact fields = %v", c.Fields)
	}
	if c := h.byEmail(t, "old@example.com"); fmt.Sprint(c.Fields) != "map[plan:Silver]" {
		t.Errorf("existing contact fields = %v", c.Fields)
	}
}

func TestPastedImportMakesANamedField(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	text := []byte("Pat\tpat@example.com\tAcme\nSam\tsam@example.com\t\n")
	in := ImportInput{ListSource: ListSourceCustomers, CSV: text, NoHeader: true, FirstNameColumn: "#1",
		NewFields: []NewImportField{{Column: "#3"}}}
	// With no header there's nothing to name the field after.
	if _, err := h.svc.CreateImport(ctx, h.acc, in); importCode(err) != "invalid_field_name" || !strings.Contains(err.Error(), "Column 3 has no header") {
		t.Fatalf("unnamed: %v", err)
	}
	p, err := h.svc.PreviewImport(ctx, h.acc, in)
	if err != nil || p.Problem == nil || p.Problem.Code != "invalid_field_name" {
		t.Errorf("preview unnamed: %v %+v", err, p.Problem)
	}

	in.NewFields[0].Name = "Company"
	p, err = h.svc.PreviewImport(ctx, h.acc, in)
	if err != nil || p.Problem != nil || p.Columns[2].NewField == nil || p.Columns[2].NewField.Key != "company" {
		t.Fatalf("preview named: %v %+v", err, p)
	}
	im := h.importFile(t, in)
	if im.Imported != 2 || len(im.FieldColumns) != 1 || im.FieldColumns[0].Column != "#3" || !im.FieldColumns[0].Created {
		t.Fatalf("import = %+v", im)
	}
	if c := h.byEmail(t, "pat@example.com"); c.Fields["company"] != "Acme" {
		t.Errorf("pat = %v", c.Fields)
	}
	if c := h.byEmail(t, "sam@example.com"); len(c.Fields) != 0 {
		t.Errorf("sam = %v", c.Fields)
	}
}
