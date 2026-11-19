package emails

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The files in testdata/html are the corpus: each starts with a comment
// listing the errors it should give ("want:") and, optionally, the
// warnings ("warn:"), as code:line.
var expectLine = regexp.MustCompile(`<!-- (want|warn):([^>]*)-->`)

func TestHTMLCorpus(t *testing.T) {
	files, err := filepath.Glob("testdata/html/*.html")
	if err != nil || len(files) == 0 {
		t.Fatalf("no corpus: %v", err)
	}
	seen := map[string]bool{}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			src := string(raw)
			want := map[string][]string{"want": nil, "warn": nil}
			for _, m := range expectLine.FindAllStringSubmatch(src, 2) {
				want[m[1]] = strings.Fields(m[2])
			}
			errs, warns := CheckHTML(src)
			got := map[string][]string{"want": codes(errs), "warn": codes(warns)}
			for _, k := range []string{"want", "warn"} {
				slices.Sort(want[k])
				if !slices.Equal(got[k], want[k]) {
					t.Errorf("%s: got %v, want %v\n%v", k, got[k], want[k], append(errs, warns...))
				}
			}
			for _, i := range append(errs, warns...) {
				seen[i.Code] = true
				if i.Message == "" {
					t.Errorf("%s has no message", i.Code)
				}
			}
		})
	}
	// Every rule has a file that breaks it.
	for _, code := range []string{HTMLMalformed, HTMLScript, HTMLForm, HTMLFrame, HTMLEmbed, HTMLExternalStylesheet,
		HTMLEventHandler, HTMLScriptURL, HTMLDataURL, HTMLRedirect, HTMLNoUnsubscribe, HTMLUnsupportedCSS} {
		if !seen[code] {
			t.Errorf("no corpus file gives %s", code)
		}
	}
}

func codes(issues []HTMLIssue) []string {
	var out []string
	for _, i := range issues {
		out = append(out, i.Code+":"+strconv.Itoa(i.Line))
	}
	slices.Sort(out)
	return out
}

// TestHTMLTooLarge builds its file, since a 102 KB file in the corpus would
// be mostly padding.
func TestHTMLTooLarge(t *testing.T) {
	line := `<p>Some words to fill the email with, and a few more of them.</p>` + "\n"
	body := strings.Repeat(line, MaxSendableHTMLBytes/len(line)+5)
	src := `<a href="{{unsubscribe_url}}">Unsubscribe</a>` + "\n" + body
	errs, _ := CheckHTML(src)
	if len(errs) != 1 || errs[0].Code != HTMLTooLarge || errs[0].Line != 1+strings.Count(src[:MaxSendableHTMLBytes], "\n") {
		t.Errorf("%+v", errs)
	}
	if !strings.Contains(errs[0].Message, "102.0 KB") {
		t.Errorf("message %q", errs[0].Message)
	}
	if errs, _ := CheckHTML(src[:MaxSendableHTMLBytes]); slices.ContainsFunc(errs, func(i HTMLIssue) bool { return i.Code == HTMLTooLarge }) {
		t.Error("exactly the limit is fine")
	}
}

func TestHTMLErrorMessage(t *testing.T) {
	e := &HTMLError{Issues: []HTMLIssue{{Line: 4, Code: HTMLScript, Message: "no scripts"}, {Line: 9, Code: HTMLForm, Message: "no forms"}}}
	if e.Error() != "line 4: no scripts (and 1 more problem)" {
		t.Errorf("%q", e.Error())
	}
}
