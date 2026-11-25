package mailchimp

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/gremlin-technologies/mailgoose/internal/emails"
)

func tagSummary(ts []MergeTag) string {
	var out []string
	for _, t := range ts {
		s := t.Tag
		if t.MergeField != "" {
			s += " (" + t.MergeField + ")"
		}
		switch {
		case t.FieldKey == "":
			s += " removed"
		case t.NewField:
			s += " -> new " + t.FieldKey
		default:
			s += " -> " + t.FieldKey
		}
		out = append(out, s)
	}
	return strings.Join(out, "; ")
}

// MG-134: a connected account knows each merge field's tag, so MMERGE3,
// the merge field named Company, becomes the field the same import makes
// from it, in the HTML, the subject and the preview text.
func TestConvertHTMLConnectedAccountTag(t *testing.T) {
	company := &TagField{Key: "company", Name: "Company", New: true}
	ft := &FieldTags{
		MergeFields: []TagMergeField{{Name: "Company", Tags: []string{"MMERGE3"}, Field: company}},
		Fields:      []*TagField{company},
	}
	src := `<html><body><p>Hello *|FNAME|* at *|MMERGE3|* and *|mmerge3|*</p>` +
		`<a href="https://shop.example/?c=*|MMERGE3|*">Shop</a><a href="*|UNSUB|*">Unsubscribe</a></body></html>`
	c := ConvertHTML(src, "News for *|MMERGE3|*", "Just for *|MMERGE3|*", ft)
	if c.Subject != "News for {{company}}" || c.PreviewText != "Just for {{company}}" {
		t.Errorf("subject %q, preview %q", c.Subject, c.PreviewText)
	}
	for _, want := range []string{"Hello {{first_name}} at {{company}} and {{company}}", `href="https://shop.example/?c={{company}}"`} {
		if !strings.Contains(c.HTML, want) {
			t.Errorf("missing %q in %s", want, c.HTML)
		}
	}
	if len(c.Warnings) != 0 || len(c.Errors) != 0 {
		t.Errorf("notes %+v %+v", c.Warnings, c.Errors)
	}
	if got, want := tagSummary(c.MergeTags), "*|MMERGE3|* (Company) -> new company"; got != want {
		t.Errorf("merge tags %q, want %q", got, want)
	}
	if errs, _ := emails.CheckHTML(c.HTML); len(errs) != 0 {
		t.Errorf("checks %v", errs)
	}
	// Without the tag, the same name alone doesn't make MMERGE3 a match.
	ft.MergeFields[0].Tags = []string{"MMERGE4"}
	ft.Fields = nil
	if m := ft.Match("MMERGE3"); m.Field != nil || m.MergeField != "" {
		t.Errorf("MMERGE3 matched %+v", m)
	}
}

// An upload knows merge fields only by name: a tag matches a merge field
// whose name matches it loosely, Mailchimp's default names included, then
// the account's fields by key and name.
func TestConvertHTMLUploadByName(t *testing.T) {
	bread := &TagField{Key: "bread", Name: "Bread", New: true}
	phone := &TagField{Key: "phone_number", Name: "Phone Number"}
	city := &TagField{Key: "town", Name: "City"}
	ft := &FieldTags{
		MergeFields: []TagMergeField{
			// Made in this import under another name.
			{Name: "Favourite bread", Field: bread},
			{Name: "Phone Number", Field: phone},
		},
		Fields: []*TagField{phone, city, bread},
	}
	src := `<p>*|FAVOURITE_BREAD|* *|PHONE|* *|CITY|* *|BREAD|*</p><a href="*|UNSUB|*">u</a>`
	c := ConvertHTML(src, "", "", ft)
	if !strings.Contains(c.HTML, "<p>{{bread}} {{phone_number}} {{town}} {{bread}}</p>") {
		t.Errorf("html %s", c.HTML)
	}
	want := "*|FAVOURITE_BREAD|* (Favourite bread) -> new bread; *|PHONE|* (Phone Number) -> phone_number; *|CITY|* -> town; *|BREAD|* -> new bread"
	if got := tagSummary(c.MergeTags); got != want {
		t.Errorf("merge tags %q, want %q", got, want)
	}
	if len(c.Warnings) != 0 {
		t.Errorf("warnings %+v", c.Warnings)
	}
}

// A tag with no field is still taken out, and its note says which field
// to add, or that its merge field was left out of this import.
func TestConvertHTMLUnmatchedTags(t *testing.T) {
	recorded := &TagField{Key: "org", Name: "Organisation", Tags: []string{"MMERGE9"}}
	ft := &FieldTags{
		MergeFields: []TagMergeField{{Name: "Birthday"}},
		Fields:      []*TagField{recorded},
	}
	src := `<p>*|BIRTHDAY|* *|FAV_CAKE|* *|MMERGE9|* *|ARCHIVE|* *|LIST:NAME|*</p><a href="*|UNSUB|*">u</a>`
	c := ConvertHTML(src, "", "", ft)
	if !strings.Contains(c.HTML, "<p>  {{org}}  </p>") {
		t.Errorf("html %s", c.HTML)
	}
	if got, want := tagSummary(c.MergeTags),
		"*|BIRTHDAY|* (Birthday) removed; *|FAV_CAKE|* removed; *|MMERGE9|* -> org; *|ARCHIVE|* removed; *|LIST:NAME|* removed"; got != want {
		t.Errorf("merge tags %q, want %q", got, want)
	}
	if got := codes(c.Warnings); !slices.Equal(got, []string{NoteRemovedTag, NoteRemovedTag, NoteRemovedTag, NoteRemovedTag}) {
		t.Fatalf("warnings %+v", c.Warnings)
	}
	for i, want := range []string{
		`is the merge field "Birthday", which this import leaves out of your contact fields`,
		`add a contact field named "Fav cake" and use its tag, {{fav_cake}}`,
		"*|ARCHIVE|* merge tag has no match in mailgoose",
		"*|LIST:NAME|* merge tag has no match in mailgoose",
	} {
		if !strings.Contains(c.Warnings[i].Message, want) {
			t.Errorf("warning %d = %q, want %q", i, c.Warnings[i].Message, want)
		}
	}
	// With no fields at all, every tag but the built-in ones goes, as
	// before MG-134.
	c = ConvertHTML(src, "", "", nil)
	if !strings.Contains(c.HTML, "<p>    </p>") || len(c.Warnings) != 5 {
		t.Errorf("no fields: %s %+v", c.HTML, c.Warnings)
	}
}

// A merge tag can't decide where a link goes, so a tag that becomes a
// field's tag is still taken out of a link it was the whole of.
func TestConvertHTMLFieldTagAsWholeLink(t *testing.T) {
	site := &TagField{Key: "website", Name: "Website"}
	ft := &FieldTags{Fields: []*TagField{site}}
	src := `<p>*|WEBSITE|*</p><a href="*|WEBSITE|*">Site</a><a href="*|UNSUB|*">u</a>`
	c := ConvertHTML(src, "", "", ft)
	if !strings.Contains(c.HTML, `<p>{{website}}</p><a href="">Site</a>`) {
		t.Errorf("html %s", c.HTML)
	}
	if len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0].Message, "became {{website}}, except where it was a link's whole address") {
		t.Errorf("warnings %+v", c.Warnings)
	}
	if errs, _ := emails.CheckHTML(c.HTML); len(errs) != 0 {
		t.Errorf("checks %v", errs)
	}
}

func TestFieldTagsMatchOrder(t *testing.T) {
	byTag := &TagField{Key: "organisation", Name: "Organisation", Tags: []string{"COMPANY"}}
	byKey := &TagField{Key: "company", Name: "Employer"}
	byName := &TagField{Key: "biz", Name: "Company"}
	made := &TagField{Key: "firm", Name: "Firm", New: true}
	for _, tc := range []struct {
		ft   *FieldTags
		want string
	}{
		// The import's own merge field comes first.
		{&FieldTags{MergeFields: []TagMergeField{{Name: "Company", Field: made}}, Fields: []*TagField{byName, byKey, byTag, made}}, "firm"},
		// Then a tag an earlier import recorded, then key, then name.
		{&FieldTags{Fields: []*TagField{byName, byKey, byTag}}, "organisation"},
		{&FieldTags{Fields: []*TagField{byName, byKey}}, "company"},
		{&FieldTags{Fields: []*TagField{byName}}, "biz"},
		// A merge field left out still lets a field match.
		{&FieldTags{MergeFields: []TagMergeField{{Name: "Company"}}, Fields: []*TagField{byKey}}, "company"},
		{nil, ""},
	} {
		m := tc.ft.Match(" company ")
		got := ""
		if m.Field != nil {
			got = m.Field.Key
		}
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", fmt.Sprint(tc.ft), got, tc.want)
		}
	}
}

func TestSuggestedField(t *testing.T) {
	for tag, want := range map[string][2]string{
		"PHONE": {"Phone Number", "phone_number"}, "FAV_CAKE": {"Fav cake", "fav_cake"}, "2ND": {"2nd", "f_2nd"}, "__": {"", ""},
	} {
		if n, k := suggestedField(tag); n != want[0] || k != want[1] {
			t.Errorf("%s: %q %q, want %v", tag, n, k, want)
		}
	}
}
