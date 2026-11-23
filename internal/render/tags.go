package render

import (
	"fmt"
	"html"
	"strings"
)

// Merge tags (DES-7) are replaced per recipient: {{first_name}},
// {{last_name}} and {{email}}, each with an optional fallback for an empty
// value ({{first_name|there}}), and {{unsubscribe_url}}.
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
	return ""
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
// can't use. Anything that doesn't open with "{{" is literal.
func findTags(s, path string) (parts, []*TagError) {
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
		case !known(name):
			errs = append(errs, &TagError{Path: path, Tag: raw,
				Message: fmt.Sprintf("%s isn't a merge tag; use {{first_name}}, {{last_name}}, {{email}} or {{unsubscribe_url}}", raw)})
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

func known(name string) bool {
	for _, t := range Tags {
		if name == t {
			return true
		}
	}
	return false
}

func clip(s string) string {
	if r := []rune(s); len(r) > 40 {
		return string(r[:40]) + "…"
	}
	return s
}
