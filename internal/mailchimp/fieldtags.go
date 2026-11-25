package mailchimp

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Merge tags as contact fields (MG-134). Since MG-132 a Mailchimp import
// brings merge fields over as contact fields, and since MG-133 a contact
// field is a merge tag, {{key}}. So a Mailchimp merge tag in an imported
// email becomes the tag of the contact field it stands for, when there is
// one, instead of being taken out.

// MergeTagsFile is the file a fetch from a connected account (MG-74) writes
// beside each audience's member files, naming each merge field column's
// Mailchimp tag (columns Tag and Name), since the member files name merge
// fields only by name. An upload doesn't have it: Mailchimp's own export
// doesn't say which tag a column is.
const MergeTagsFile = "merge_field_tags.csv"

// FieldTags is what a Mailchimp import's merge tags can become: its own
// merge fields and the contact fields each goes into, and the account's
// contact fields.
type FieldTags struct {
	// MergeFields are the import's merge fields, in Mailchimp's order.
	MergeFields []TagMergeField
	// Fields are the account's contact fields, in the order they were
	// made, then the ones the import makes.
	Fields []*TagField
}

// TagMergeField is one merge field of the import.
type TagMergeField struct {
	Name string
	// Tags are its Mailchimp tags (MMERGE3, COMPANY), where the import
	// knows them: a connected account's do, an upload's don't.
	Tags []string
	// Field is the contact field the import puts it into, one of
	// FieldTags.Fields; nil when the import leaves it out.
	Field *TagField
}

// TagField is a contact field.
type TagField struct {
	Key, Name string
	// Tags are the Mailchimp tags of the merge fields earlier Mailchimp
	// imports put into it, where they knew them.
	Tags []string
	// New is true for a field the import makes.
	New bool
}

// TagMatch is what a Mailchimp merge tag stands for.
type TagMatch struct {
	// MergeField names the import's merge field the tag is; "" when none.
	MergeField string
	// Field is the contact field the tag becomes; nil when none, so the
	// tag is taken out.
	Field *TagField
}

// systemTags are Mailchimp's own merge tags that fill something in per
// list or per send, never a contact's merge field, beside every tag with
// a colon in it (LIST:NAME, MC:DATE). They're taken out, as before.
var systemTags = map[string]bool{
	"ARCHIVE": true, "ARCHIVE_LINK_SHORT": true, "UPDATE_PROFILE": true, "FORWARD": true, "CURRENT_YEAR": true,
	"DATE": true, "REWARDS": true, "REWARDS_TEXT": true, "ABOUT_LIST": true, "EMAIL_TYPE": true, "LIST_NAME": true,
	"UNSUB": true, "FNAME": true, "LNAME": true, "EMAIL": true, "MC_PREVIEW_TEXT": true, "MC_LANGUAGE": true,
	"MC_LANGUAGE_LABEL": true, "UNIQID": true, "CAMPAIGN_UID": true,
}

// defaultMergeNames are the names Mailchimp gives the merge fields a new
// audience has, by tag, so an upload, which knows only names, still
// matches *|PHONE|* to its "Phone Number" column.
var defaultMergeNames = map[string]string{
	"ADDRESS": "Address", "PHONE": "Phone Number", "BIRTHDAY": "Birthday", "COMPANY": "Company",
}

// FieldTag reports whether tag, as written between *| and |*, could be a
// contact's merge field: not one of Mailchimp's own tags.
func FieldTag(tag string) bool {
	tag = strings.ToUpper(strings.TrimSpace(tag))
	return tag != "" && !strings.Contains(tag, ":") && !systemTags[tag] && !conditional(tag)
}

// looseKey compares names, keys and tags loosely, as MG-131 matches a
// header: case, spaces, underscores, dashes and dots ignored.
func looseKey(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '_', '-', '.':
			return -1
		}
		return r
	}, strings.ToLower(strings.TrimSpace(s)))
}

// tagKeys are the loose keys a tag matches a name by: its own, and the
// default name of Mailchimp's for it.
func tagKeys(tag string) []string {
	keys := []string{looseKey(tag)}
	if n := defaultMergeNames[tag]; n != "" {
		keys = append(keys, looseKey(n))
	}
	return keys
}

// Match says what tag (as written between *| and |*) stands for. In order:
//
//  1. The import's own merge field with that tag, or, where the import
//     doesn't know a merge field's tag, whose name matches it loosely; it
//     becomes the contact field the import puts that merge field into.
//  2. A contact field an earlier import put that tag's merge field into.
//  3. A contact field whose key, then whose name, matches it loosely.
//
// A merge field the import leaves out still names the tag, so its note can
// say so, and the tag can still match by 2 or 3.
func (ft *FieldTags) Match(tag string) TagMatch {
	var m TagMatch
	tag = strings.ToUpper(strings.TrimSpace(tag))
	if ft == nil || !FieldTag(tag) {
		return m
	}
	if mf := ft.mergeField(tag); mf != nil {
		m.MergeField = mf.Name
		if mf.Field != nil {
			m.Field = mf.Field
			return m
		}
	}
	for _, f := range ft.Fields {
		if slices.Contains(f.Tags, tag) {
			m.Field = f
			return m
		}
	}
	keys := tagKeys(tag)
	for _, by := range []func(*TagField) string{func(f *TagField) string { return f.Key }, func(f *TagField) string { return f.Name }} {
		for _, f := range ft.Fields {
			if slices.Contains(keys, looseKey(by(f))) {
				m.Field = f
				return m
			}
		}
	}
	return m
}

func (ft *FieldTags) mergeField(tag string) *TagMergeField {
	for i := range ft.MergeFields {
		if slices.Contains(ft.MergeFields[i].Tags, tag) {
			return &ft.MergeFields[i]
		}
	}
	keys := tagKeys(tag)
	for i := range ft.MergeFields {
		if len(ft.MergeFields[i].Tags) == 0 && slices.Contains(keys, looseKey(ft.MergeFields[i].Name)) {
			return &ft.MergeFields[i]
		}
	}
	return nil
}

// MergeTag is one Mailchimp merge tag an email used, other than those
// mailgoose always converts (names, email, unsubscribe, subject, preview
// text and the postal address) and conditionals, with what it became.
type MergeTag struct {
	// Tag is as Mailchimp writes it, *|COMPANY|*.
	Tag string `json:"tag"`
	// MergeField names the import's merge field it is; "" when none.
	MergeField string `json:"merge_field,omitempty"`
	// FieldKey and FieldName are the contact field it became, as
	// {{FieldKey}}; "" when it was taken out. NewField is true for a field
	// the import makes.
	FieldKey  string `json:"field_key,omitempty"`
	FieldName string `json:"field_name,omitempty"`
	NewField  bool   `json:"new_field,omitempty"`
}

// Same reports whether m says the same as the match t, for a tag.
func (m MergeTag) Same(t TagMatch) bool {
	if m.MergeField != t.MergeField || (t.Field == nil) != (m.FieldKey == "") {
		return false
	}
	return t.Field == nil || (t.Field.Key == m.FieldKey && t.Field.Name == m.FieldName && t.Field.New == m.NewField)
}

func mergeTagOf(tag string, t TagMatch) MergeTag {
	m := MergeTag{Tag: "*|" + tag + "|*", MergeField: t.MergeField}
	if t.Field != nil {
		m.FieldKey, m.FieldName, m.NewField = t.Field.Key, t.Field.Name, t.Field.New
	}
	return m
}

// TagName is the bare tag of a MergeTag's Tag: COMPANY for *|COMPANY|*.
func TagName(written string) string {
	return strings.ToUpper(strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(written, "*|"), "|*")))
}

var notKeyChars = regexp.MustCompile(`[^a-z0-9]+`)

// suggestedField is the name of a contact field to add for an unmatched
// tag, and the key it would have: Mailchimp's default name for it, else
// the tag in words (FAV_BREAD is "Fav bread"). The key is worked out as
// contacts.FieldKey does. Both are "" when the tag has no letter or digit
// to make a key from.
func suggestedField(tag string) (name, key string) {
	name = defaultMergeNames[tag]
	if name == "" {
		words := strings.Fields(strings.NewReplacer("_", " ", "-", " ", ".", " ").Replace(strings.ToLower(tag)))
		if len(words) == 0 {
			return "", ""
		}
		name = strings.Join(words, " ")
		name = strings.ToUpper(name[:1]) + name[1:]
	}
	key = strings.Trim(notKeyChars.ReplaceAllString(strings.ToLower(name), "_"), "_")
	if key == "" {
		return "", ""
	}
	if key[0] < 'a' || key[0] > 'z' {
		key = "f_" + key
	}
	return name, key
}

// removedTagNote is the warning for a tag taken out, as written in written
// (*|COMPANY|*), in where ("" for the HTML, else "the subject" or "the
// preview text"). link is true when it was a link's whole address.
func removedTagNote(written, tag, where string, t TagMatch, link bool) Note {
	in := ""
	if where != "" {
		in = " in " + where
	}
	var msg string
	switch {
	case !FieldTag(tag):
		msg = fmt.Sprintf("Mailchimp's %s merge tag%s has no match in mailgoose, so it was taken out", written, in)
	case t.MergeField != "":
		msg = fmt.Sprintf("Mailchimp's %s merge tag%s is the merge field %q, which this import leaves out of your contact fields, "+
			"so it was taken out; to keep it, bring that merge field into a contact field and use that field's tag", written, in, t.MergeField)
	default:
		msg = fmt.Sprintf("Mailchimp's %s merge tag%s matches none of your contact fields, so it was taken out", written, in)
		if name, key := suggestedField(tag); name != "" {
			msg += fmt.Sprintf("; to fill it in from your contacts, add a contact field named %q and use its tag, {{%s}}", name, key)
		}
	}
	if link {
		msg += "; a link was left empty, so give it an address or remove it"
	}
	return Note{Code: NoteRemovedTag, Message: msg}
}

// linkTagNote is the warning for a tag that became a field's tag but was
// also a link's whole address, which a merge tag can't be.
func linkTagNote(written string, f *TagField) Note {
	return Note{Code: NoteRemovedTag, Message: fmt.Sprintf(
		"Mailchimp's %s merge tag became {{%s}}, except where it was a link's whole address: a merge tag can't decide where a link goes, "+
			"so that link was left empty; give it an address or remove it", written, f.Key)}
}
