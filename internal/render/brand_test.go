package render

import (
	"encoding/json"
	"strings"
	"testing"
)

const (
	signupLink = "https://app.mailgoose.test/signup?ref=footer"
	wordmark   = "https://app.mailgoose.test/assets/brand/v1/wordmark.png"
)

var freePat = func() Recipient { r := pat; r.Branded = true; return r }()

// A free account's copies carry the line under the compliance footer, in
// the HTML and the text; a paid account's don't (MG-119).
func TestBrandFooterInBlocksEmails(t *testing.T) {
	d := doc(t, goldenCases["footer"], "")
	free, paid := render(t, d, freePat), render(t, d, pat)

	for _, want := range []string{`<a href="` + signupLink + `"`, `<img src="` + wordmark + `" alt="Sent by mailgoose"`} {
		if !strings.Contains(free.HTML, want) {
			t.Errorf("free html lacks %s", want)
		}
	}
	// Under the postal address and the unsubscribe link, the last thing in the email.
	if strings.Index(free.HTML, "Unsubscribe</a>") > strings.Index(free.HTML, signupLink) ||
		strings.Index(free.HTML, signupLink) > strings.Index(free.HTML, "</table>\n<!--[if mso]>") {
		t.Errorf("footer line is out of place:\n%s", free.HTML)
	}
	if !strings.HasSuffix(free.Text, "\nSent by mailgoose: "+signupLink+"\n") {
		t.Errorf("free text = %q", free.Text)
	}
	if strings.Contains(paid.HTML, "Sent by mailgoose") || strings.Contains(paid.HTML, "ref=footer") ||
		strings.Contains(paid.Text, "Sent by mailgoose") {
		t.Errorf("paid copy carries the footer:\n%s\n%s", paid.HTML, paid.Text)
	}
	// What a paid copy renders is the email without the slot: nothing else differs.
	if strings.Replace(free.HTML, brandHTML(assets), "", 1) != paid.HTML ||
		strings.Replace(free.Text, brandText(assets), "", 1) != paid.Text {
		t.Error("a free copy differs from a paid one by more than the footer")
	}
}

func TestBrandFooterInHTMLEmails(t *testing.T) {
	tpl, err := CompileHTML(htmlDoc(t, page, ""), Account{PostalAddress: postal, AssetsURL: assets})
	if err != nil {
		t.Fatal(err)
	}
	free, paid := tpl.Personalize(freePat), tpl.Personalize(pat)
	i := strings.Index(free.HTML, "Springfield, IL 62701")
	j := strings.Index(free.HTML, signupLink)
	if i < 0 || j < i || strings.Index(free.HTML, "</Body>") < j {
		t.Errorf("the line should follow the postal address, before </body>:\n%s", free.HTML)
	}
	if !strings.HasSuffix(free.Text, "\nSent by mailgoose: "+signupLink+"\n") {
		t.Errorf("free text = %q", free.Text)
	}
	if strings.Contains(paid.HTML, "Sent by mailgoose") || strings.Contains(paid.Text, "Sent by mailgoose") {
		t.Error("paid copy carries the footer")
	}

	// An html fragment gets it at the end; markup that shows the address
	// already gets it after what it has.
	frag, err := CompileHTML(htmlDoc(t, `<p>Hi</p><p><a href="{{unsubscribe_url}}">Unsubscribe</a></p>`, ""), Account{PostalAddress: postal, AssetsURL: assets})
	if err != nil {
		t.Fatal(err)
	}
	if got := frag.Personalize(freePat).HTML; !strings.HasSuffix(got, brandHTMLFragment(assets)) {
		t.Errorf("fragment = %s", got)
	}
}

// The footer can't be written into or out of an email by its markup: a NUL
// in the customer's html is dropped, and the footer is the template's alone.
func TestBrandFooterCantBeForgedOrKept(t *testing.T) {
	src := strings.Replace(page, "<hr>", "<hr>\x00mg-brand\x00", 1)
	tpl, err := CompileHTML(htmlDoc(t, src, ""), Account{PostalAddress: postal, AssetsURL: assets})
	if err != nil {
		t.Fatal(err)
	}
	if got := tpl.Personalize(pat).HTML; strings.Contains(got, "\x00") || strings.Contains(got, "Sent by mailgoose") {
		t.Errorf("paid copy has a forged slot: %q", got)
	}
	if got := strings.Count(tpl.Personalize(freePat).HTML, signupLink); got != 1 {
		t.Errorf("free copy has %d footers, want 1", got)
	}
}

// With no base URL there's nowhere to link or load the wordmark from.
func TestBrandFooterNeedsABaseURL(t *testing.T) {
	tpl, err := Compile(doc(t, goldenCases["footer"], ""), Account{PostalAddress: postal})
	if err != nil {
		t.Fatal(err)
	}
	if m := tpl.Personalize(freePat); strings.Contains(m.HTML, "Sent by mailgoose") || strings.Contains(m.Text, "Sent by mailgoose") {
		t.Error("a footer with no base URL")
	}
}

// A launched campaign keeps its template as JSON; the footer's place and
// its pieces survive that, so a copy rendered later still decides per copy.
func TestBrandFooterSurvivesStorage(t *testing.T) {
	tpl, err := Compile(doc(t, goldenCases["footer"], ""), Account{PostalAddress: postal, AssetsURL: assets})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(tpl)
	if err != nil {
		t.Fatal(err)
	}
	var back Template
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if a, b := tpl.Personalize(freePat), back.Personalize(freePat); a.HTML != b.HTML || a.Text != b.Text {
		t.Error("a stored template renders differently")
	}
	if got := back.Personalize(pat).HTML; strings.Contains(got, "Sent by mailgoose") {
		t.Error("stored paid copy carries the footer")
	}
}

func TestBrandLink(t *testing.T) {
	if !IsBrandLink("https://app.mailgoose.test/", signupLink) || IsBrandLink(assets, "https://app.mailgoose.test/signup") ||
		IsBrandLink("", signupLink) {
		t.Error("IsBrandLink")
	}
}

// The line at phone width, as a copy is sent (MG-119).
func TestBrandFooterGolden(t *testing.T) {
	settings := `{"subject":"Fresh bread","content_width":480}`
	m := render(t, doc(t, goldenCases["footer"], settings), freePat)
	golden(t, "brand_480.html", m.HTML)
	golden(t, "brand_480.txt", m.Text)
}
