package render

import (
	"fmt"
	"strings"
	"testing"
)

type fakeTracker struct{}

func (fakeTracker) Click(n int, target string) string {
	if !strings.HasPrefix(target, "http") {
		return target
	}
	return fmt.Sprintf("https://app.test/t/c/%d", n)
}
func (fakeTracker) Pixel() string { return "https://app.test/t/o/px" }

func TestTrack(t *testing.T) {
	src := `<!doctype html><html><head><style>a{color:red}</style></head><body>
<p>Hi <a href="https://example.com/a?x=1&amp;y=2" style="color:#000">shop</a>,
<a href="mailto:pat@example.com">mail</a>, <a name="top">anchor</a>,
<a href="https://example.com/b">again</a>,
<a href="https://app.test/u/u1abc">Unsubscribe</a></p>
</body></html>`
	got := Track(src, fakeTracker{}, func(href string) bool { return strings.Contains(href, "/u/") })
	for _, want := range []string{
		`<a href="https://app.test/t/c/1" style="color:#000">shop</a>`,
		`<a href="mailto:pat@example.com">mail</a>`,
		`<a name="top">anchor</a>`,
		`<a href="https://app.test/t/c/3">again</a>`,
		`<a href="https://app.test/u/u1abc">Unsubscribe</a>`,
		`<style>a{color:red}</style>`,
		`<img src="https://app.test/t/o/px" width="1" height="1" alt="" border="0" style="display:block;width:1px;height:1px;border:0;margin:0;padding:0;"></body>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "example.com/a") {
		t.Errorf("the tracked link's target is still in the markup:\n%s", got)
	}
}

func TestTrackWithoutBody(t *testing.T) {
	got := Track(`<p><a href="https://example.com">x</a></p>`, fakeTracker{}, nil)
	if !strings.HasSuffix(got, `<img src="https://app.test/t/o/px" width="1" height="1" alt="" border="0" style="display:block;width:1px;height:1px;border:0;margin:0;padding:0;">`) ||
		!strings.Contains(got, "/t/c/1") {
		t.Errorf("got %s", got)
	}
}

// An html email may write the unsubscribe link with spaces inside the
// quotes and the braces; once filled in, it must still be left untracked.
func TestTrackKeepsSpacedUnsubscribeLink(t *testing.T) {
	src := `<p><a href="https://example.com/menu">Menu</a></p><a href=" {{ unsubscribe_url }} ">Unsubscribe</a>`
	tpl, err := CompileHTML(htmlDoc(t, src, ""), Account{PostalAddress: postal})
	if err != nil {
		t.Fatal(err)
	}
	const unsub = "https://app.test/u/u1abc"
	m := tpl.Personalize(Recipient{Email: "pat@example.com", UnsubscribeURL: unsub})
	got := Track(m.HTML, fakeTracker{}, func(href string) bool { return href == unsub })
	if !strings.Contains(got, `href=" `+unsub+` "`) || !strings.Contains(got, "/t/c/1") || strings.Contains(got, "/t/c/2") {
		t.Errorf("got:\n%s", got)
	}
}
