// Package emailcheck looks over an email before it's sent (DES-12): what
// would stop it going (errors) and what some readers would trip on
// (warnings). Errors block a launch; warnings don't.
package emailcheck

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"golang.org/x/net/html"

	"github.com/gremlin-technologies/mailgoose/internal/emails"
	"github.com/gremlin-technologies/mailgoose/internal/render"
)

// Error codes: any of these stops a launch.
const (
	MissingSubject       = "missing_subject"
	MissingFrom          = "missing_from"
	UnverifiedFrom       = "unverified_from"
	MissingPostalAddress = "missing_postal_address"
	EmptyEmail           = "empty_email"
	MergeTag             = "merge_tag"
	MissingUnsubscribe   = "missing_unsubscribe"
	BrokenLink           = "broken_link"
	TooLarge             = "too_large"
	InvalidHTML          = "invalid_html"
)

// Warning codes.
const (
	InsecureLink    = "insecure_link"
	LinkUnreachable = "link_unreachable"
	LinkNotChecked  = "link_not_checked"
	MissingAlt      = "missing_alt"
	NearSizeLimit   = "near_size_limit"
	UnsupportedCSS  = emails.HTMLUnsupportedCSS
)

// NearSizeBytes is where the size warning starts: tracking adds to every
// link at send time, so an email this close to Gmail's clipping point may
// pass it once sent.
const NearSizeBytes = 90 * 1024

// Issue is one finding. Path names a block field (blocks[2].alt) or a
// setting; Line is a line of an html email's markup; URL is the link it's
// about. Each is empty when it doesn't apply.
type Issue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Path    string `json:"path,omitempty"`
	Line    int    `json:"line,omitempty"`
	URL     string `json:"url,omitempty"`
}

// Result is everything the check found. Errors and Warnings are never nil,
// so they're [] rather than null in JSON.
type Result struct {
	Errors   []Issue `json:"errors"`
	Warnings []Issue `json:"warnings"`
}

// OK reports whether nothing stops the email being sent.
func (r *Result) OK() bool { return len(r.Errors) == 0 }

// Input is the email and what the check needs to know about its account.
type Input struct {
	Doc *emails.Document
	// PostalAddress is the account's; empty when it has none.
	PostalAddress string
	// FromEmail is the address the email goes from: its own from_email, or
	// the account's default. Empty when neither is set.
	FromEmail string
	// FromVerified is whether FromEmail is on a verified sending domain.
	FromVerified bool
}

// unsubscribeSample stands in for each recipient's own unsubscribe link,
// so the link check can tell it apart and skip it.
const unsubscribeSample = "https://unsubscribe.mailgoose.invalid/sample-recipient-token-0123456789abcdef"

// sample is the recipient the check renders for, as the preview does.
var sample = render.Recipient{Email: "alex@example.com", FirstName: "Alex", LastName: "Rivera", UnsubscribeURL: unsubscribeSample}

// placeholderAddress lets an email without an account postal address
// render, so its other problems still show.
const placeholderAddress = "1 Placeholder Street\nAnytown"

// Check looks the email over. links fetches each link; nil skips fetching
// and only looks at the addresses themselves.
func Check(ctx context.Context, in Input, links LinkChecker) Result {
	c := &checker{res: Result{Errors: []Issue{}, Warnings: []Issue{}}}
	doc := in.Doc
	st := doc.Settings

	if strings.TrimSpace(st.Subject) == "" {
		c.fail(Issue{Code: MissingSubject, Path: "settings.subject", Message: "The email has no subject."})
	}
	switch {
	case in.FromEmail == "":
		c.fail(Issue{Code: MissingFrom, Path: "settings.from_email",
			Message: "The email has no From address, and the account has no default one."})
	case !in.FromVerified:
		c.fail(Issue{Code: UnverifiedFrom, Path: "settings.from_email",
			Message: fmt.Sprintf("%s isn't on a verified sending domain, so it can't send yet.", in.FromEmail)})
	}
	addr := in.PostalAddress
	if strings.TrimSpace(addr) == "" {
		c.fail(Issue{Code: MissingPostalAddress,
			Message: "The account has no postal address, which every email must show. Add it in Settings."})
		addr = placeholderAddress
	}

	if doc.HTML != nil {
		if strings.TrimSpace(*doc.HTML) == "" {
			c.fail(Issue{Code: EmptyEmail, Path: "html", Message: "The email has no markup yet."})
			return c.res
		}
		c.markup(*doc.HTML)
	} else if !hasContent(doc) {
		c.fail(Issue{Code: EmptyEmail, Path: "blocks", Message: "The email has no blocks besides the footer."})
	}

	tpl, problems, err := render.Draft(doc, render.Account{PostalAddress: addr})
	if err != nil {
		c.fail(Issue{Code: InvalidHTML, Message: "The email can't be rendered: " + err.Error()})
		return c.res
	}
	for _, p := range problems {
		code := MergeTag
		if p.Tag == "" {
			code = BrokenLink
		}
		c.fail(Issue{Code: code, Path: p.Path, Message: sentence(p.Message)})
	}
	// An html email's unsubscribe link is checked in markup, where a tag
	// inside a comment or as plain text doesn't count.
	if doc.HTML == nil && !tpl.Has(render.TagUnsubscribeURL) {
		c.fail(Issue{Code: MissingUnsubscribe, Path: "html",
			Message: `There's no {{unsubscribe_url}}; every email needs an unsubscribe link.`})
	}
	if doc.HTML == nil {
		c.blockAlts(doc)
	}

	msg := tpl.Personalize(sample)
	switch n := len(msg.HTML); {
	case n > emails.MaxSendableHTMLBytes:
		c.fail(Issue{Code: TooLarge, Message: fmt.Sprintf(
			"The email is %s; Gmail clips anything over %s and hides the unsubscribe link.", kb(n), kb(emails.MaxSendableHTMLBytes))})
	case n > NearSizeBytes:
		c.warn(Issue{Code: NearSizeLimit, Message: fmt.Sprintf(
			"The email is %s, close to the %s where Gmail clips it; link tracking adds a little more.", kb(n), kb(emails.MaxSendableHTMLBytes))})
	}
	c.links(ctx, msg.HTML, links)
	return c.res
}

type checker struct {
	res Result
}

func (c *checker) fail(i Issue) { c.res.Errors = append(c.res.Errors, i) }
func (c *checker) warn(i Issue) { c.res.Warnings = append(c.res.Warnings, i) }

// hasContent reports whether a blocks email has any block but the footer.
func hasContent(doc *emails.Document) bool {
	for _, b := range doc.Blocks {
		if _, footer := b.(*emails.Footer); !footer {
			return true
		}
	}
	return false
}

// markup reports what emails.CheckHTML finds in an html email, and images
// without alt text. Its size finding is left to Check, which looks at the
// email as rendered.
func (c *checker) markup(src string) {
	errs, warns := emails.CheckHTML(src)
	for _, e := range errs {
		if e.Code == emails.HTMLTooLarge {
			continue
		}
		if e.Code == emails.HTMLNoUnsubscribe {
			c.fail(Issue{Code: MissingUnsubscribe, Path: "html", Line: e.Line, Message: sentence(e.Message)})
			continue
		}
		c.fail(Issue{Code: InvalidHTML, Line: e.Line, Message: sentence(e.Message)})
	}
	for _, w := range warns {
		c.warn(Issue{Code: w.Code, Line: w.Line, Message: sentence(w.Message)})
	}
	z := html.NewTokenizer(strings.NewReader(src))
	line := 1
	for {
		tt := z.Next()
		start := line
		line += strings.Count(string(z.Raw()), "\n")
		if tt == html.ErrorToken {
			return
		}
		if tt != html.StartTagToken && tt != html.SelfClosingTagToken {
			continue
		}
		tok := z.Token()
		if tok.Data == "img" && !hasAlt(tok) {
			c.warn(Issue{Code: MissingAlt, Line: start,
				Message: "An image has no alt text, so people who don't load images, or use a screen reader, get nothing."})
		}
	}
}

func hasAlt(tok html.Token) bool {
	for _, a := range tok.Attr {
		if a.Key == "alt" {
			return strings.TrimSpace(a.Val) != ""
		}
	}
	return false
}

// blockAlts warns about image blocks without alt text.
func (c *checker) blockAlts(doc *emails.Document) {
	for _, info := range emails.ListBlocks(doc) {
		if img, ok := info.Block.(*emails.Image); ok && strings.TrimSpace(img.Alt) == "" {
			c.warn(Issue{Code: MissingAlt, Path: info.Path + ".alt",
				Message: "The image has no alt text, so people who don't load images, or use a screen reader, get nothing."})
		}
	}
}

// links checks every link in the rendered email, once each.
func (c *checker) links(ctx context.Context, src string, checker LinkChecker) {
	var fetch []string
	seen := map[string]bool{}
	for _, href := range hrefs(src) {
		if seen[href] || href == unsubscribeSample {
			continue
		}
		seen[href] = true
		u, err := url.Parse(href)
		scheme := ""
		if err == nil {
			scheme = strings.ToLower(u.Scheme)
		}
		switch {
		case strings.HasPrefix(href, "#"), scheme == "mailto", scheme == "tel", scheme == "sms":
			continue
		case strings.Contains(href, "{{"):
			continue // a merge tag that couldn't be filled, already reported
		case err != nil || (scheme != "http" && scheme != "https") || u.Host == "":
			c.fail(Issue{Code: BrokenLink, URL: href, Message: fmt.Sprintf("The link %q isn't a full web address.", clip(href))})
			continue
		case scheme == "http":
			c.warn(Issue{Code: InsecureLink, URL: href,
				Message: fmt.Sprintf("The link %s isn't https://, so some clients warn about it.", clip(href))})
		}
		fetch = append(fetch, href)
	}
	if checker == nil || len(fetch) == 0 {
		return
	}
	found := checker.CheckLinks(ctx, fetch)
	for _, href := range fetch {
		st := found[href]
		switch st.Result {
		case ResultOK:
		case ResultMissing:
			c.fail(Issue{Code: BrokenLink, URL: href, Message: fmt.Sprintf("The link %s is broken: %s.", clip(href), st.Detail)})
		case ResultUnreachable:
			c.warn(Issue{Code: LinkUnreachable, URL: href, Message: fmt.Sprintf("The link %s may be broken: %s.", clip(href), st.Detail)})
		default:
			detail := st.Detail
			if detail == "" {
				detail = "it wasn't checked"
			}
			c.warn(Issue{Code: LinkNotChecked, URL: href, Message: fmt.Sprintf("The link %s wasn't checked: %s.", clip(href), detail)})
		}
	}
}

// hrefs lists every <a href> in the HTML, in order.
func hrefs(src string) []string {
	var out []string
	z := html.NewTokenizer(strings.NewReader(src))
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			return out
		}
		if tt != html.StartTagToken {
			continue
		}
		tok := z.Token()
		if tok.Data != "a" {
			continue
		}
		for _, a := range tok.Attr {
			if a.Key == "href" {
				out = append(out, strings.TrimSpace(a.Val))
			}
		}
	}
}

// sentence starts a message with a capital letter and ends it with a full
// stop.
func sentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	s = strings.ToUpper(s[:1]) + s[1:]
	if !strings.HasSuffix(s, ".") {
		s += "."
	}
	return s
}

func clip(s string) string {
	if r := []rune(s); len(r) > 80 {
		return string(r[:80]) + "…"
	}
	return s
}

func kb(n int) string {
	return fmt.Sprintf("%.1f KB", float64(n)/1024)
}
