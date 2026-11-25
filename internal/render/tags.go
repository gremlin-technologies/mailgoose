package render

import (
	"fmt"
	"html"
	"regexp"
	"slices"
	"strings"

	"github.com/gremlin-technologies/mailgoose/internal/emails"
)

// Merge tags (DES-7) are replaced per recipient: {{first_name}},
// {{last_name}} and {{email}}, each with an optional fallback for an empty
// value ({{first_name|there}}), and {{unsubscribe_url}}. Each of the
// account's contact fields (MG-130) is a merge tag too, by its key
// ({{company|your team}}, MG-133); the built-in tags come first, and no
// field can have one of their keys.
const (
	TagFirstName      = "first_name"
	TagLastName       = "last_name"
	TagEmail          = "email"
	TagUnsubscribeURL = "unsubscribe_url"
)

// Tags lists the merge tags, in the order the docs give them.
var Tags = []string{TagFirstName, TagLastName, TagEmail, TagUnsubscribeURL}

// MaxFallbackChars is the longest fallback a tag can carry.
const MaxFallbackChars = 100

// Recipient is who one copy of an email goes to. Empty names take the tag's
// fallback.
type Recipient struct {
	Email          string
	FirstName      string
	LastName       string
	UnsubscribeURL string
	// Fields are the recipient's contact field values by key (MG-133). A
	// field with no value, or one that's gone, takes the tag's fallback.
	Fields map[string]string
	// Branded adds the "Sent by mailgoose" footer to this copy (MG-119): a
	// free account's, or a tester's. It's decided for each copy.
	Branded bool
}

func (r Recipient) value(tag string) string {
	switch tag {
	case TagFirstName:
		return r.FirstName
	case TagLastName:
		return r.LastName
	case TagEmail:
		return r.Email
	case TagUnsubscribeURL:
		return r.UnsubscribeURL
	}
	return r.Fields[tag]
}

// segment is literal text, or a merge tag when tag is set.
type segment struct {
	text     string
	tag      string
	fallback string
	// slot is where the "Sent by mailgoose" footer goes (MG-119): filled
	// for a branded copy, empty for any other.
	slot bool
}

// parts is text with its merge tags found, ready to fill in.
type parts []segment

// fill writes the parts with r's values. With escape, values are HTML
// escaped; literal text and fallbacks were escaped when the parts were made.
func (p parts) fill(b *strings.Builder, r Recipient, escape bool, brand string) {
	for _, s := range p {
		if s.slot {
			if r.Branded {
				b.WriteString(brand)
			}
			continue
		}
		if s.tag == "" {
			b.WriteString(s.text)
			continue
		}
		v := strings.TrimSpace(r.value(s.tag))
		switch {
		case v == "":
			b.WriteString(s.fallback)
		case escape:
			b.WriteString(html.EscapeString(v))
		default:
			b.WriteString(v)
		}
	}
}

func (p parts) size() int {
	n := 0
	for _, s := range p {
		n += len(s.text) + len(s.fallback) + 20
	}
	return n
}

// TagError is a merge tag the renderer can't fill in.
type TagError struct {
	// Path names where it is, as in emails.DocError: blocks[2].text, or
	// settings.subject.
	Path    string
	Tag     string
	Message string
}

func (e *TagError) Error() string { return fmt.Sprintf("%s: %s", e.Path, e.Message) }

// findTags splits s into literal text and merge tags, reporting each tag it
// can't use. Anything that doesn't open with "{{" is literal. fields are the
// account's contact field keys, each a merge tag too (MG-133).
func findTags(s, path string, fields []string) (parts, []*TagError) {
	var out parts
	var errs []*TagError
	for {
		i := strings.Index(s, "{{")
		if i < 0 {
			break
		}
		j := strings.Index(s[i:], "}}")
		if j < 0 {
			errs = append(errs, &TagError{Path: path, Tag: s[i:], Message: fmt.Sprintf("%q opens a merge tag but never closes it with }}", clip(s[i:]))})
			break
		}
		raw := s[i : i+j+2]
		inner := s[i+2 : i+j]
		name, fallback, hasFallback := strings.Cut(inner, "|")
		name = strings.TrimSpace(name)
		fallback = strings.TrimSpace(fallback)
		switch {
		case !known(name, fields):
			errs = append(errs, &TagError{Path: path, Tag: raw, Message: unknownTagMessage(raw, fields)})
		case hasFallback && name == TagUnsubscribeURL:
			errs = append(errs, &TagError{Path: path, Tag: raw, Message: "{{unsubscribe_url}} takes no fallback; every recipient has one"})
		case strings.ContainsAny(fallback, "{}"):
			errs = append(errs, &TagError{Path: path, Tag: raw, Message: fmt.Sprintf("the fallback in %s can't contain { or }", raw)})
		case len([]rune(fallback)) > MaxFallbackChars:
			errs = append(errs, &TagError{Path: path, Tag: raw, Message: fmt.Sprintf("the fallback in %s is over %d characters", clip(raw), MaxFallbackChars)})
		default:
			if i > 0 {
				out = append(out, segment{text: s[:i]})
			}
			out = append(out, segment{tag: name, fallback: fallback})
			s = s[i+j+2:]
			continue
		}
		// Keep the bad tag as text, so later tags are still checked.
		out = append(out, segment{text: s[:i+j+2]})
		s = s[i+j+2:]
	}
	if s != "" {
		out = append(out, segment{text: s})
	}
	return out, errs
}

func known(name string, fields []string) bool {
	return Builtin(name) || slices.Contains(fields, name)
}

// Builtin reports whether name is one of the built-in merge tags, rather
// than a contact field's key.
func Builtin(name string) bool { return slices.Contains(Tags, name) }

// unknownTagMessage says a tag isn't one, and lists the tags that are: the
// built-in ones and the account's contact field keys.
func unknownTagMessage(raw string, fields []string) string {
	if len(fields) == 0 {
		return fmt.Sprintf("%s isn't a merge tag; use {{first_name}}, {{last_name}}, {{email}} or {{unsubscribe_url}} "+
			"(this account has no contact fields to use as tags)", raw)
	}
	keys := make([]string, len(fields))
	for i, k := range fields {
		keys[i] = "{{" + k + "}}"
	}
	return fmt.Sprintf("%s isn't a merge tag; use {{first_name}}, {{last_name}}, {{email}}, {{unsubscribe_url}} "+
		"or one of this account's contact fields: %s", raw, strings.Join(keys, ", "))
}

// fieldKey matches what a contact field's key can be (contacts.FieldKey):
// a to z, 0 to 9 and _, starting with a letter.
var fieldKey = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// TagUse is one well-formed merge tag in an email: where it is, its name
// and whether it has a fallback. The name may be one no account has.
type TagUse struct {
	// Path names where it is, as TagError.Path does.
	Path        string
	Tag         string
	HasFallback bool
}

// UsedTags lists every well-formed merge tag in an email, of either kind,
// in the order they come, once for each tag at each path (twice when it's
// there both with a fallback and without). It's how a
// check finds a contact field's tag with no fallback, and how deleting a
// field finds the emails that use it.
func UsedTags(doc *emails.Document) []TagUse {
	var out []TagUse
	seen := map[TagUse]bool{}
	eachTagged(doc, func(path, s string) {
		for {
			i := strings.Index(s, "{{")
			if i < 0 {
				return
			}
			j := strings.Index(s[i:], "}}")
			if j < 0 {
				return
			}
			name, _, hasFallback := strings.Cut(s[i+2:i+j], "|")
			s = s[i+j+2:]
			if name = strings.TrimSpace(name); name == "" {
				continue
			}
			u := TagUse{Path: path, Tag: name, HasFallback: hasFallback}
			if !seen[u] {
				seen[u] = true
				out = append(out, u)
			}
		}
	})
	return out
}

// eachTagged calls fn with every text of an email that merge tags can be
// in, and where it is. For an html email that's its subject, preview text
// and markup.
func eachTagged(doc *emails.Document, fn func(path, s string)) {
	fn("settings.subject", doc.Settings.Subject)
	fn("settings.preview_text", doc.Settings.PreviewText)
	if doc.HTML != nil {
		fn("html", *doc.HTML)
		return
	}
	for _, info := range emails.ListBlocks(doc) {
		p := info.Path
		switch b := info.Block.(type) {
		case *emails.Heading:
			fn(p+".text", b.Text)
		case *emails.Text:
			fn(p+".text", b.Text)
		case *emails.Image:
			fn(p+".alt", b.Alt)
			if b.Href != nil {
				fn(p+".href", *b.Href)
			}
		case *emails.Button:
			fn(p+".text", b.Text)
			fn(p+".href", b.Href)
		case *emails.Social:
			for i, l := range b.Links {
				fn(fmt.Sprintf("%s.links[%d].label", p, i), l.Label)
			}
		case *emails.Footer:
			fn(p+".text", b.Text)
		}
	}
}

func clip(s string) string {
	if r := []rune(s); len(r) > 40 {
		return string(r[:40]) + "…"
	}
	return s
}
