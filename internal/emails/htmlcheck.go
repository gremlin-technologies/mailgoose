package emails

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// MaxSendableHTMLBytes is the most HTML an email may hold (DES-11). Gmail
// clips a message bigger than this behind a "View entire message" link,
// which hides the unsubscribe link and the tracking pixel.
const MaxSendableHTMLBytes = 102 * 1024

// Codes for the problems CheckHTML finds. The ones under "Errors" stop the
// HTML from being saved; unsupported_css is only a warning.
const (
	// Errors
	HTMLMalformed          = "malformed_markup"
	HTMLScript             = "script"
	HTMLForm               = "form"
	HTMLFrame              = "iframe"
	HTMLEmbed              = "embedded_object"
	HTMLExternalStylesheet = "external_stylesheet"
	HTMLEventHandler       = "event_handler"
	HTMLScriptURL          = "javascript_url"
	HTMLDataURL            = "data_url"
	HTMLRedirect           = "redirect"
	HTMLNoUnsubscribe      = "missing_unsubscribe"
	HTMLTooLarge           = "too_large"

	// Warnings
	HTMLUnsupportedCSS = "unsupported_css"
)

// HTMLIssue is one problem in an html email, at a 1-based line.
type HTMLIssue struct {
	Line    int    `json:"line"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (i HTMLIssue) String() string {
	return "line " + strconv.Itoa(i.Line) + ": " + i.Message
}

// HTMLError holds the errors that keep HTML from being saved.
type HTMLError struct {
	Issues []HTMLIssue
}

func (e *HTMLError) Error() string {
	if len(e.Issues) == 0 {
		return "the html isn't valid"
	}
	msg := e.Issues[0].String()
	if n := len(e.Issues) - 1; n == 1 {
		msg += " (and 1 more problem)"
	} else if n > 1 {
		msg += fmt.Sprintf(" (and %d more problems)", n)
	}
	return msg
}

// UnsupportedCSS lists the CSS that major email clients (Gmail, Outlook
// desktop and web, Apple Mail, Yahoo) drop or mangle. Using it is a
// warning, not an error: the email still goes, but some people see it
// without that styling. Keep this list and the README in step.
var UnsupportedCSS = []struct {
	Pattern *regexp.Regexp
	What    string
	Why     string
}{
	{cssProp(`position`), "position", "Gmail and Outlook drop it"},
	{regexp.MustCompile(`(?i)\bdisplay\s*:\s*(?:inline-)?(?:flex|grid)\b`), "display: flex or grid", "Outlook desktop ignores it"},
	{cssProp(`(?:flex|flex-[a-z-]+|grid|grid-[a-z-]+|gap|row-gap|column-gap)`), "flexbox and grid properties", "Outlook desktop ignores them"},
	{cssProp(`(?:transform|transition|animation|animation-[a-z-]+)`), "transforms and animation", "most clients drop them"},
	{regexp.MustCompile(`(?i)@keyframes\b`), "@keyframes", "most clients drop animation"},
	{regexp.MustCompile(`(?i)\bvar\(\s*--|(?:^|[;{\s])--[a-z0-9_-]+\s*:`), "CSS variables", "Gmail drops them"},
	{cssProp(`(?:filter|backdrop-filter|clip-path|mask|mask-[a-z-]+)`), "filters and masks", "most clients drop them"},
	{cssProp(`object-fit`), "object-fit", "Gmail and Outlook drop it"},
	{regexp.MustCompile(`(?i)@font-face\b`), "@font-face", "Gmail and Outlook desktop fall back to a system font"},
	{regexp.MustCompile(`(?i)@supports\b`), "@supports", "Gmail drops the whole rule"},
}

// cssProp matches a declaration of one of the properties in names.
func cssProp(names string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)(?:^|[;{\s"'])` + names + `\s*:`)
}

var (
	// unsubscribeHref is a link whose whole address is the unsubscribe tag.
	unsubscribeHref = regexp.MustCompile(`^\s*\{\{\s*unsubscribe_url\s*\}\}\s*$`)
	cssImport       = regexp.MustCompile(`(?i)@import\b`)
)

// Elements with no end tag, and elements whose end tag HTML lets you leave
// out. Leaving out any other end tag is malformed markup.
var (
	voidElements = set("area", "base", "br", "col", "embed", "hr", "img", "input", "link", "meta",
		"param", "source", "track", "wbr", "keygen")
	optionalEnd = set("html", "head", "body", "p", "li", "dt", "dd", "option", "optgroup",
		"tr", "td", "th", "thead", "tbody", "tfoot", "colgroup", "caption", "rt", "rp")
)

func set(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

type openTag struct {
	name string
	line int
}

// CheckHTML reads an html email and returns what stops it being saved
// (errors) and what some clients will show differently (warnings), each
// with the line it's on. HTML with no errors is safe to save and send.
func CheckHTML(src string) (errs, warns []HTMLIssue) {
	c := &htmlChecker{}
	c.run(src)
	return c.errs, c.warns
}

type htmlChecker struct {
	errs, warns []HTMLIssue
	open        []openTag
	warned      map[string]bool
	bodyLine    int
	// unsubscribe is set by a link to {{unsubscribe_url}}, as the
	// tokenizer sees it: not in a comment, and not as plain text.
	unsubscribe bool
}

func (c *htmlChecker) fail(line int, code, format string, args ...any) {
	c.errs = append(c.errs, HTMLIssue{Line: line, Code: code, Message: fmt.Sprintf(format, args...)})
}

func (c *htmlChecker) run(src string) {
	if len(src) > MaxSendableHTMLBytes {
		c.fail(lineAt(src, MaxSendableHTMLBytes), HTMLTooLarge,
			"the html is %s; the most an email can be is %s, or Gmail clips it and hides the unsubscribe link",
			kb(len(src)), kb(MaxSendableHTMLBytes))
	}

	z := html.NewTokenizer(strings.NewReader(src))
	line := 1
	inStyle, styleLine := false, 0
	for {
		tt := z.Next()
		raw := string(z.Raw())
		start := line
		line += strings.Count(raw, "\n")
		switch tt {
		case html.ErrorToken:
			if err := z.Err(); !errors.Is(err, io.EOF) {
				c.fail(start, HTMLMalformed, "the markup can't be read: %v", err)
			}
			c.end()
			if !c.unsubscribe {
				at := c.bodyLine
				if at == 0 {
					at = lineAt(src, len(strings.TrimRight(src, "\n")))
				}
				c.fail(at, HTMLNoUnsubscribe, "there's no link to {{unsubscribe_url}}; every email needs an unsubscribe link, such as <a href=\"{{unsubscribe_url}}\">Unsubscribe</a> (one inside a comment, or the tag as plain text, doesn't count)")
			}
			return
		case html.CommentToken:
			if !strings.HasSuffix(raw, ">") || (strings.HasPrefix(raw, "<!--") && !strings.HasSuffix(raw, "-->")) {
				c.fail(start, HTMLMalformed, "a comment opened here never closes with -->")
			}
		case html.TextToken:
			if inStyle {
				c.css(raw, styleLine, true)
			}
		case html.StartTagToken, html.SelfClosingTagToken:
			tok := z.Token()
			c.tag(tok, start)
			if tok.Data == "style" && tt == html.StartTagToken {
				inStyle, styleLine = true, start+strings.Count(raw, "\n")
			}
			if tt == html.StartTagToken && !voidElements[tok.Data] {
				c.open = append(c.open, openTag{tok.Data, start})
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			tag := string(name)
			if tag == "style" {
				inStyle = false
			}
			if tag == "body" && c.bodyLine == 0 {
				c.bodyLine = start
			}
			c.close(tag, start)
		}
	}
}

// tag checks a start tag's element and attributes.
func (c *htmlChecker) tag(tok html.Token, line int) {
	switch tok.Data {
	case "script":
		c.fail(line, HTMLScript, "<script> isn't allowed; email clients won't run it, and spam filters reject mail that has it")
	case "form":
		c.fail(line, HTMLForm, "<form> isn't allowed; most email clients block forms. Link to a signup form instead")
	case "iframe", "frame", "frameset":
		c.fail(line, HTMLFrame, "<%s> isn't allowed; email clients don't show frames", tok.Data)
	case "object", "embed", "applet":
		c.fail(line, HTMLEmbed, "<%s> isn't allowed; email clients don't show embedded objects", tok.Data)
	case "base":
		c.fail(line, HTMLRedirect, "<base> isn't allowed; it changes where every link in the email goes")
	case "meta":
		for _, a := range tok.Attr {
			if strings.EqualFold(a.Key, "http-equiv") && strings.EqualFold(strings.TrimSpace(a.Val), "refresh") {
				c.fail(line, HTMLRedirect, "<meta http-equiv=\"refresh\"> isn't allowed; an email can't send the reader to another page by itself")
			}
		}
	case "link":
		for _, a := range tok.Attr {
			if a.Key == "rel" && strings.Contains(strings.ToLower(a.Val), "stylesheet") {
				c.fail(line, HTMLExternalStylesheet, "external stylesheets aren't allowed; most clients ignore them. Put the CSS in a <style> element or style attributes")
			}
		}
	}
	for _, a := range tok.Attr {
		key := strings.ToLower(a.Key)
		if len(key) > 2 && strings.HasPrefix(key, "on") {
			c.fail(line, HTMLEventHandler, "the %s attribute on <%s> isn't allowed; email clients don't run scripts", key, tok.Data)
			continue
		}
		if scriptURL(a.Val) || (key == "style" && strings.Contains(squash(a.Val), "javascript:")) {
			c.fail(line, HTMLScriptURL, "the %s attribute on <%s> holds a javascript: address, which isn't allowed", key, tok.Data)
			continue
		}
		if key == "href" && strings.HasPrefix(squash(a.Val), "data:") {
			c.fail(line, HTMLDataURL, "the href on <%s> holds a data: address, which isn't allowed; link to a web page instead", tok.Data)
			continue
		}
		if key == "href" && (tok.Data == "a" || tok.Data == "area") && unsubscribeHref.MatchString(a.Val) {
			c.unsubscribe = true
		}
		if key == "style" {
			c.css(a.Val, line, false)
		}
	}
}

// close matches an end tag with the element it closes.
func (c *htmlChecker) close(tag string, line int) {
	if voidElements[tag] {
		return
	}
	at := -1
	for i := len(c.open) - 1; i >= 0; i-- {
		if c.open[i].name == tag {
			at = i
			break
		}
	}
	if at < 0 {
		if !optionalEnd[tag] {
			c.fail(line, HTMLMalformed, "</%s> closes nothing: no <%s> is open here", tag, tag)
		}
		return
	}
	for _, o := range c.open[at+1:] {
		if !optionalEnd[o.name] {
			c.fail(line, HTMLMalformed, "</%s> comes before </%s>, which the <%s> on line %d needs first", tag, o.name, o.name, o.line)
		}
	}
	c.open = c.open[:at]
}

// end reports elements never closed.
func (c *htmlChecker) end() {
	for _, o := range c.open {
		if !optionalEnd[o.name] {
			c.fail(o.line, HTMLMalformed, "<%s> opened here never closes", o.name)
		}
	}
	c.open = nil
}

// css checks a style element's text or a style attribute. In a style
// element each finding gets its own line; in an attribute, the tag's.
func (c *htmlChecker) css(css string, line int, element bool) {
	at := func(i int) int {
		if !element {
			return line
		}
		return line + strings.Count(css[:i], "\n")
	}
	if element && strings.Contains(squash(css), "javascript:") {
		c.fail(line, HTMLScriptURL, "the <style> element holds a javascript: address, which isn't allowed")
	}
	for _, m := range cssImport.FindAllStringIndex(css, -1) {
		c.fail(at(m[0]), HTMLExternalStylesheet, "@import loads an external stylesheet, which isn't allowed; most clients ignore it")
	}
	for _, u := range UnsupportedCSS {
		for _, m := range u.Pattern.FindAllStringIndex(css, -1) {
			l := at(m[0])
			key := strconv.Itoa(l) + u.What
			if c.warned == nil {
				c.warned = map[string]bool{}
			}
			if c.warned[key] {
				continue
			}
			c.warned[key] = true
			c.warns = append(c.warns, HTMLIssue{Line: l, Code: HTMLUnsupportedCSS,
				Message: u.What + ": " + u.Why + ", so some people won't see this styling"})
		}
	}
}

// scriptURL reports a URL that would run script, ignoring the spaces and
// control characters browsers skip.
func scriptURL(v string) bool {
	s := squash(v)
	return strings.HasPrefix(s, "javascript:") || strings.HasPrefix(s, "vbscript:")
}

// squash lowercases v and drops whitespace and control characters.
func squash(v string) string {
	var b strings.Builder
	for _, r := range v {
		if r <= ' ' || r == 0x7f {
			continue
		}
		if r >= 'A' && r <= 'Z' {
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

func lineAt(src string, offset int) int {
	if offset > len(src) {
		offset = len(src)
	}
	return 1 + strings.Count(src[:offset], "\n")
}

func kb(n int) string {
	return strconv.FormatFloat(float64(n)/1024, 'f', 1, 64) + " KB"
}
