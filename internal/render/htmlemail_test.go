package render

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gremlin-technologies/mailgoose/internal/emails"
)

func htmlDoc(t *testing.T, src, settings string) *emails.Document {
	t.Helper()
	if settings == "" {
		settings = `{"subject":"News for {{first_name|you}}"}`
	}
	raw, _ := json.Marshal(src)
	d, err := emails.ParseDocument(emails.KindHTML, []byte(`{"settings":`+settings+`,"html":`+string(raw)+`}`))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

const page = `<!DOCTYPE html>
<html><head><title>x</title><style>p{color:#333}</style></head>
<BODY class="x">
<h1>Hi {{first_name|friend}} &amp; co</h1>
<p>Our <b>new</b>
   menu is <a href="https://example.com/menu?a=1&amp;b=2">here</a>.</p>
<ul><li>Rye<li>Spelt</ul>
<img src="https://cdn.example.com/a.png" alt="A loaf">
<hr>
<p><a href="{{unsubscribe_url}}">Unsubscribe</a> &middot; <a href="https://example.com">https://example.com</a></p>
</Body>
</html>`

func TestCompileHTMLAddsFooter(t *testing.T) {
	tpl, err := CompileHTML(htmlDoc(t, page, ""), Account{PostalAddress: postal})
	if err != nil {
		t.Fatal(err)
	}
	m := tpl.Personalize(pat)
	footer := "Pat&#39;s Bakery<br>12 Main St<br>Springfield, IL 62701</div>\n</Body>"
	if !strings.Contains(m.HTML, footer) {
		t.Errorf("no footer before </body>:\n%s", m.HTML)
	}
	if m.Subject != "News for Pat" || !strings.Contains(m.HTML, "<h1>Hi Pat &amp; co</h1>") ||
		!strings.Contains(m.HTML, `<a href="https://u.mailgoose.app/u/abc123">Unsubscribe</a>`) {
		t.Errorf("merge tags: %q\n%s", m.Subject, m.HTML)
	}
	want := `Hi Pat & co

Our new menu is here (https://example.com/menu?a=1&b=2).

- Rye
- Spelt
[A loaf]
----------

Unsubscribe (https://u.mailgoose.app/u/abc123) · https://example.com

Pat's Bakery
12 Main St
Springfield, IL 62701
`
	if m.Text != want {
		t.Errorf("text:\n%s\nwant:\n%s", m.Text, want)
	}
}

func TestCompileHTMLKeepsAddressItHas(t *testing.T) {
	src := `<p>Hi</p><p style="font-size:12px">PAT'S BAKERY<br> 12 Main St<br>
Springfield, IL 62702</p><a href="{{unsubscribe_url}}">Unsubscribe</a>`
	// One line differs, so the footer is added.
	tpl, _ := CompileHTML(htmlDoc(t, src, ""), Account{PostalAddress: postal})
	if m := tpl.Personalize(pat); !strings.Contains(m.HTML, "12 Main St<br>") {
		t.Error("a near miss should still get the footer")
	}

	src = `<p>Hi</p><p>PAT'S   BAKERY<br> 12 Main St<br>Springfield,  IL 62701</p><a href="{{unsubscribe_url}}">Unsubscribe</a>`
	tpl, err := CompileHTML(htmlDoc(t, src, ""), Account{PostalAddress: postal})
	if err != nil {
		t.Fatal(err)
	}
	m := tpl.Personalize(pat)
	if strings.Count(m.HTML, "Main St") != 1 {
		t.Errorf("footer added though the address is there:\n%s", m.HTML)
	}
	if !strings.HasSuffix(m.HTML, "Unsubscribe</a>") {
		t.Error("the markup changed")
	}
}

func TestCompileHTMLFragmentAndPreheader(t *testing.T) {
	src := `<p>Hi</p><a href="{{unsubscribe_url}}">Unsubscribe</a>`
	tpl, err := CompileHTML(htmlDoc(t, src, `{"preview_text":"Fresh this week"}`), Account{PostalAddress: postal})
	if err != nil {
		t.Fatal(err)
	}
	m := tpl.Personalize(pat)
	if !strings.HasPrefix(m.HTML, `<div style="display:none;`) || !strings.Contains(m.HTML, ">Fresh this week&#847;") {
		t.Errorf("preheader:\n%s", m.HTML)
	}
	if !strings.HasSuffix(m.HTML, "Springfield, IL 62701</div>\n") {
		t.Errorf("fragment footer:\n%s", m.HTML)
	}
	if strings.Contains(m.Text, "Fresh") {
		t.Errorf("the hidden preview line is in the text part:\n%s", m.Text)
	}

	tpl, _ = CompileHTML(htmlDoc(t, page, `{"preview_text":"Fresh"}`), Account{PostalAddress: postal})
	if m := tpl.Personalize(pat); !strings.Contains(m.HTML, "<BODY class=\"x\">\n<div style=\"display:none;") {
		t.Errorf("preheader after <body>:\n%s", m.HTML[:300])
	}
}

func TestCompileHTMLRefusals(t *testing.T) {
	_, err := CompileHTML(htmlDoc(t, `<p>{{name}}</p>{{unsubscribe_url}}`, `{"subject":"{{first}}"}`), Account{PostalAddress: postal})
	var p Problems
	if !errors.As(err, &p) || len(p) != 2 || p[0].Path != "settings.subject" || p[1].Path != "html" {
		t.Errorf("tags: %v", err)
	}
	if _, err := CompileHTML(htmlDoc(t, page, ""), Account{}); !errors.Is(err, ErrNoPostalAddress) {
		t.Errorf("no address: %v", err)
	}
	if _, err := CompileHTML(emails.NewDocument(emails.KindBlocks), Account{PostalAddress: postal}); !errors.Is(err, ErrNotHTML) {
		t.Errorf("blocks: %v", err)
	}
}
