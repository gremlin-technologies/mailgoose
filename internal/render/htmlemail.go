package render

import (
	"errors"
	"html"
	"regexp"
	"strings"

	nethtml "golang.org/x/net/html"

	"github.com/gremlin-technologies/mailgoose/internal/emails"
)

// ErrNotHTML means CompileHTML was given a blocks email; Compile renders
// those.
var ErrNotHTML = errors.New("only an html email can be compiled as html")

var (
	bodyOpen  = regexp.MustCompile(`(?i)<body\b[^>]*>`)
	bodyClose = regexp.MustCompile(`(?i)</body\s*>`)
)

// CompileHTML prepares an html email for sending, as Compile does for a
// blocks email. The markup is the customer's, as emails.CheckHTML passed
// it, with two additions: the preview text as a hidden line at the top of
// the body, and the account's postal address at the bottom when the email
// doesn't already show it (DES-11, CMP-2). The plain-text part is made
// from the markup.
func CompileHTML(doc *emails.Document, acct Account) (*Template, error) {
	t, problems, err := compileHTML(doc, acct)
	if err != nil {
		return nil, err
	}
	// The same check the html had to pass to be saved, so an email whose
	// markup was never imported (empty) or has no unsubscribe link can't
	// be sent. It comes back as an *emails.HTMLError, apart from merge tag
	// Problems. Draft skips it: the email check reports these itself.
	if errs, _ := emails.CheckHTML(*doc.HTML); len(errs) > 0 {
		return nil, &emails.HTMLError{Issues: errs}
	}
	if len(problems) > 0 {
		return nil, problems
	}
	return t, nil
}

func compileHTML(doc *emails.Document, acct Account) (*Template, Problems, error) {
	if doc.HTML == nil {
		return nil, nil, ErrNotHTML
	}
	if strings.TrimSpace(acct.PostalAddress) == "" {
		return nil, nil, ErrNoPostalAddress
	}
	src := cleanSlot(*doc.HTML)
	if !showsAddress(src, acct.PostalAddress) {
		src = addFooter(src, acct.PostalAddress)
	}
	// The text part has no preheader: it's hidden in the html.
	text := htmlText(src) + brandSlot
	src = addBrandSlot(src)
	if p := doc.Settings.PreviewText; p != "" {
		src = addPreheader(src, p)
	}

	var problems Problems
	subject, e := findTags(doc.Settings.Subject, "settings.subject")
	problems = append(problems, e...)
	htmlParts, e := findTags(src, "html")
	problems = append(problems, e...)
	textParts, _ := findTags(text, "html")
	return &Template{subject: subject, html: splitSlot(htmlParts), text: splitSlot(textParts),
		brandHTML: brandHTMLFragment(acct.AssetsURL), brandText: brandText(acct.AssetsURL)}, problems, nil
}

// addBrandSlot puts the place for the "Sent by mailgoose" footer (MG-119)
// just before </body>, after the postal address, or at the end of a
// fragment.
func addBrandSlot(src string) string {
	locs := bodyClose.FindAllStringIndex(src, -1)
	if len(locs) == 0 {
		return src + brandSlot
	}
	at := locs[len(locs)-1][0]
	return src[:at] + brandSlot + src[at:]
}

// showsAddress reports whether every line of the postal address appears in
// the email's visible text, ignoring case and spacing.
func showsAddress(src, address string) bool {
	text := normalize(visibleText(src))
	for _, line := range strings.Split(address, "\n") {
		if l := normalize(line); l != "" && !strings.Contains(text, l) {
			return false
		}
	}
	return true
}

func normalize(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// addFooter puts the postal address just before </body>, or at the end of
// a fragment.
func addFooter(src, address string) string {
	var lines []string
	for _, l := range strings.Split(address, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, html.EscapeString(l))
		}
	}
	footer := "\n" + `<div style="padding:24px 16px;font-family:` + fontStack + `;font-size:12px;line-height:1.6;color:` +
		mutedColor + `;text-align:center;">` + strings.Join(lines, "<br>") + "</div>\n"
	locs := bodyClose.FindAllStringIndex(src, -1)
	if len(locs) == 0 {
		return src + footer
	}
	at := locs[len(locs)-1][0]
	return src[:at] + footer + src[at:]
}

// addPreheader puts the preview text, hidden, right after <body>.
func addPreheader(src, preview string) string {
	div := `<div style="display:none;max-height:0;max-width:0;overflow:hidden;mso-hide:all;font-size:1px;line-height:1px;opacity:0;">` +
		html.EscapeString(preview) + strings.Repeat("&#847;&zwnj;&nbsp;", 30) + "</div>\n"
	if loc := bodyOpen.FindStringIndex(src); loc != nil {
		return src[:loc[1]] + "\n" + div + src[loc[1]:]
	}
	return div + src
}

// Elements whose text never shows, and elements that start a new line.
var (
	hiddenElements = map[string]bool{"head": true, "title": true, "style": true, "script": true, "template": true}
	blockElements  = map[string]bool{
		"p": true, "div": true, "table": true, "tr": true, "ul": true, "ol": true, "li": true, "br": true,
		"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true, "hr": true, "blockquote": true,
		"center": true, "section": true, "article": true, "header": true, "footer": true, "pre": true,
	}
)

// visibleText is the text a reader sees, without links or image text.
func visibleText(src string) string {
	return walkText(src, false)
}

// htmlText is the plain-text part for an html email: the visible text with
// a line per block, "- " before list items, link addresses after their
// text, and image alt text in brackets.
func htmlText(src string) string {
	return walkText(src, true)
}

func walkText(src string, rich bool) string {
	z := nethtml.NewTokenizer(strings.NewReader(src))
	var b strings.Builder
	hidden := 0
	var hrefs []string
	var linkStart []int
	newline := func() { b.WriteString("\n") }
	for {
		tt := z.Next()
		switch tt {
		case nethtml.ErrorToken:
			return tidyText(b.String())
		case nethtml.TextToken:
			if hidden > 0 {
				continue
			}
			// Text comes unescaped; keep one space where the markup had any.
			t := string(z.Text())
			words := strings.Join(strings.Fields(t), " ")
			if t != "" && isSpace(t[0]) {
				b.WriteString(" ")
			}
			b.WriteString(words)
			if words != "" && isSpace(t[len(t)-1]) {
				b.WriteString(" ")
			}
		case nethtml.StartTagToken, nethtml.SelfClosingTagToken:
			tok := z.Token()
			if hiddenElements[tok.Data] {
				if tt == nethtml.StartTagToken {
					hidden++
				}
				continue
			}
			if hidden > 0 {
				continue
			}
			if blockElements[tok.Data] {
				newline()
			}
			if !rich {
				continue
			}
			switch tok.Data {
			case "li":
				b.WriteString("- ")
			case "hr":
				b.WriteString("----------\n")
			case "img":
				if alt := strings.TrimSpace(attr(tok, "alt")); alt != "" {
					b.WriteString("[" + alt + "]")
				}
			case "a":
				if tt == nethtml.StartTagToken {
					hrefs = append(hrefs, strings.TrimSpace(attr(tok, "href")))
					linkStart = append(linkStart, b.Len())
				}
			}
		case nethtml.EndTagToken:
			name, _ := z.TagName()
			tag := string(name)
			if hiddenElements[tag] {
				if hidden > 0 {
					hidden--
				}
				continue
			}
			if hidden > 0 {
				continue
			}
			if rich && tag == "a" && len(hrefs) > 0 {
				href := hrefs[len(hrefs)-1]
				label := strings.TrimSpace(b.String()[linkStart[len(linkStart)-1]:])
				hrefs, linkStart = hrefs[:len(hrefs)-1], linkStart[:len(linkStart)-1]
				if showHref(href) && href != label {
					b.WriteString(" (" + href + ")")
				}
			}
			if blockElements[tag] {
				newline()
			}
		}
	}
}

// showHref reports whether a link's address belongs in the text part.
func showHref(href string) bool {
	h := strings.ToLower(href)
	return strings.HasPrefix(h, "http://") || strings.HasPrefix(h, "https://") ||
		strings.HasPrefix(h, "mailto:") || strings.HasPrefix(h, "{{")
}

func isSpace(c byte) bool { return c == ' ' || c == '\n' || c == '\t' || c == '\r' || c == '\f' }

func attr(tok nethtml.Token, key string) string {
	for _, a := range tok.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// tidyText trims each line, keeps at most one blank line in a row, and
// ends with a single newline.
func tidyText(s string) string {
	var out []string
	blank := true
	for _, line := range strings.Split(s, "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			if !blank {
				out = append(out, "")
			}
			blank = true
			continue
		}
		out = append(out, line)
		blank = false
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n") + "\n"
}
