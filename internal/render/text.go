package render

import (
	"strconv"
	"strings"

	"github.com/gremlin-technologies/mailgoose/internal/emails"
)

// text renders the plain-text part: each block as readable lines, blocks
// separated by a blank line, links written out after their text.
func (r *renderer) text() string {
	var out []string
	for _, b := range r.doc.Blocks {
		out = append(out, textOf(b, r.acct)...)
	}
	return strings.Join(out, "\n\n") + "\n"
}

func textOf(b emails.Block, acct Account) []string {
	switch b := b.(type) {
	case *emails.Heading:
		return []string{b.Text}
	case *emails.Text:
		return []string{markdownText(b.Text)}
	case *emails.Image:
		label := b.Alt
		if b.Href != nil {
			if label == "" {
				return []string{*b.Href}
			}
			return []string{label + " (" + *b.Href + ")"}
		}
		if label == "" {
			return nil
		}
		return []string{"[" + label + "]"}
	case *emails.Button:
		return []string{b.Text + ": " + b.Href}
	case *emails.Divider:
		return []string{"----------"}
	case *emails.Spacer:
		return nil
	case *emails.Social:
		lines := make([]string, len(b.Links))
		for i, l := range b.Links {
			lines[i] = networkNames[l.Network] + ": " + l.URL
		}
		return []string{strings.Join(lines, "\n")}
	case *emails.Columns:
		var out []string
		for _, col := range b.Columns {
			for _, inner := range col.Blocks {
				out = append(out, textOf(inner, acct)...)
			}
		}
		return out
	case *emails.Footer:
		var out []string
		out = append(out, "----------")
		if strings.TrimSpace(b.Text) != "" {
			out = append(out, markdownText(b.Text))
		}
		out = append(out, strings.Join(postalLines(acct.PostalAddress), "\n"), "Unsubscribe: {{unsubscribe_url}}")
		return []string{strings.Join(out, "\n\n")}
	}
	return nil
}

func markdownText(src string) string {
	var paras []string
	for _, mb := range parseBlocks(src) {
		lines := make([]string, len(mb.lines))
		for i, line := range mb.lines {
			var b strings.Builder
			switch mb.kind {
			case mdBullets:
				b.WriteString("- ")
			case mdNumbers:
				b.WriteString(strconv.Itoa(i+1) + ". ")
			}
			inlineText(&b, parseInline(line))
			lines[i] = b.String()
		}
		paras = append(paras, strings.Join(lines, "\n"))
	}
	return strings.Join(paras, "\n\n")
}
