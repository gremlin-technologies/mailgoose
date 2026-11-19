package emailcheck

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/gremlin-technologies/mailgoose/internal/emails"
)

func blocksDoc(t *testing.T, settings, blocks string) *emails.Document {
	t.Helper()
	d, err := emails.ParseDocument(emails.KindBlocks, []byte(`{"settings":`+settings+`,"blocks":[`+blocks+`]}`))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func htmlDoc(t *testing.T, settings, src string) *emails.Document {
	t.Helper()
	raw, _ := json.Marshal(src)
	d, err := emails.ParseDocument(emails.KindHTML, []byte(`{"settings":`+settings+`,"html":`+string(raw)+`}`))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// fakeLinks answers from a table; anything not in it is fine.
type fakeLinks struct {
	answers map[string]LinkStatus
	asked   []string
}

func (f *fakeLinks) CheckLinks(_ context.Context, urls []string) map[string]LinkStatus {
	f.asked = append(f.asked, urls...)
	out := map[string]LinkStatus{}
	for _, u := range urls {
		st, ok := f.answers[u]
		if !ok {
			st = LinkStatus{Result: ResultOK}
		}
		out[u] = st
	}
	return out
}

func codes(issues []Issue) string {
	var out []string
	for _, i := range issues {
		where := i.Path
		if i.Line > 0 {
			where = "line" + string(rune('0'+i.Line))
		}
		if i.URL != "" {
			where = i.URL
		}
		out = append(out, i.Code+"@"+where)
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

var good = Input{PostalAddress: "1 Main St\nSpringfield", FromEmail: "news@bakery.example", FromVerified: true}

func TestCleanBlocksEmailPasses(t *testing.T) {
	in := good
	in.Doc = blocksDoc(t, `{"subject":"Spring menu"}`,
		`{"type":"heading","text":"Hi {{first_name|there}}"},`+
			`{"type":"image","src":"https://cdn.example.com/a.png","alt":"Bread","href":"https://bakery.example/menu"},`+
			`{"type":"button","text":"Order","href":"https://bakery.example/order"},`+
			`{"type":"footer","text":"You signed up at the bakery."}`)
	links := &fakeLinks{}
	res := Check(context.Background(), in, links)
	if !res.OK() || len(res.Warnings) != 0 {
		t.Errorf("errors %s; warnings %s", codes(res.Errors), codes(res.Warnings))
	}
	sort.Strings(links.asked)
	if strings.Join(links.asked, " ") != "https://bakery.example/menu https://bakery.example/order" {
		t.Errorf("checked %v; the unsubscribe link must be skipped", links.asked)
	}
}

func TestBlocksEmailProblems(t *testing.T) {
	in := Input{FromEmail: "news@unverified.example"}
	in.Doc = blocksDoc(t, `{"subject":" "}`,
		`{"type":"heading","text":"Hi {{first}}"},`+
			`{"type":"text","text":"See [the menu](/menu) and [old](http://old.bakery.example)."},`+
			`{"type":"image","src":"https://cdn.example.com/a.png","alt":" "},`+
			`{"type":"button","text":"Order","href":"https://bakery.example/gone"},`+
			`{"type":"button","text":"Hours","href":"https://slow.example/hours"},`+
			`{"type":"footer","text":""}`)
	links := &fakeLinks{answers: map[string]LinkStatus{
		"https://bakery.example/gone": {Result: ResultMissing, Detail: "the page answers 404 Not Found"},
		"https://slow.example/hours":  {Result: ResultUnreachable, Detail: "it timed out"},
	}}
	res := Check(context.Background(), in, links)
	wantErrs := "broken_link@blocks[1].text broken_link@https://bakery.example/gone merge_tag@blocks[0].text " +
		"missing_postal_address@ missing_subject@settings.subject unverified_from@settings.from_email"
	if got := codes(res.Errors); got != wantErrs {
		t.Errorf("errors\n got %s\nwant %s", got, wantErrs)
	}
	wantWarns := "insecure_link@http://old.bakery.example link_unreachable@https://slow.example/hours missing_alt@blocks[2].alt"
	if got := codes(res.Warnings); got != wantWarns {
		t.Errorf("warnings\n got %s\nwant %s", got, wantWarns)
	}
	for _, i := range res.Errors {
		if i.Code == BrokenLink && i.URL != "" && i.Message != "The link https://bakery.example/gone is broken: the page answers 404 Not Found." {
			t.Errorf("message %q", i.Message)
		}
	}
}

func TestEmptyAndMissingFrom(t *testing.T) {
	in := good
	in.FromEmail = ""
	in.Doc = blocksDoc(t, `{"subject":"x"}`, `{"type":"footer","text":""}`)
	if got := codes(Check(context.Background(), in, nil).Errors); got != "empty_email@blocks missing_from@settings.from_email" {
		t.Errorf("blocks: %s", got)
	}
	in = good
	in.Doc = htmlDoc(t, `{"subject":"x"}`, "")
	if got := codes(Check(context.Background(), in, nil).Errors); got != "empty_email@html" {
		t.Errorf("html: %s", got)
	}
}

func TestHTMLEmail(t *testing.T) {
	in := good
	src := "<html><body>\n<style>.a{position:absolute}</style>\n<img src=\"https://cdn.example.com/a.png\">\n" +
		"<p>Hi {{full_name}}</p><a href=\"#top\">Top</a> <a href=\"mailto:hi@bakery.example\">Mail</a>\n" +
		"<a href=\"ftp://files.bakery.example/x\">Files</a> <a href=\"https://bakery.example\">Site</a>\n" +
		"<a href=\"{{unsubscribe_url}}\">Unsubscribe</a></body></html>"
	in.Doc = htmlDoc(t, `{"subject":"Hi"}`, src)
	links := &fakeLinks{}
	res := Check(context.Background(), in, links)
	if got := codes(res.Errors); got != "broken_link@ftp://files.bakery.example/x merge_tag@html" {
		t.Errorf("errors %s", got)
	}
	if got := codes(res.Warnings); got != "missing_alt@line3 unsupported_css@line2" {
		t.Errorf("warnings %s", got)
	}
	if strings.Join(links.asked, " ") != "https://bakery.example" {
		t.Errorf("checked %v", links.asked)
	}
}

func TestHTMLWithoutUnsubscribe(t *testing.T) {
	// Stored markup always has one, since import checks for it; the check
	// still looks. Only a real link counts.
	in := good
	for name, src := range map[string]string{
		"none":       "<p>Hi</p>",
		"commented":  `<p>Hi</p><!-- <a href="{{unsubscribe_url}}">Unsubscribe</a> -->`,
		"plain text": `<p>Unsubscribe: {{unsubscribe_url}}</p>`,
	} {
		in.Doc = htmlDoc(t, `{"subject":"Hi"}`, src)
		if got := codes(Check(context.Background(), in, nil).Errors); got != "missing_unsubscribe@line1" {
			t.Errorf("%s: errors %s", name, got)
		}
	}
}

func TestSize(t *testing.T) {
	in := good
	filler := strings.Repeat("<p>Fresh bread every morning.</p>\n", 2900) // about 100 KB
	in.Doc = htmlDoc(t, `{"subject":"Hi"}`, filler+`<a href="{{unsubscribe_url}}">Unsubscribe</a>`)
	res := Check(context.Background(), in, nil)
	if codes(res.Warnings) != "near_size_limit@" || !res.OK() {
		t.Errorf("near: errors %s, warnings %s", codes(res.Errors), codes(res.Warnings))
	}
	in.Doc = htmlDoc(t, `{"subject":"Hi"}`, filler+filler[:10000]+`<a href="{{unsubscribe_url}}">Unsubscribe</a>`)
	if got := codes(Check(context.Background(), in, nil).Errors); got != "too_large@" {
		t.Errorf("over: %s", got)
	}
}

func TestResultJSONHasEmptyLists(t *testing.T) {
	in := good
	in.Doc = blocksDoc(t, `{"subject":"x"}`, `{"type":"text","text":"Hi"},{"type":"footer","text":""}`)
	b, _ := json.Marshal(Check(context.Background(), in, nil))
	if string(b) != `{"errors":[],"warnings":[]}` {
		t.Errorf("%s", b)
	}
}
