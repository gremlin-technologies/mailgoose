package mailchimp

import (
	"fmt"
	"html"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// Converting Mailchimp email HTML (MG-71). Templates and past campaigns come
// over as HTML emails, edited as code. ConvertHTML rewrites what mailgoose
// can take and lists the rest; the email checks still run on the result.

// Codes for the notes ConvertHTML makes.
const (
	// NoteRemovedTag: a Mailchimp merge tag mailgoose has no match for was
	// taken out, or one that became a contact field's tag was taken out of
	// a link (a warning).
	NoteRemovedTag = "mailchimp_tag_removed"
	// NoteConditional: a *|IF:...|* block was left as written (an error to
	// fix: mailgoose has no conditionals).
	NoteConditional = "mailchimp_conditional"
	// NoteTrackingLink: a Mailchimp tracking link (list-manage.com) was
	// kept, since its target can't be read without following it (a
	// warning).
	NoteTrackingLink = "mailchimp_tracking_link"
	// NoteImageNotCopied: a Mailchimp-hosted image couldn't be copied and
	// keeps its Mailchimp link (a warning).
	NoteImageNotCopied = "image_not_copied"
)

// Note is one thing a conversion changed or couldn't.
type Note struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// Line is the 1-based line of the converted HTML, where it applies.
	Line int `json:"line,omitempty"`
}

// Conversion is Mailchimp HTML rewritten for mailgoose.
type Conversion struct {
	HTML        string
	Subject     string
	PreviewText string
	Warnings    []Note
	Errors      []Note
	// Images are the Mailchimp-hosted images the HTML uses, as written in
	// it, without repeats and in the order they first appear.
	Images []string
	// MergeTags are the merge tags it used other than the built-in ones
	// (MG-134), each once, in the order first seen (subject, preview text,
	// then the HTML), with the contact field each became or none.
	MergeTags []MergeTag
}

// mergeTag matches a Mailchimp merge tag, *|NAME|*.
var mergeTag = regexp.MustCompile(`\*\|([^|*\r\n]{1,200})\|\*`)

// Tags and their mailgoose merge tags.
var mailgooseTags = map[string]string{
	"FNAME": "{{first_name}}",
	"LNAME": "{{last_name}}",
	"EMAIL": "{{email}}",
	"UNSUB": "{{unsubscribe_url}}",
}

// addressTags are removed without a note: mailgoose adds the account's
// postal address to every email.
var addressTags = map[string]bool{"LIST:ADDRESS": true, "LIST:ADDRESSLINE": true, "HTML:LIST_ADDRESS_HTML": true}

// conditional reports whether a tag opens, continues or ends a Mailchimp
// conditional block.
func conditional(tag string) bool {
	for _, p := range []string{"IF:", "ELSEIF:", "ELSE:", "END:IF", "IFNOT:", "INTERESTED:", "END:INTERESTED"} {
		if strings.HasPrefix(tag, p) {
			return true
		}
	}
	return tag == "ELSE" || tag == "END"
}

// mcAttr matches the Mailchimp editor's attributes inside a start tag.
var (
	startTag = regexp.MustCompile(`<[a-zA-Z](?:[^>"']|"[^"]*"|'[^']*')*>`)
	mcAttr   = regexp.MustCompile(`(?i)\s+mc:(?:edit|repeatable|hideable)(?:\s*=\s*(?:"[^"]*"|'[^']*'|[^\s"'>]+))?`)
	// imageRef finds image addresses in src and background attributes and
	// in CSS url(...).
	imageRef = regexp.MustCompile(`(?i)(?:\b(?:src|background)\s*=\s*["']?|url\(\s*["']?)(https?://[^\s"'()<>]+)`)
	// hrefTag finds links whose whole address is one merge tag.
	hrefTag = regexp.MustCompile(`(?i)\bhref\s*=\s*(?:"\s*\*\|([^|*\r\n"]{1,200})\|\*\s*"|'\s*\*\|([^|*\r\n']{1,200})\|\*\s*'|\*\|([^|*\r\n\s>]{1,200})\|\*)`)
	hrefRef = regexp.MustCompile(`(?i)\bhref\s*=\s*["']?(https?://[^\s"'<>]+)`)
)

// ImageHost reports whether host serves Mailchimp-hosted images
// that the import copies.
func ImageHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return host == "mcusercontent.com" || strings.HasSuffix(host, ".mcusercontent.com") || host == "gallery.mailchimp.com"
}

// trackingHost reports whether host is Mailchimp's click-tracking host.
func trackingHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return host == "list-manage.com" || strings.HasSuffix(host, ".list-manage.com")
}

func hostOf(raw string) string {
	u, err := url.Parse(html.UnescapeString(raw))
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// ConvertHTML rewrites src, a Mailchimp template or campaign, for mailgoose.
// subject and preview are the email's subject and preview text where known;
// their own merge tags are converted too. ft says which contact field a
// merge tag stands for (MG-134); nil matches none, so every tag but the
// built-in ones is taken out.
func ConvertHTML(src, subject, preview string, ft *FieldTags) *Conversion {
	c := &Conversion{}
	tags := &tagUses{ft: ft, c: c, notes: map[string]bool{}, at: map[string]int{}}
	c.Subject = convertText(subject, "", tags, "the subject")
	c.PreviewText = convertText(preview, c.Subject, tags, "the preview text")

	// Tags that are a link's whole address leave it empty when taken out.
	// A merge tag can't decide where a link goes (MG-19), so a tag that
	// becomes a field's tag is taken out of such a link too.
	linkTags := map[string]bool{}
	src = hrefTag.ReplaceAllStringFunc(src, func(m string) string {
		s := hrefTag.FindStringSubmatch(m)
		tag := strings.ToUpper(strings.TrimSpace(s[1] + s[2] + s[3]))
		linkTags[tag] = true
		if mailgooseTags[tag] == "" && tags.match(tag).Field != nil {
			tags.linkEmptied(tag)
			return mergeTag.ReplaceAllString(m, "")
		}
		return m
	})

	conditionals := 0
	firstConditional := 0
	out := mergeTag.ReplaceAllStringFunc(src, func(m string) string {
		tag := strings.ToUpper(strings.TrimSpace(m[2 : len(m)-2]))
		switch {
		case mailgooseTags[tag] != "":
			return mailgooseTags[tag]
		case tag == "MC:SUBJECT":
			return html.EscapeString(c.Subject)
		case tag == "MC_PREVIEW_TEXT":
			return html.EscapeString(c.PreviewText)
		case addressTags[tag]:
			return ""
		case conditional(tag):
			conditionals++
			return m
		}
		return tags.use(tag, "", linkTags[tag])
	})
	tags.finish()
	if conditionals > 0 {
		firstConditional = lineOf(out, conditionalIndex(out))
		c.Errors = append(c.Errors, Note{Code: NoteConditional, Line: firstConditional,
			Message: "Mailchimp's *|IF:...|* conditional blocks were left as written, since mailgoose has none; " +
				"keep the part every reader should see and delete the *|...|* tags before sending"})
	}

	out = startTag.ReplaceAllStringFunc(out, func(t string) string { return mcAttr.ReplaceAllString(t, "") })

	seen := map[string]bool{}
	for _, m := range imageRef.FindAllStringSubmatch(out, -1) {
		if !seen[m[1]] && ImageHost(hostOf(m[1])) {
			c.Images = append(c.Images, m[1])
		}
		seen[m[1]] = true
	}
	tracking := 0
	for _, m := range hrefRef.FindAllStringSubmatch(out, -1) {
		if trackingHost(hostOf(m[1])) {
			tracking++
		}
	}
	if tracking > 0 {
		links := "link goes"
		if tracking > 1 {
			links = "links go"
		}
		c.Warnings = append(c.Warnings, Note{Code: NoteTrackingLink, Message: fmt.Sprintf(
			"%d %s through Mailchimp's tracking (list-manage.com), which may stop working once you leave Mailchimp; "+
				"replace each with the page it leads to", tracking, links)})
	}
	c.HTML = out
	return c
}

// tagUses converts the merge tags of one email that aren't built in, and
// lists them, each once, in the order first seen, with one note for each
// tag taken out.
type tagUses struct {
	ft *FieldTags
	c  *Conversion
	// at is each tag's place in c.MergeTags; notes the tags already noted.
	at    map[string]int
	notes map[string]bool
	// emptied are the converted tags taken out of a link's whole address,
	// noted once the HTML is done.
	emptied []string
}

func (u *tagUses) match(tag string) TagMatch { return u.ft.Match(tag) }

// use converts one use of tag in where ("" for the HTML), returning what
// takes its place.
func (u *tagUses) use(tag, where string, link bool) string {
	t := u.match(tag)
	written := "*|" + tag + "|*"
	if _, ok := u.at[tag]; !ok {
		u.at[tag] = len(u.c.MergeTags)
		u.c.MergeTags = append(u.c.MergeTags, mergeTagOf(tag, t))
	}
	if t.Field != nil {
		return "{{" + t.Field.Key + "}}"
	}
	if !u.notes[tag] {
		u.notes[tag] = true
		u.c.Warnings = append(u.c.Warnings, removedTagNote(written, tag, where, t, link))
	}
	return ""
}

func (u *tagUses) linkEmptied(tag string) {
	if !slices.Contains(u.emptied, tag) {
		u.emptied = append(u.emptied, tag)
	}
}

// finish notes the converted tags that were taken out of links.
func (u *tagUses) finish() {
	for _, tag := range u.emptied {
		t := u.match(tag)
		if _, ok := u.at[tag]; !ok {
			u.at[tag] = len(u.c.MergeTags)
			u.c.MergeTags = append(u.c.MergeTags, mergeTagOf(tag, t))
		}
		u.c.Warnings = append(u.c.Warnings, linkTagNote("*|"+tag+"|*", t.Field))
	}
}

// convertText converts the merge tags in a subject or preview text: names
// become mailgoose tags, *|MC:SUBJECT|* the subject, a contact field's
// merge tag that field's tag, and anything else is taken out with a
// warning (conditionals included, since a subject can't show an error
// line).
func convertText(s, subject string, tags *tagUses, where string) string {
	if s == "" {
		return ""
	}
	out := mergeTag.ReplaceAllStringFunc(s, func(m string) string {
		tag := strings.ToUpper(strings.TrimSpace(m[2 : len(m)-2]))
		switch tag {
		case "FNAME", "LNAME", "EMAIL":
			return mailgooseTags[tag]
		case "MC:SUBJECT":
			return subject
		}
		return tags.use(tag, where, false)
	})
	return strings.Join(strings.Fields(out), " ")
}

func conditionalIndex(s string) int {
	for _, loc := range mergeTag.FindAllStringSubmatchIndex(s, -1) {
		if conditional(strings.ToUpper(strings.TrimSpace(s[loc[2]:loc[3]]))) {
			return loc[0]
		}
	}
	return 0
}

func lineOf(s string, offset int) int {
	return strings.Count(s[:offset], "\n") + 1
}
