package render

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gremlin-technologies/mailgoose/internal/emails"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

const postal = "Pat's Bakery\n12 Main St\nSpringfield, IL 62701"

var pat = Recipient{Email: "pat@example.com", FirstName: "Pat", LastName: "Lee", UnsubscribeURL: "https://u.mailgoose.app/u/abc123"}

func doc(t testing.TB, blocks string, settings string) *emails.Document {
	t.Helper()
	if settings == "" {
		settings = `{"subject":"Fresh bread, {{first_name|friend}}","preview_text":"This week at the bakery"}`
	}
	d, err := emails.ParseDocument(emails.KindBlocks, []byte(`{"settings":`+settings+`,"blocks":[`+blocks+`]}`))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func render(t testing.TB, d *emails.Document, r Recipient) Message {
	t.Helper()
	tpl, err := Compile(d, Account{PostalAddress: postal})
	if err != nil {
		t.Fatal(err)
	}
	return tpl.Personalize(r)
}

// goldenCases render every block type and its options. Each is checked at
// the default desktop width (600) and the narrowest content width (480).
var goldenCases = map[string]string{
	"heading": `{"id":"h1","type":"heading","text":"Hello {{first_name|there}}","level":1},
		{"id":"h2","type":"heading","text":"This week","level":2,"align":"center"},
		{"id":"h3","type":"heading","text":"Small print & more","level":3,"align":"right"}`,
	"text": `{"id":"t","type":"text","text":"Hi {{first_name|there}},\nOur **sourdough** is *back*, and [the menu](https://example.com/menu) is up.\n\n- Rye\n- Spelt **with seeds**\n\n1. Order\n2. Collect"},
		{"id":"c","type":"text","text":"Centered <not html> & plain","align":"center"}`,
	"image": `{"id":"full","type":"image","src":"https://cdn.example.com/a.png","alt":"Loaves on a shelf"},
		{"id":"fixed","type":"image","src":"https://cdn.example.com/logo.png","alt":"Logo","width":120,"align":"center","href":"https://example.com"},
		{"id":"wide","type":"image","src":"https://cdn.example.com/b.png","alt":"","width":800,"align":"right"}`,
	"button": `{"id":"b","type":"button","text":"Order now","href":"https://example.com/order?a=1&b=2","align":"center","background_color":"#825142","text_color":"#ffffff"},
		{"id":"l","type":"button","text":"Leave the list","href":"{{unsubscribe_url}}"}`,
	"divider": `{"id":"d","type":"divider","color":"#e4e4e7","thickness":2}`,
	"spacer":  `{"id":"s","type":"spacer","height":40}`,
	"columns": `{"id":"cols","type":"columns","columns":[
			{"blocks":[{"id":"i","type":"image","src":"https://cdn.example.com/c.png","alt":"Croissant"},{"id":"ct","type":"text","text":"**Croissants** every day"}]},
			{"blocks":[{"id":"bh","type":"heading","text":"Opening hours","level":3},{"id":"bb","type":"button","text":"Visit","href":"https://example.com/visit"}]}]},
		{"id":"half","type":"columns","columns":[{"blocks":[{"id":"x","type":"text","text":"Left only"}]},{"blocks":[]}]}`,
	"social": `{"id":"so","type":"social","align":"center","links":[{"network":"instagram","url":"https://instagram.com/patsbakery"},{"network":"website","url":"https://example.com"}]}`,
	"footer": `{"id":"f","type":"footer","text":"You signed up at [our shop](https://example.com).\nThanks, {{first_name|friend}}!"}`,
}

func TestGolden(t *testing.T) {
	for name, blocks := range goldenCases {
		for _, width := range []string{"600", "480"} {
			t.Run(name+"_"+width, func(t *testing.T) {
				settings := `{"subject":"Fresh bread, {{first_name|friend}}","preview_text":"This week at the bakery","content_width":` + width + `}`
				m := render(t, doc(t, blocks, settings), pat)
				golden(t, name+"_"+width+".html", m.HTML)
				golden(t, name+"_"+width+".txt", m.Text)
			})
		}
	}
}

func golden(t *testing.T, file, got string) {
	t.Helper()
	path := filepath.Join("testdata", file)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/render -update to write it)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs from the golden file; if the change is intended, run go test ./internal/render -update and review the diff.\ngot:\n%s", file, got)
	}
}

// TestNarrowScreens checks the rules that adapt the email to a phone: the
// breakpoint follows the content width, columns stack, and full-width
// images and paddings shrink.
func TestNarrowScreens(t *testing.T) {
	for _, width := range []string{"600", "480", "800"} {
		m := render(t, doc(t, goldenCases["columns"], `{"content_width":`+width+`}`), pat)
		for _, want := range []string{
			"@media only screen and (max-width:" + map[string]string{"600": "620", "480": "500", "800": "820"}[width] + "px)",
			`.mg-col{display:block !important;width:100% !important`,
			`<td class="mg-col" width="50%"`,
			`class="mg-fluid"`,
			`class="mg-container" width="` + width + `"`,
			`<meta name="viewport" content="width=device-width, initial-scale=1">`,
		} {
			if !strings.Contains(m.HTML, want) {
				t.Errorf("width %s: html lacks %q", width, want)
			}
		}
	}
}

func TestMergeTags(t *testing.T) {
	d := doc(t, `{"id":"t","type":"text","text":"Hi {{ first_name | there }} {{last_name}} <{{email}}>"}`, `{"subject":"For  {{first_name|you}}","preview_text":"{{email}}"}`)
	tpl, err := Compile(d, Account{PostalAddress: postal})
	if err != nil {
		t.Fatal(err)
	}
	m := tpl.Personalize(Recipient{Email: "a&b@example.com", FirstName: "<Jo>", UnsubscribeURL: "https://u.example/u?x=1&y=2"})
	if m.Subject != "For <Jo>" {
		t.Errorf("subject %q", m.Subject)
	}
	for _, want := range []string{"Hi &lt;Jo&gt;  &lt;a&amp;b@example.com&gt;", `href="https://u.example/u?x=1&amp;y=2"`, "<title>For  &lt;Jo&gt;</title>"} {
		if !strings.Contains(m.HTML, want) {
			t.Errorf("html lacks %q", want)
		}
	}
	if !strings.Contains(m.Text, "Hi <Jo>  <a&b@example.com>") || !strings.Contains(m.Text, "Unsubscribe: https://u.example/u?x=1&y=2") {
		t.Errorf("text:\n%s", m.Text)
	}

	// Fallbacks fill empty values, escaped once in html.
	d = doc(t, `{"id":"t","type":"text","text":"Hi {{first_name|Pat & co}}"}`, "")
	m = render(t, d, Recipient{Email: "x@example.com", FirstName: "  ", UnsubscribeURL: "https://u.example/u"})
	if !strings.Contains(m.HTML, "Hi Pat &amp; co") || !strings.Contains(m.Text, "Hi Pat & co") || m.Subject != "Fresh bread, friend" {
		t.Errorf("fallback: %q / %q", m.Subject, m.Text)
	}
	if strings.Contains(m.HTML, "{{") || strings.Contains(m.Text, "{{") {
		t.Error("a merge tag was left unfilled")
	}
}

func TestLintReportsTags(t *testing.T) {
	d := doc(t, `{"id":"h","type":"heading","text":"Hi {{name}}"},
		{"id":"cols","type":"columns","columns":[{"blocks":[{"id":"t","type":"text","text":"{{email|a}} {{unsubscribe_url|x}} [x](ftp://nope)"}]},{"blocks":[]}]},
		{"id":"b","type":"button","text":"Go {{first_name|{x}}}","href":"{{unsubscribe_url}}"},
		{"id":"f","type":"footer","text":"Bye {{first_name"}`, `{"subject":"{{FIRST_NAME}}"}`)
	errs := Lint(d)
	var got []string
	for _, e := range errs {
		got = append(got, e.Path+": "+e.Message)
	}
	want := []string{
		"settings.subject: {{FIRST_NAME}} isn't a merge tag; use {{first_name}}, {{last_name}}, {{email}} or {{unsubscribe_url}}",
		"blocks[0].text: {{name}} isn't a merge tag; use {{first_name}}, {{last_name}}, {{email}} or {{unsubscribe_url}}",
		"blocks[1].columns[0].blocks[0].text: {{unsubscribe_url}} takes no fallback; every recipient has one",
		"blocks[1].columns[0].blocks[0].text: the link [x](ftp://nope) needs a full https:// address",
		"blocks[2].text: the fallback in {{first_name|{x}} can't contain { or }",
		`blocks[3].text: "{{first_name" opens a merge tag but never closes it with }}`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	_, err := Compile(d, Account{PostalAddress: postal})
	var p Problems
	if !errors.As(err, &p) || len(p) != len(want) {
		t.Errorf("Compile: %v", err)
	}
}

func TestFooterAlwaysHasAddressAndUnsubscribe(t *testing.T) {
	// A document with only the footer, which has no text of its own.
	m := render(t, doc(t, `{"id":"f","type":"footer"}`, `{}`), pat)
	for _, want := range []string{"Pat&#39;s Bakery<br>12 Main St<br>Springfield, IL 62701", `<a href="https://u.mailgoose.app/u/abc123"`, ">Unsubscribe</a>"} {
		if !strings.Contains(m.HTML, want) {
			t.Errorf("html lacks %q", want)
		}
	}
	if !strings.Contains(m.Text, "Pat's Bakery\n12 Main St\nSpringfield, IL 62701\n\nUnsubscribe: https://u.mailgoose.app/u/abc123") {
		t.Errorf("text:\n%s", m.Text)
	}

	if _, err := Compile(doc(t, "", ""), Account{PostalAddress: " \n "}); !errors.Is(err, ErrNoPostalAddress) {
		t.Errorf("no address: %v", err)
	}
	if _, err := Compile(emails.NewDocument(emails.KindHTML), Account{PostalAddress: postal}); !errors.Is(err, ErrNotBlocks) {
		t.Errorf("html email: %v", err)
	}
}

func TestMarkdown(t *testing.T) {
	cases := []struct{ in, html, text string }{
		{"plain & <b>", "plain &amp; &lt;b&gt;", "plain & <b>"},
		{"**bold** and *it*", "<strong>bold</strong> and <em>it</em>", "bold and it"},
		{"**bold *nested* here**", "<strong>bold <em>nested</em> here</strong>", "bold nested here"},
		{"a * b ** c", "a * b ** c", "a * b ** c"},
		{"[site](https://x.example/?a=1&b=2)", `<a href="https://x.example/?a=1&amp;b=2" style="L">site</a>`, "site (https://x.example/?a=1&b=2)"},
		{"[**bold** link](mailto:pat@example.com)", `<a href="mailto:pat@example.com" style="L"><strong>bold</strong> link</a>`, "bold link (mailto:pat@example.com)"},
		{"[bad](javascript:alert(1))", "[bad](javascript:alert(1))", "[bad](javascript:alert(1))"},
		{"[https://x.example](https://x.example)", `<a href="https://x.example" style="L">https://x.example</a>`, "https://x.example"},
	}
	for _, c := range cases {
		var h, txt strings.Builder
		inlineHTML(&h, parseInline(c.in), "L")
		inlineText(&txt, parseInline(c.in))
		if h.String() != c.html || txt.String() != c.text {
			t.Errorf("%q:\n html %q, want %q\n text %q, want %q", c.in, h.String(), c.html, txt.String(), c.text)
		}
	}

	blocks := parseBlocks("one\ntwo\n\n- a\n- b\n1. x\n10. y\n\n\nlast")
	var kinds []mdKind
	for _, b := range blocks {
		kinds = append(kinds, b.kind)
	}
	if len(blocks) != 4 || kinds[0] != mdParagraph || kinds[1] != mdBullets || kinds[2] != mdNumbers || kinds[3] != mdParagraph ||
		strings.Join(blocks[0].lines, "|") != "one|two" || strings.Join(blocks[2].lines, "|") != "x|y" {
		t.Errorf("blocks %+v", blocks)
	}
}

// twentyBlocks is a realistic 20-block email for the benchmark.
func twentyBlocks(t testing.TB) *emails.Document {
	var b []string
	for i := 0; i < 4; i++ {
		b = append(b,
			`{"type":"heading","text":"Section {{first_name|friend}}","level":2}`,
			`{"type":"text","text":"Some **bold** words, a [link](https://example.com/x), and a list:\n\n- one\n- two\n- three"}`,
			`{"type":"image","src":"https://cdn.example.com/p.png","alt":"A picture","href":"https://example.com"}`,
			`{"type":"button","text":"Read more","href":"https://example.com/more"}`,
		)
	}
	b = append(b,
		`{"type":"columns","columns":[{"blocks":[{"type":"text","text":"Left"}]},{"blocks":[{"type":"text","text":"Right"}]}]}`,
		`{"type":"divider"}`,
		`{"type":"social","links":[{"network":"instagram","url":"https://instagram.com/x"}]}`,
		`{"type":"footer","text":"Thanks for reading."}`,
	)
	return doc(t, strings.Join(b, ","), "")
}

func TestTwentyBlocksRenders(t *testing.T) {
	d := twentyBlocks(t)
	if n := len(emails.ListBlocks(d)); n != 22 {
		t.Fatalf("%d blocks", n)
	}
	m := render(t, d, pat)
	if strings.Count(m.HTML, "Section Pat") != 4 {
		t.Error("headings not personalized")
	}
}

func BenchmarkCompile20Blocks(b *testing.B) {
	d := twentyBlocks(b)
	acct := Account{PostalAddress: postal}
	for b.Loop() {
		if _, err := Compile(d, acct); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPersonalize20Blocks(b *testing.B) {
	tpl, err := Compile(twentyBlocks(b), Account{PostalAddress: postal})
	if err != nil {
		b.Fatal(err)
	}
	for b.Loop() {
		tpl.Personalize(pat)
	}
}

// BenchmarkRender20Blocks is the whole job for one recipient: compile and
// personalize.
func BenchmarkRender20Blocks(b *testing.B) {
	d := twentyBlocks(b)
	acct := Account{PostalAddress: postal}
	for b.Loop() {
		tpl, err := Compile(d, acct)
		if err != nil {
			b.Fatal(err)
		}
		tpl.Personalize(pat)
	}
}
