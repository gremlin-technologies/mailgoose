package contacts

import (
	"context"
	"slices"
	"testing"

	"github.com/gremlin-technologies/mailgoose/internal/mailchimp"
)

// Mailchimp merge tags as contact field tags (MG-134).

func matchKey(ft *mailchimp.FieldTags, tag string) string {
	m := ft.Match(tag)
	switch {
	case m.Field == nil && m.MergeField != "":
		return "left out (" + m.MergeField + ")"
	case m.Field == nil:
		return "none"
	case m.Field.New:
		return "new " + m.Field.Key
	}
	return m.Field.Key
}

// A connected account's bundle knows MMERGE3 is the merge field Company:
// the preview and the emails of the confirmed import match the tag to the
// field the import makes from it, and the field keeps the tag for later
// imports.
func TestMailchimpTagsOfAConnectedAccount(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.field(t, h.acc, "Phone")
	a := withMerge(mcAudience("Shop", "pat@example.com subscribed"), []string{"Company", "Phone", "Birthday"},
		[]string{"Acme", "555-0100", "12/10"})
	a.MergeTags = []string{"MMERGE3", "PHONE", "MMERGE7"}
	b := mcBundle(a)

	p, err := h.svc.PreviewMailchimpImport(ctx, h.acc, b)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.Fields.Choices[0].Tags, []string{"MMERGE3"}) {
		t.Errorf("Company's tags = %v", p.Fields.Choices[0].Tags)
	}
	ft, err := h.svc.MailchimpPreviewFieldTags(ctx, h.acc, p.Fields)
	if err != nil {
		t.Fatal(err)
	}
	for tag, want := range map[string]string{"MMERGE3": "new company", "PHONE": "phone", "MMERGE7": "new birthday", "COMPANY": "new company", "FAX": "none"} {
		if got := matchKey(ft, tag); got != want {
			t.Errorf("preview %s = %q, want %q", tag, got, want)
		}
	}

	// Birthday is left out of the import, so its tag is too.
	im, err := h.mailchimpImportWith(t, b, MailchimpFieldInput{NewFields: &[]NewImportField{{Column: "Company"}}})
	if err != nil {
		t.Fatal(err)
	}
	ft, err = h.svc.MailchimpImportFieldTags(ctx, h.acc, &im.ID, b)
	if err != nil {
		t.Fatal(err)
	}
	for tag, want := range map[string]string{"MMERGE3": "new company", "PHONE": "phone", "MMERGE7": "left out (Birthday)"} {
		if got := matchKey(ft, tag); got != want {
			t.Errorf("import %s = %q, want %q", tag, got, want)
		}
	}

	// A later upload of templates alone still finds Company by its tag,
	// no longer as a new field.
	ft, err = h.svc.MailchimpImportFieldTags(ctx, h.acc, nil, &mailchimp.Bundle{})
	if err != nil {
		t.Fatal(err)
	}
	for tag, want := range map[string]string{"MMERGE3": "company", "PHONE": "phone", "MMERGE7": "none"} {
		if got := matchKey(ft, tag); got != want {
			t.Errorf("later %s = %q, want %q", tag, got, want)
		}
	}

	// Another import that puts MMERGE3 into another field moves the tag.
	h.field(t, h.acc, "Organisation")
	a2 := withMerge(mcAudience("Shop", "lee@example.com subscribed"), []string{"Org"}, []string{"Initech"})
	a2.MergeTags = []string{"MMERGE3"}
	if _, err := h.mailchimpImportWith(t, mcBundle(a2), MailchimpFieldInput{FieldColumns: map[string]string{"organisation": "Org"},
		NewFields: &[]NewImportField{}}); err != nil {
		t.Fatal(err)
	}
	ft, err = h.svc.MailchimpImportFieldTags(ctx, h.acc, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := matchKey(ft, "MMERGE3"); got != "organisation" {
		t.Errorf("moved MMERGE3 = %q", got)
	}
}

// An upload knows merge fields only by name; a field the same import makes
// under another name still takes the tag that matches the merge field's
// name.
func TestMailchimpTagsOfAnUpload(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	b := mcBundle(withMerge(mcAudience("Bakery", "pat@example.com subscribed"), []string{"Favourite bread"}, []string{"Rye"}))
	im, err := h.mailchimpImportWith(t, b, MailchimpFieldInput{NewFields: &[]NewImportField{{Column: "Favourite bread", Name: "Bread"}}})
	if err != nil {
		t.Fatal(err)
	}
	ft, err := h.svc.MailchimpImportFieldTags(ctx, h.acc, &im.ID, b)
	if err != nil {
		t.Fatal(err)
	}
	for tag, want := range map[string]string{"FAVOURITE_BREAD": "new bread", "BREAD": "new bread", "MMERGE1": "none"} {
		if got := matchKey(ft, tag); got != want {
			t.Errorf("%s = %q, want %q", tag, got, want)
		}
	}
	// No tags were known, so none was kept.
	var tags []string
	if err := h.svc.DB.QueryRow(ctx, `SELECT mailchimp_tags FROM contact_fields WHERE account_id = $1`, h.acc).Scan(&tags); err != nil ||
		len(tags) != 0 {
		t.Errorf("tags = %v, %v", tags, err)
	}
}
