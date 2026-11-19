// Package render turns an email's block document into the HTML and plain
// text that's sent (DES-7, DES-8, CMP-2). A Template is made once per
// campaign by Compile and filled in per recipient by Personalize, so the
// per-recipient work is only replacing merge tags.
//
// The HTML is table-based with every style inline, which holds up in Gmail,
// Apple Mail, Outlook and Yahoo. A <style> block adds the narrow-screen rules
// (columns stack, full-width images shrink) for the clients that read it;
// clients that don't still get a readable fixed-width email.
package render

import (
	"errors"
	"fmt"
	"html"
	"strconv"
	"strings"

	"github.com/gremlin-technologies/mailgoose/internal/emails"
)

var (
	// ErrNotBlocks means the email is html; CompileHTML handles those.
	ErrNotBlocks = errors.New("only a blocks email can be rendered")
	// ErrNoPostalAddress means the account has no postal address, which
	// every footer must carry (CMP-2).
	ErrNoPostalAddress = errors.New("the account has no postal address")
)

// Account is what the renderer needs from the account's settings.
type Account struct {
	PostalAddress string
}

// Problems is every merge tag or link Lint found that can't be rendered.
type Problems []*TagError

func (p Problems) Error() string {
	msgs := make([]string, len(p))
	for i, e := range p {
		msgs[i] = e.Error()
	}
	return strings.Join(msgs, "; ")
}

// Template is a rendered email whose merge tags are still to fill in.
type Template struct {
	subject parts
	html    parts
	text    parts
}

// Message is one recipient's copy of an email.
type Message struct {
	Subject string
	HTML    string
	Text    string
	// Headers are the extra headers this copy needs: the List-Unsubscribe
	// pair when the recipient has an unsubscribe URL.
	Headers []Header
}

// Header is one email header.
type Header struct {
	Name, Value string
}

// UnsubscribeHeaders are the RFC 8058 one-click headers for an unsubscribe
// URL: a mail app shows its own unsubscribe button and POSTs
// "List-Unsubscribe=One-Click" to the URL, with no page to open. Gmail and
// Yahoo require them for bulk senders. An empty URL has none.
func UnsubscribeHeaders(url string) []Header {
	url = strings.TrimSpace(url)
	if url == "" || strings.ContainsAny(url, "<>\r\n") {
		return nil
	}
	return []Header{
		{Name: "List-Unsubscribe", Value: "<" + url + ">"},
		{Name: "List-Unsubscribe-Post", Value: "List-Unsubscribe=One-Click"},
	}
}

// Personalize fills in the merge tags for r.
func (t *Template) Personalize(r Recipient) Message {
	var subject, htmlOut, text strings.Builder
	t.subject.fill(&subject, r, false)
	htmlOut.Grow(t.html.size())
	t.html.fill(&htmlOut, r, true)
	text.Grow(t.text.size())
	t.text.fill(&text, r, false)
	return Message{
		Subject: strings.Join(strings.Fields(subject.String()), " "),
		HTML:    htmlOut.String(),
		Text:    text.String(),
		Headers: UnsubscribeHeaders(r.UnsubscribeURL),
	}
}

// Lint returns every merge tag in the document that can't be filled in,
// and every text-block link that isn't a full https:// address (or mailto:
// or a merge tag). Compile refuses a document Lint finds anything in.
func Lint(doc *emails.Document) []*TagError {
	var errs []*TagError
	check := func(path, s string) {
		_, e := findTags(s, path)
		errs = append(errs, e...)
	}
	check("settings.subject", doc.Settings.Subject)
	check("settings.preview_text", doc.Settings.PreviewText)
	for _, info := range emails.ListBlocks(doc) {
		p := info.Path
		switch b := info.Block.(type) {
		case *emails.Heading:
			check(p+".text", b.Text)
		case *emails.Text:
			check(p+".text", b.Text)
			for _, l := range badLinks(b.Text) {
				errs = append(errs, &TagError{Path: p + ".text", Message: fmt.Sprintf("the link %s needs a full https:// address", clip(l))})
			}
		case *emails.Image:
			check(p+".alt", b.Alt)
			if b.Href != nil {
				check(p+".href", *b.Href)
			}
		case *emails.Button:
			check(p+".text", b.Text)
			check(p+".href", b.Href)
		case *emails.Footer:
			check(p+".text", b.Text)
			for _, l := range badLinks(b.Text) {
				errs = append(errs, &TagError{Path: p + ".text", Message: fmt.Sprintf("the link %s needs a full https:// address", clip(l))})
			}
		}
	}
	return errs
}

// Compile renders a blocks document for an account.
func Compile(doc *emails.Document, acct Account) (*Template, error) {
	t, problems, err := compileBlocks(doc, acct)
	if err != nil {
		return nil, err
	}
	if len(problems) > 0 {
		return nil, problems
	}
	return t, nil
}

// Draft renders an email of either kind even when Lint (or, for html, the
// tag check) finds problems, and returns them with it; a bad tag stays in
// the output as text. The email check (MG-26) uses it to look at the rest
// of an email that has tag problems. Nothing is sent from a draft: sending
// goes through Compile or CompileHTML, which refuse problems.
func Draft(doc *emails.Document, acct Account) (*Template, Problems, error) {
	if doc.HTML != nil {
		return compileHTML(doc, acct)
	}
	return compileBlocks(doc, acct)
}

func compileBlocks(doc *emails.Document, acct Account) (*Template, Problems, error) {
	if doc.Kind() != emails.KindBlocks {
		return nil, nil, ErrNotBlocks
	}
	if strings.TrimSpace(acct.PostalAddress) == "" {
		return nil, nil, ErrNoPostalAddress
	}
	problems := Problems(Lint(doc))
	r := &renderer{doc: doc, acct: acct, width: doc.Settings.ContentWidth}
	if r.width <= 0 {
		r.width = emails.DefaultSettings().ContentWidth
	}
	subject, _ := findTags(doc.Settings.Subject, "")
	htmlParts, _ := findTags(r.html(), "")
	textParts, _ := findTags(r.text(), "")
	return &Template{subject: subject, html: htmlParts, text: textParts}, problems, nil
}

// Has reports whether the email's HTML uses the merge tag, such as
// TagUnsubscribeURL.
func (t *Template) Has(tag string) bool {
	for _, s := range t.html {
		if s.tag == tag {
			return true
		}
	}
	return false
}

// Styles shared by the blocks.
const (
	fontStack  = `-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif`
	textColor  = "#27272a"
	titleColor = "#18181b"
	mutedColor = "#71717a"
	linkColor  = "#2563eb"
	padX       = 32 // left and right padding of a top-level block
	colGap     = 8  // padding on each side of a column
)

type renderer struct {
	doc   *emails.Document
	acct  Account
	width int
	b     strings.Builder
}

func (r *renderer) w(parts ...string) {
	for _, p := range parts {
		r.b.WriteString(p)
	}
}

func esc(s string) string { return html.EscapeString(s) }
func px(n int) string     { return strconv.Itoa(n) + "px" }

func (r *renderer) html() string {
	s := r.doc.Settings
	bg := esc(s.BackgroundColor)
	width := strconv.Itoa(r.width)
	r.b.Grow(16 << 10)
	r.w(`<!DOCTYPE html>
<html lang="en" xmlns="http://www.w3.org/1999/xhtml" xmlns:o="urn:schemas-microsoft-com:office:office">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="X-UA-Compatible" content="IE=edge">
<meta name="x-apple-disable-message-reformatting">
<meta name="format-detection" content="telephone=no, date=no, address=no, email=no">
<title>`, esc(s.Subject), `</title>
<!--[if mso]><noscript><xml><o:OfficeDocumentSettings><o:PixelsPerInch>96</o:PixelsPerInch></o:OfficeDocumentSettings></xml></noscript><![endif]-->
<style>
body{margin:0;padding:0;width:100%;-webkit-text-size-adjust:100%;-ms-text-size-adjust:100%}
table,td{border-collapse:collapse;mso-table-lspace:0;mso-table-rspace:0}
img{border:0;outline:none;text-decoration:none;-ms-interpolation-mode:bicubic}
@media only screen and (max-width:`, strconv.Itoa(r.width+20), `px){
.mg-container{width:100% !important;max-width:100% !important}
.mg-pad{padding-left:16px !important;padding-right:16px !important}
.mg-col{display:block !important;width:100% !important;max-width:100% !important;padding-left:0 !important;padding-right:0 !important}
.mg-fluid{width:100% !important;max-width:100% !important;height:auto !important}
}
</style>
</head>
<body style="margin:0;padding:0;background-color:`, bg, `;">
`)
	if s.PreviewText != "" {
		// The preview line most inboxes show after the subject; the filler
		// keeps the body's first words from following it.
		r.w(`<div style="display:none;max-height:0;max-width:0;overflow:hidden;mso-hide:all;font-size:1px;line-height:1px;opacity:0;">`,
			esc(s.PreviewText), strings.Repeat("&#847;&zwnj;&nbsp;", 30), "</div>\n")
	}
	r.w(`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="background-color:`, bg, `;">
<tr><td align="center" style="padding:24px 0;">
<!--[if mso]><table role="presentation" width="`, width, `" cellpadding="0" cellspacing="0" border="0"><tr><td><![endif]-->
<table role="presentation" class="mg-container" width="`, width, `" cellpadding="0" cellspacing="0" border="0" style="width:`, width, `px;max-width:`, width, `px;background-color:#ffffff;">
<tr><td height="16" style="height:16px;font-size:0;line-height:0;">&nbsp;</td></tr>
`)
	inner := r.width - 2*padX
	for _, b := range r.doc.Blocks {
		r.block(b, inner, true)
	}
	r.w(`</table>
<!--[if mso]></td></tr></table><![endif]-->
</td></tr>
</table>
</body>
</html>
`)
	return r.b.String()
}

// block writes one block as a table row. width is the space its content
// has; top says it's at the top level rather than in a column.
func (r *renderer) block(b emails.Block, width int, top bool) {
	pad := "8px 0"
	class := ""
	if top {
		pad = "8px " + px(padX)
		class = ` class="mg-pad"`
	}
	row := func(align string) {
		r.w(`<tr><td`, class, ` align="`, align, `" style="padding:`, pad, `;">`)
	}
	switch b := b.(type) {
	case *emails.Heading:
		size := map[int]int{1: 28, 2: 22, 3: 18}[b.Level]
		tag := "h" + strconv.Itoa(b.Level)
		row(b.Align)
		r.w(`<`, tag, ` style="margin:0;font-family:`, fontStack, `;font-size:`, px(size), `;line-height:1.3;font-weight:700;color:`, titleColor,
			`;text-align:`, b.Align, `;">`, esc(b.Text), `</`, tag, `>`)
	case *emails.Text:
		row(b.Align)
		r.markdown(b.Text, b.Align, "font-size:16px;line-height:1.6;color:"+textColor, 16)
	case *emails.Image:
		w := width
		fluid := b.Width == nil
		if b.Width != nil && *b.Width < width {
			w = *b.Width
		}
		cls := ""
		if fluid {
			cls = ` class="mg-fluid"`
		}
		margin := map[string]string{"left": "0", "center": "0 auto", "right": "0 0 0 auto"}[b.Align]
		row(b.Align)
		img := `<img src="` + esc(b.Src) + `" alt="` + esc(b.Alt) + `" width="` + strconv.Itoa(w) + `"` + cls +
			` style="display:block;width:` + px(w) + `;max-width:100%;height:auto;margin:` + margin + `;border:0;font-family:` + fontStack +
			`;font-size:14px;color:` + mutedColor + `;">`
		if b.Href != nil {
			r.w(`<a href="`, esc(*b.Href), `" style="display:block;text-decoration:none;">`, img, `</a>`)
		} else {
			r.w(img)
		}
	case *emails.Button:
		row(b.Align)
		bg := esc(b.BackgroundColor)
		r.w(`<table role="presentation" cellpadding="0" cellspacing="0" border="0" align="`, b.Align, `"><tr><td align="center" bgcolor="`, bg,
			`" style="border-radius:6px;background-color:`, bg, `;"><a href="`, esc(b.Href), `" style="display:inline-block;padding:12px 24px;font-family:`,
			fontStack, `;font-size:16px;font-weight:600;line-height:1.2;color:`, esc(b.TextColor), `;text-decoration:none;border-radius:6px;">`,
			esc(b.Text), `</a></td></tr></table>`)
	case *emails.Divider:
		row("left")
		r.w(`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0"><tr><td style="border-top:`, px(b.Thickness), ` solid `,
			esc(b.Color), `;font-size:0;line-height:0;height:0;">&nbsp;</td></tr></table>`)
	case *emails.Spacer:
		h := strconv.Itoa(b.Height)
		r.w(`<tr><td height="`, h, `" style="height:`, h, `px;font-size:0;line-height:0;">&nbsp;</td></tr>`+"\n")
		return
	case *emails.Social:
		row(b.Align)
		r.w(`<p style="margin:0;font-family:`, fontStack, `;font-size:14px;line-height:1.6;color:`, mutedColor, `;text-align:`, b.Align, `;">`)
		for i, l := range b.Links {
			if i > 0 {
				r.w(`&nbsp;&middot;&nbsp;`)
			}
			r.w(`<a href="`, esc(l.URL), `" style="color:`, linkColor, `;text-decoration:underline;">`, networkNames[l.Network], `</a>`)
		}
		r.w(`</p>`)
	case *emails.Columns:
		r.columns(b, width)
		return
	case *emails.Footer:
		r.footer(b)
		return
	}
	r.w("</td></tr>\n")
}

func (r *renderer) columns(b *emails.Columns, width int) {
	pad := "8px " + px(padX-colGap)
	r.w(`<tr><td class="mg-pad" style="padding:`, pad, `;">`+"\n"+`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0"><tr>`+"\n")
	colWidth := (width+2*colGap)/len(b.Columns) - 2*colGap
	share := strconv.Itoa(100/len(b.Columns)) + "%"
	for _, col := range b.Columns {
		r.w(`<td class="mg-col" width="`, share, `" valign="top" style="width:`, share, `;padding:0 `, px(colGap), `;">`)
		if len(col.Blocks) == 0 {
			r.w("&nbsp;</td>\n")
			continue
		}
		r.w("\n" + `<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0">` + "\n")
		for _, inner := range col.Blocks {
			r.block(inner, colWidth, false)
		}
		r.w("</table></td>\n")
	}
	r.w("</tr></table>\n</td></tr>\n")
}

func (r *renderer) footer(b *emails.Footer) {
	style := "font-size:12px;line-height:1.6;color:" + mutedColor
	r.w(`<tr><td class="mg-pad" align="center" style="padding:24px `, px(padX), ` 16px;">`)
	if strings.TrimSpace(b.Text) != "" {
		r.markdown(b.Text, "center", style, 8)
		r.w("\n")
	}
	lines := postalLines(r.acct.PostalAddress)
	for i, l := range lines {
		lines[i] = esc(l)
	}
	r.w(`<p style="margin:8px 0 0;font-family:`, fontStack, `;`, style, `;text-align:center;">`, strings.Join(lines, "<br>"), "</p>\n")
	r.w(`<p style="margin:8px 0 0;font-family:`, fontStack, `;`, style, `;text-align:center;"><a href="{{unsubscribe_url}}" style="color:`,
		mutedColor, `;text-decoration:underline;">Unsubscribe</a></p>`)
	r.w("</td></tr>\n")
}

// markdown writes a text block's paragraphs and lists. gap is the space
// between them; the last has none.
func (r *renderer) markdown(src, align, style string, gap int) {
	blocks := parseBlocks(src)
	link := "color:" + linkColor + ";text-decoration:underline;"
	for i, mb := range blocks {
		margin := "0 0 " + px(gap)
		if i == len(blocks)-1 {
			margin = "0"
		}
		base := "margin:" + margin + ";font-family:" + fontStack + ";" + style + ";text-align:" + align + ";"
		switch mb.kind {
		case mdParagraph:
			r.w(`<p style="`, base, `">`)
			for j, line := range mb.lines {
				if j > 0 {
					r.w("<br>")
				}
				inlineHTML(&r.b, parseInline(line), link)
			}
			r.w("</p>")
		case mdBullets, mdNumbers:
			tag := "ul"
			if mb.kind == mdNumbers {
				tag = "ol"
			}
			r.w(`<`, tag, ` style="`, base, `padding:0 0 0 24px;">`)
			for _, item := range mb.lines {
				r.w(`<li style="margin:0 0 4px;">`)
				inlineHTML(&r.b, parseInline(item), link)
				r.w(`</li>`)
			}
			r.w(`</`, tag, `>`)
		}
	}
}

// postalLines splits an address into its non-empty lines.
func postalLines(addr string) []string {
	var out []string
	for _, l := range strings.Split(strings.ReplaceAll(addr, "\r\n", "\n"), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

var networkNames = map[string]string{
	"facebook": "Facebook", "instagram": "Instagram", "x": "X", "linkedin": "LinkedIn", "youtube": "YouTube",
	"tiktok": "TikTok", "threads": "Threads", "bluesky": "Bluesky", "website": "Website",
}
