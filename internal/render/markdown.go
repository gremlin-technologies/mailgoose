package render

import (
	"html"
	"net/url"
	"regexp"
	"strings"
)

// A text block holds a small Markdown subset (decided with MG-19): **bold**,
// *italic*, [text](https://...) links, "- " and "1. " lists, and blank lines
// between paragraphs. A single line break stays a line break. Anything else
// is literal text.

type mdKind int

const (
	mdParagraph mdKind = iota
	mdBullets
	mdNumbers
)

// mdBlock is a paragraph (its lines) or a list (its items).
type mdBlock struct {
	kind  mdKind
	lines []string
}

var numbered = regexp.MustCompile(`^\d{1,3}\. `)

func parseBlocks(src string) []mdBlock {
	src = strings.ReplaceAll(strings.ReplaceAll(src, "\r\n", "\n"), "\r", "\n")
	var out []mdBlock
	var cur *mdBlock
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		kind, item := mdParagraph, trimmed
		switch {
		case trimmed == "":
			cur = nil
			continue
		case strings.HasPrefix(trimmed, "- "):
			kind, item = mdBullets, strings.TrimSpace(trimmed[2:])
		case numbered.MatchString(trimmed):
			kind, item = mdNumbers, strings.TrimSpace(trimmed[strings.Index(trimmed, " ")+1:])
		}
		if cur == nil || cur.kind != kind {
			out = append(out, mdBlock{kind: kind})
			cur = &out[len(out)-1]
		}
		cur.lines = append(cur.lines, item)
	}
	return out
}

// inline is one piece of a line: text, or bold, italic or a link around
// more pieces.
type inline struct {
	text     string
	kind     string // "", "b", "i" or "a"
	href     string
	children []inline
}

// parseInline reads **bold**, *italic* and [links](url). A marker without
// its closing half is literal text.
func parseInline(s string) []inline {
	var out []inline
	text := strings.Builder{}
	flush := func() {
		if text.Len() > 0 {
			out = append(out, inline{text: text.String()})
			text.Reset()
		}
	}
	for i := 0; i < len(s); {
		switch {
		case strings.HasPrefix(s[i:], "**"):
			if end := strings.Index(s[i+2:], "**"); end > 0 {
				flush()
				out = append(out, inline{kind: "b", children: parseInline(s[i+2 : i+2+end])})
				i += end + 4
				continue
			}
		case s[i] == '*':
			if end := closingStar(s[i+1:]); end > 0 {
				flush()
				out = append(out, inline{kind: "i", children: parseInline(s[i+1 : i+1+end])})
				i += end + 2
				continue
			}
		case s[i] == '[':
			if mid := strings.Index(s[i:], "]("); mid > 1 {
				if end := strings.IndexByte(s[i+mid+2:], ')'); end > 0 {
					label := s[i+1 : i+mid]
					href := strings.TrimSpace(s[i+mid+2 : i+mid+2+end])
					if okLink(href) {
						flush()
						out = append(out, inline{kind: "a", href: href, children: parseInline(label)})
						i += mid + 2 + end + 1
						continue
					}
				}
			}
		}
		text.WriteByte(s[i])
		i++
	}
	flush()
	return out
}

// closingStar finds the * that closes an italic run, skipping **.
func closingStar(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] != '*' {
			continue
		}
		if i+1 < len(s) && s[i+1] == '*' {
			i++
			continue
		}
		return i
	}
	return -1
}

// okLink is a link a text block may hold: http, https, mailto, or a whole
// merge tag such as {{unsubscribe_url}}.
func okLink(href string) bool {
	if strings.HasPrefix(href, "{{") && strings.HasSuffix(href, "}}") && strings.Count(href, "{{") == 1 {
		return true
	}
	u, err := url.Parse(href)
	if err != nil {
		return false
	}
	switch u.Scheme {
	case "http", "https":
		return u.Host != ""
	case "mailto":
		return u.Opaque != ""
	}
	return false
}

// badLinks returns the [text](target) links in s whose target isn't one a
// text block may hold, so Lint can report them; they render as plain text.
var mdLink = regexp.MustCompile(`\[[^\]]+\]\(([^)]*)\)`)

func badLinks(s string) []string {
	var out []string
	for _, m := range mdLink.FindAllStringSubmatch(s, -1) {
		if !okLink(strings.TrimSpace(m[1])) {
			out = append(out, m[0])
		}
	}
	return out
}

func inlineHTML(b *strings.Builder, nodes []inline, link string) {
	for _, n := range nodes {
		switch n.kind {
		case "":
			b.WriteString(html.EscapeString(n.text))
		case "b":
			b.WriteString("<strong>")
			inlineHTML(b, n.children, link)
			b.WriteString("</strong>")
		case "i":
			b.WriteString("<em>")
			inlineHTML(b, n.children, link)
			b.WriteString("</em>")
		case "a":
			b.WriteString(`<a href="` + html.EscapeString(n.href) + `" style="` + link + `">`)
			inlineHTML(b, n.children, link)
			b.WriteString("</a>")
		}
	}
}

func inlineText(b *strings.Builder, nodes []inline) {
	for _, n := range nodes {
		switch n.kind {
		case "a":
			label := strings.Builder{}
			inlineText(&label, n.children)
			b.WriteString(label.String())
			if label.String() != n.href {
				b.WriteString(" (" + n.href + ")")
			}
		case "":
			b.WriteString(n.text)
		default:
			inlineText(b, n.children)
		}
	}
}
