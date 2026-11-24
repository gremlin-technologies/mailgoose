package contacts

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func (h *harness) field(t *testing.T, account, name string) *Field {
	t.Helper()
	f, err := h.svc.CreateField(context.Background(), account, name)
	if err != nil {
		t.Fatalf("create field %s: %v", name, err)
	}
	return f
}

func TestFieldKey(t *testing.T) {
	for name, want := range map[string]string{
		"Company":          "company",
		"Company size":     "company_size",
		"  Plan -- Tier! ": "plan_tier",
		"2nd email":        "f_2nd_email",
		"__x__":            "x",
		"Größe":            "gr_e",
		"!!!":              "",
		"日本":               "",
	} {
		if got := FieldKey(name); got != want {
			t.Errorf("FieldKey(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestFieldLimitPerPlan(t *testing.T) {
	if FieldLimit("free") != 20 || FieldLimit("paid") != 20 {
		t.Errorf("limits = %d free, %d paid; want 20 on both", FieldLimit("free"), FieldLimit("paid"))
	}
}

func TestFieldCreateRenameList(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	size := h.field(t, h.acc, " Company size ")
	if size.Name != "Company size" || size.Key != "company_size" {
		t.Errorf("field = %+v", size)
	}
	if _, err := h.svc.CreateField(ctx, h.acc, "COMPANY SIZE"); !errors.Is(err, ErrFieldExists) {
		t.Errorf("same name, other case: %v", err)
	}
	var ke *FieldKeyExistsError
	if _, err := h.svc.CreateField(ctx, h.acc, "Company-size!"); !errors.As(err, &ke) || ke.Key != "company_size" {
		t.Errorf("same key: %v", err)
	}
	if _, err := h.svc.CreateField(ctx, h.acc2, "Company size"); err != nil {
		t.Errorf("other account: %v", err)
	}
	for _, bad := range []string{"", "  ", "a\nb", "!!!", strings.Repeat("x", MaxFieldNameLen+1)} {
		if _, err := h.svc.CreateField(ctx, h.acc, bad); code(err) != "invalid_field_name" {
			t.Errorf("%q: %v", bad, err)
		}
	}
	for _, reserved := range []string{"Email", "First name", "tags", "Status", "ID", "Created at", "Signup IP", "permission confirmed"} {
		if _, err := h.svc.CreateField(ctx, h.acc, reserved); code(err) != "reserved_field_name" {
			t.Errorf("%q: %v", reserved, err)
		}
	}
	plan := h.field(t, h.acc, "Plan")
	h.field(t, h.acc, "2nd email")

	if _, err := h.svc.RenameField(ctx, h.acc, plan.ID, "company size"); !errors.Is(err, ErrFieldExists) {
		t.Errorf("rename onto an existing name: %v", err)
	}
	if _, err := h.svc.RenameField(ctx, h.acc, plan.ID, "Email"); code(err) != "reserved_field_name" {
		t.Errorf("rename to a reserved name: %v", err)
	}
	r, err := h.svc.RenameField(ctx, h.acc, plan.ID, "Plan tier")
	if err != nil || r.Name != "Plan tier" || r.Key != "plan" {
		t.Errorf("rename keeps the key: %+v %v", r, err)
	}
	if _, err := h.svc.RenameField(ctx, h.acc2, plan.ID, "x"); !errors.Is(err, ErrFieldNotFound) {
		t.Errorf("rename from another account: %v", err)
	}

	var keys []string
	var after *FieldCursor
	for {
		page, err := h.svc.ListFields(ctx, h.acc, after, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range page {
			keys = append(keys, f.Key)
		}
		if len(page) < 2 {
			break
		}
		last := page[len(page)-1]
		after = &FieldCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	if want := []string{"company_size", "plan", "f_2nd_email"}; !slices.Equal(keys, want) {
		t.Errorf("fields in order made = %v, want %v", keys, want)
	}
}

func TestFieldLimit(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for i := range FreeFieldLimit {
		h.field(t, h.acc, fmt.Sprintf("Field %d", i))
	}
	_, err := h.svc.CreateField(ctx, h.acc, "One more")
	var le *FieldLimitError
	if !errors.As(err, &le) || le.Limit != FreeFieldLimit || le.Count != FreeFieldLimit {
		t.Fatalf("field past the limit: %v", err)
	}
	// The paid plan has its own limit, 20 today too.
	if _, err := h.svc.DB.Exec(ctx, `UPDATE accounts SET plan = 'paid' WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.CreateField(ctx, h.acc, "One more"); !errors.As(err, &le) || le.Limit != PaidFieldLimit {
		t.Errorf("paid account past the limit: %v", err)
	}
	all, err := h.svc.AllFields(ctx, h.acc)
	if err != nil || len(all) != FreeFieldLimit {
		t.Fatalf("fields = %d, %v", len(all), err)
	}
	// Deleting one makes room.
	if _, err := h.svc.DeleteField(ctx, h.acc, all[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.CreateField(ctx, h.acc, "One more"); err != nil {
		t.Errorf("after a delete: %v", err)
	}
}

func TestFieldValues(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.field(t, h.acc, "Company")
	h.field(t, h.acc, "Plan")
	h.field(t, h.acc2, "Region")

	c, err := h.svc.Create(ctx, h.acc, CreateInput{Email: "pat@example.com", Fields: map[string]*string{
		"company": str(" Acme "), "plan": str(""),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Fields) != 1 || c.Fields["company"] != "Acme" {
		t.Errorf("created with fields = %v, want only company=Acme", c.Fields)
	}
	plain := h.add(t, "sam@example.com", "")
	if plain.Fields == nil || len(plain.Fields) != 0 {
		t.Errorf("contact with no values = %#v, want an empty map", plain.Fields)
	}

	// Unknown keys, and another account's keys, are refused.
	for _, k := range []string{"nope", "region"} {
		_, err := h.svc.Create(ctx, h.acc, CreateInput{Email: "x@example.com", Fields: map[string]*string{k: str("y")}})
		if code(err) != "unknown_field" || !strings.Contains(err.Error(), k) {
			t.Errorf("create with %s: %v", k, err)
		}
		if _, err := h.svc.Update(ctx, h.acc, c.ID, UpdateInput{Fields: map[string]*string{k: str("y")}}); code(err) != "unknown_field" {
			t.Errorf("update with %s: %v", k, err)
		}
	}
	// A value is at most 500 characters on one line.
	long := strings.Repeat("é", MaxFieldValueLen)
	if _, err := h.svc.Update(ctx, h.acc, c.ID, UpdateInput{Fields: map[string]*string{"plan": &long}}); err != nil {
		t.Errorf("500 characters: %v", err)
	}
	for _, bad := range []string{long + "x", "a\nb"} {
		if _, err := h.svc.Update(ctx, h.acc, c.ID, UpdateInput{Fields: map[string]*string{"plan": str(bad)}}); code(err) != "invalid_field_value" {
			t.Errorf("value %.10q…: %v", bad, err)
		}
	}

	// Keys left out stay; "" and nil clear.
	c, err = h.svc.Update(ctx, h.acc, c.ID, UpdateInput{Fields: map[string]*string{"plan": str("Pro")}})
	if err != nil || c.Fields["company"] != "Acme" || c.Fields["plan"] != "Pro" {
		t.Fatalf("set plan: %v %v", c.Fields, err)
	}
	c, err = h.svc.Update(ctx, h.acc, c.ID, UpdateInput{LastName: set("Lee")})
	if err != nil || len(c.Fields) != 2 {
		t.Errorf("an update without fields keeps them: %v %v", c.Fields, err)
	}
	c, err = h.svc.Update(ctx, h.acc, c.ID, UpdateInput{Fields: map[string]*string{"company": nil, "plan": str(" ")}})
	if err != nil || len(c.Fields) != 0 {
		t.Errorf("clear both: %v %v", c.Fields, err)
	}
	got, err := h.svc.Get(ctx, h.acc, c.ID)
	if err != nil || len(got.Fields) != 0 {
		t.Errorf("get after clearing: %v %v", got.Fields, err)
	}
}

func TestDeleteFieldRemovesValues(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	company := h.field(t, h.acc, "Company")
	h.field(t, h.acc, "Plan")
	other := h.field(t, h.acc2, "Company")
	for i, email := range []string{"a@example.com", "b@example.com", "c@example.com"} {
		in := CreateInput{Email: email, Fields: map[string]*string{"plan": str("Pro")}}
		if i < 2 {
			in.Fields["company"] = str("Acme")
		}
		if _, err := h.svc.Create(ctx, h.acc, in); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.svc.Create(ctx, h.acc2, CreateInput{Email: "a@example.com", Fields: map[string]*string{"company": str("Other")}}); err != nil {
		t.Fatal(err)
	}

	if _, err := h.svc.DeleteField(ctx, h.acc2, company.ID); !errors.Is(err, ErrFieldNotFound) {
		t.Errorf("delete from another account: %v", err)
	}
	d, err := h.svc.DeleteField(ctx, h.acc, company.ID)
	if err != nil || d.ValuesDeleted != 2 {
		t.Fatalf("delete = %+v, %v; want 2 values deleted", d, err)
	}
	cs, err := h.svc.List(ctx, h.acc, Filter{}, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		if _, ok := c.Fields["company"]; ok || c.Fields["plan"] != "Pro" {
			t.Errorf("%s fields after the delete = %v", c.Email, c.Fields)
		}
	}
	// A new field with the same key starts empty.
	h.field(t, h.acc, "Company")
	cs, _ = h.svc.List(ctx, h.acc, Filter{}, nil, 10)
	for _, c := range cs {
		if _, ok := c.Fields["company"]; ok {
			t.Errorf("%s has an old value under a new field: %v", c.Email, c.Fields)
		}
	}
	// The other account's field and values are untouched.
	if _, err := h.svc.GetField(ctx, h.acc2, other.ID); err != nil {
		t.Errorf("other account's field: %v", err)
	}
	theirs, _ := h.svc.List(ctx, h.acc2, Filter{}, nil, 10)
	if len(theirs) != 1 || theirs[0].Fields["company"] != "Other" {
		t.Errorf("other account's contacts = %v", theirs)
	}
	if _, err := h.svc.DeleteField(ctx, h.acc, company.ID); !errors.Is(err, ErrFieldNotFound) {
		t.Errorf("delete twice: %v", err)
	}
}

func TestExportFieldColumns(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.field(t, h.acc, "Company size")
	plan := h.field(t, h.acc, "Plan")
	if _, err := h.svc.Create(ctx, h.acc, CreateInput{Email: "pat@example.com", Fields: map[string]*string{
		"company_size": str("50"), "plan": str("=SUM(A1)"),
	}}); err != nil {
		t.Fatal(err)
	}
	h.add(t, "sam@example.com", "")
	if _, err := h.svc.RenameField(ctx, h.acc, plan.ID, "Plan tier"); err != nil {
		t.Fatal(err)
	}

	recs := h.export(t, h.acc, Filter{})
	want := append(slices.Clone(ExportHeader), "Company size", "Plan tier")
	if !slices.Equal(recs[0], want) {
		t.Fatalf("header = %v, want %v", recs[0], want)
	}
	n := len(ExportHeader)
	if got := recs[1][n:]; !slices.Equal(got, []string{"50", "'=SUM(A1)"}) {
		t.Errorf("pat's field columns = %q", got)
	}
	if got := recs[2][n:]; !slices.Equal(got, []string{"", ""}) {
		t.Errorf("sam's field columns = %q", got)
	}
	// An account with no fields exports the usual columns only.
	if recs := h.export(t, h.acc2, Filter{}); !slices.Equal(recs[0], ExportHeader) {
		t.Errorf("header without fields = %v", recs[0])
	}
}
