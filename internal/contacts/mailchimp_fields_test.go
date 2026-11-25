package contacts

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"testing"

	"github.com/gremlin-technologies/mailgoose/internal/mailchimp"
)

// Mailchimp merge fields as contact fields (MG-132).

// withMerge gives an audience merge fields, and each member, in order, its
// values for them.
func withMerge(a *mailchimp.Audience, fields []string, values ...[]string) *mailchimp.Audience {
	a.MergeFields = fields
	for i, v := range values {
		a.Members[i].Merge = v
	}
	return a
}

// choices sums up a field plan's choices as "merge -> field" lines.
func choices(p *MailchimpFieldPlan) []string {
	var out []string
	for _, c := range p.Choices {
		switch {
		case c.Field != nil:
			out = append(out, c.Name+" -> "+c.Field.Key)
		case c.NewField != nil:
			out = append(out, c.Name+" -> new "+c.NewField.Key)
		case c.SkipReason != "":
			out = append(out, c.Name+" -> skip "+c.SkipReason)
		default:
			out = append(out, c.Name+" -> skip")
		}
	}
	return out
}

func (h *harness) mailchimpImportWith(t *testing.T, b *mailchimp.Bundle, in MailchimpFieldInput) (*Import, error) {
	t.Helper()
	im, _, err := h.svc.CreateMailchimpImport(context.Background(), h.acc,
		MailchimpImportInput{Bundle: b, FileName: "export.zip", PermissionConfirmed: true, Fields: in})
	if err != nil {
		return nil, err
	}
	h.runImports(t)
	return h.svc.GetImport(context.Background(), h.acc, im.ID)
}

func TestMailchimpMergeFieldsPastTheFieldLimit(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	// 17 fields of 20, one of them Company, so 3 more can be made.
	h.field(t, h.acc, "Company")
	for i := range 16 {
		h.field(t, h.acc, fmt.Sprintf("Old %d", i))
	}
	merge := []string{"Phone", "COMPANY", "Birthday", "Address", "Shoe size", "Status", "Pet"}
	a := withMerge(mcAudience("Shop", "pat@example.com subscribed", "lee@example.com subscribed"), merge,
		[]string{"555-0100", "Acme", "12/10", "1 Main St, Leeds", "9", "Gold", "Rex"},
		[]string{"", "Initech", "", "", "", "", "Tom"})
	b := mcBundle(a)

	// The preview matches Company by name, leaves Status out (a contact's
	// own column), makes fields for the first three others in Mailchimp's
	// order and leaves the rest out for the limit.
	p, err := h.svc.PreviewMailchimpImport(ctx, h.acc, b)
	if err != nil {
		t.Fatal(err)
	}
	fp := p.Fields
	want := []string{"Phone -> new phone", "COMPANY -> company", "Birthday -> new birthday", "Address -> new address",
		"Shoe size -> skip field_limit", "Status -> skip reserved_field_name", "Pet -> skip field_limit"}
	if got := choices(fp); !slices.Equal(got, want) {
		t.Errorf("choices\n got %q\nwant %q", got, want)
	}
	if fp.Limit != 20 || fp.Existing != 17 || fp.New != 3 || fp.LeftOut != 2 || fp.Problem != nil {
		t.Errorf("plan: limit %d, existing %d, new %d, left out %d, problem %v", fp.Limit, fp.Existing, fp.New, fp.LeftOut, fp.Problem)
	}
	if c := fp.Choices[1]; c.Members != 2 || !slices.Equal(c.Samples, []string{"Acme", "Initech"}) || !slices.Equal(c.Audiences, []string{"Shop"}) {
		t.Errorf("COMPANY = %+v", c.MailchimpMergeField)
	}
	if h.fieldCount(t, h.acc) != 17 {
		t.Fatal("the preview made fields")
	}

	// Asking for more new fields than there's room for is refused, and
	// nothing is made or queued.
	four := []NewImportField{{Column: "Phone"}, {Column: "Birthday"}, {Column: "Pet"}, {Column: "Shoe size"}}
	if _, err := h.mailchimpImportWith(t, b, MailchimpFieldInput{NewFields: &four}); importCode(err) != "field_limit" {
		t.Fatalf("four new fields with room for three: %v", err)
	}
	if h.fieldCount(t, h.acc) != 17 || h.importCount(t, h.acc) != 0 {
		t.Fatal("a refused import made fields or an import")
	}

	// The person picks which: Pet instead of Address, Status as "Member
	// status", and Shoe size into an existing field, which works past the
	// limit.
	news := []NewImportField{{Column: "Phone"}, {Column: "Pet"}, {Column: "Status", Name: "Member status"}}
	im, err := h.mailchimpImportWith(t, b, MailchimpFieldInput{NewFields: &news, FieldColumns: map[string]string{"old_0": "Shoe size"}})
	if err != nil {
		t.Fatal(err)
	}
	if im.Status != ImportDone || im.Imported != 2 {
		t.Fatalf("import = %+v", im)
	}
	var cols []string
	for _, fc := range im.FieldColumns {
		cols = append(cols, fmt.Sprintf("%s<-%s %v", fc.Key, fc.Column, fc.Created))
	}
	// In the fields' order: the existing ones, then the new ones.
	wantCols := []string{"company<-COMPANY false", "old_0<-Shoe size false", "phone<-Phone true", "pet<-Pet true",
		"member_status<-Status true"}
	if !slices.Equal(cols, wantCols) {
		t.Errorf("field columns\n got %q\nwant %q", cols, wantCols)
	}
	if h.fieldCount(t, h.acc) != 20 {
		t.Errorf("fields = %d, want 20", h.fieldCount(t, h.acc))
	}
	pat, lee := h.byEmail(t, "pat@example.com"), h.byEmail(t, "lee@example.com")
	wantPat := map[string]string{"company": "Acme", "old_0": "9", "phone": "555-0100", "pet": "Rex", "member_status": "Gold"}
	if !maps.Equal(pat.Fields, wantPat) {
		t.Errorf("pat's fields = %v, want %v", pat.Fields, wantPat)
	}
	if want := map[string]string{"company": "Initech", "pet": "Tom"}; !maps.Equal(lee.Fields, want) {
		t.Errorf("lee's fields = %v, want %v", lee.Fields, want)
	}

	// With the account at its limit, the default makes nothing and says
	// why; matching still works.
	fp, err = h.svc.PlanMailchimpFields(ctx, h.acc, p.Summary.MergeFields, MailchimpFieldInput{})
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"Phone -> phone", "COMPANY -> company", "Birthday -> skip field_limit", "Address -> skip field_limit",
		"Shoe size -> skip field_limit", "Status -> skip reserved_field_name", "Pet -> pet"}
	if got := choices(fp); !slices.Equal(got, want) || fp.LeftOut != 3 || fp.New != 0 {
		t.Errorf("at the limit\n got %q (left out %d)\nwant %q", got, fp.LeftOut, want)
	}
}

func TestMailchimpAudiencesShareAMergeField(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.add(t, "old@example.com", StatusSubscribed)
	shop := withMerge(mcAudience("Shop", "pat@example.com subscribed", "old@example.com subscribed"),
		[]string{"Phone", "Company"},
		[]string{"", "Acme"}, []string{"555-0199"})
	blog := withMerge(mcAudience("Blog", "pat@example.com subscribed", "kim@example.com subscribed"),
		[]string{"phone", "Website"},
		[]string{"555-0100"}, []string{"555-0111", "kim.example"})
	b := mcBundle(shop, blog)

	p, err := h.svc.PreviewMailchimpImport(ctx, h.acc, b)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Phone -> new phone", "Company -> new company", "Website -> new website"}; !slices.Equal(choices(p.Fields), want) {
		t.Errorf("choices = %q, want %q", choices(p.Fields), want)
	}
	phone := p.Summary.MergeFields[0]
	if !slices.Equal(phone.Audiences, []string{"Shop", "Blog"}) || phone.Members != 3 || len(phone.Samples) != 3 {
		t.Errorf("phone = %+v", phone)
	}

	im, err := h.mailchimpImportWith(t, b, MailchimpFieldInput{})
	if err != nil {
		t.Fatal(err)
	}
	if h.fieldCount(t, h.acc) != 3 {
		t.Errorf("fields = %d, want one each for phone, company and website", h.fieldCount(t, h.acc))
	}
	// pat is in both audiences: the phone comes from the one that has it.
	for email, want := range map[string]map[string]string{
		"pat@example.com": {"phone": "555-0100", "company": "Acme"},
		"kim@example.com": {"phone": "555-0111", "website": "kim.example"},
		// An existing contact takes the values too, as in a CSV import.
		"old@example.com": {"phone": "555-0199"},
	} {
		if got := h.byEmail(t, email).Fields; !maps.Equal(got, want) {
			t.Errorf("%s's fields = %v, want %v", email, got, want)
		}
	}
	if im.ExistingTagged != 1 || im.ExistingUpdated != 1 {
		t.Errorf("existing tagged %d, updated %d", im.ExistingTagged, im.ExistingUpdated)
	}
}

func TestMailchimpMergeFieldsLeftOut(t *testing.T) {
	h := newHarness(t)
	h.field(t, h.acc, "Phone")
	a := withMerge(mcAudience("Shop", "pat@example.com subscribed"), []string{"Phone", "Company"}, []string{"555-0100", "Acme"})
	b := mcBundle(a)

	// An unknown merge field or field is refused.
	for _, in := range []MailchimpFieldInput{
		{FieldColumns: map[string]string{"phone": "Fax"}},
		{FieldColumns: map[string]string{"fax": "Phone"}},
	} {
		if _, err := h.mailchimpImportWith(t, b, in); importCode(err) != "invalid_mapping" && importCode(err) != "unknown_field" {
			t.Errorf("%+v: %v", in, err)
		}
	}
	// No new fields and phone= leave every merge field out.
	none := []NewImportField{}
	im, err := h.mailchimpImportWith(t, b, MailchimpFieldInput{NewFields: &none, FieldColumns: map[string]string{"phone": ""}})
	if err != nil {
		t.Fatal(err)
	}
	if len(im.FieldColumns) != 0 || h.fieldCount(t, h.acc) != 1 || len(h.byEmail(t, "pat@example.com").Fields) != 0 {
		t.Errorf("left out: columns %+v, fields %v", im.FieldColumns, h.byEmail(t, "pat@example.com").Fields)
	}
}
