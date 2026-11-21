package mailchimp

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/emails"
)

func codes(ns []Note) []string {
	var out []string
	for _, n := range ns {
		out = append(out, n.Code)
	}
	return out
}

func TestConvertHTMLMergeTags(t *testing.T) {
	src := `<!doctype html><html><head><title>*|MC:SUBJECT|*</title></head>` +
		`<body mc:edit="body" class="x"><span class="preheader">*|MC_PREVIEW_TEXT|*</span>` +
		`<p>Hi *|FNAME|* *|lname|*, this went to *|EMAIL|*.</p>` +
		`<p>*|LIST:ADDRESSLINE|*</p><p>*|REWARDS|* *|REWARDS|* <a href="*|ARCHIVE|*">View online</a></p>` +
		`<table mc:repeatable="item" mc:hideable><tr><td>x</td></tr></table>` +
		`<a href="*|UNSUB|*">Unsubscribe</a></body></html>`
	c := ConvertHTML(src, "Hi *|FNAME|* & friends", "")
	if c.Subject != "Hi {{first_name}} & friends" {
		t.Errorf("subject %q", c.Subject)
	}
	for _, want := range []string{
		"<title>Hi {{first_name}} &amp; friends</title>",
		"Hi {{first_name}} {{last_name}}, this went to {{email}}.",
		`<a href="{{unsubscribe_url}}">Unsubscribe</a>`,
		`<body class="x">`, `<table><tr>`, `<a href="">View online</a>`, "<p></p>",
	} {
		if !strings.Contains(c.HTML, want) {
			t.Errorf("missing %q in %s", want, c.HTML)
		}
	}
	if strings.Contains(c.HTML, "*|") || strings.Contains(c.HTML, "mc:") {
		t.Errorf("Mailchimp markup left: %s", c.HTML)
	}
	// One warning per distinct tag removed; the address tag goes quietly.
	if got := codes(c.Warnings); !slices.Equal(got, []string{NoteRemovedTag, NoteRemovedTag}) {
		t.Errorf("warnings %v", c.Warnings)
	}
	// Only the tag that was a link's whole address says it left the link empty.
	if strings.Contains(c.Warnings[0].Message, "link was left empty") || !strings.Contains(c.Warnings[1].Message, "link was left empty") {
		t.Errorf("warnings %v", c.Warnings)
	}
	if len(c.Errors) != 0 {
		t.Errorf("errors %v", c.Errors)
	}
	if errs, _ := emails.CheckHTML(c.HTML); len(errs) != 0 {
		t.Errorf("converted HTML fails the checks: %v", errs)
	}
}

func TestConvertHTMLKeepsConditionals(t *testing.T) {
	src := "<html><body>\n<p>Hello</p>\n*|IF:FNAME|*<p>Hi *|FNAME|*</p>*|ELSE:|*<p>Hi there</p>*|END:IF|*\n" +
		`<a href="*|UNSUB|*">Unsubscribe</a></body></html>`
	c := ConvertHTML(src, "", "")
	if len(c.Errors) != 1 || c.Errors[0].Code != NoteConditional || c.Errors[0].Line != 3 {
		t.Fatalf("errors %+v", c.Errors)
	}
	if !strings.Contains(c.HTML, "*|IF:FNAME|*<p>Hi {{first_name}}</p>*|ELSE:|*") {
		t.Errorf("conditional not kept: %s", c.HTML)
	}
	// The email checks stop it being sent as is.
	errs, _ := emails.CheckHTML(c.HTML)
	if len(errs) != 1 || errs[0].Code != emails.HTMLMailchimpTag || errs[0].Line != 3 {
		t.Errorf("checks %+v", errs)
	}
}

func TestConvertHTMLImagesAndTrackingLinks(t *testing.T) {
	img := "https://mcusercontent.com/abc/images/logo.png"
	src := `<html><body style="background:url('https://gallery.mailchimp.com/abc/bg.jpg')">` +
		`<img src="` + img + `"><img src='` + img + `'><img src="https://cdn.example.com/x.png">` +
		`<td background="https://abc.mcusercontent.com/b.gif"></td>` +
		`<a href="https://example.us1.list-manage.com/track/click?u=1&amp;id=2">a</a>` +
		`<a href="https://example.us1.list-manage.com/track/click?u=1&amp;id=3">b</a>` +
		`<a href="https://example.com/">c</a><a href="*|UNSUB|*">u</a></body></html>`
	c := ConvertHTML(src, "", "")
	want := []string{"https://gallery.mailchimp.com/abc/bg.jpg", img, "https://abc.mcusercontent.com/b.gif"}
	if !slices.Equal(c.Images, want) {
		t.Errorf("images %v", c.Images)
	}
	if len(c.Warnings) != 1 || c.Warnings[0].Code != NoteTrackingLink || !strings.HasPrefix(c.Warnings[0].Message, "2 links go") {
		t.Errorf("warnings %+v", c.Warnings)
	}
}

func TestImageHost(t *testing.T) {
	for host, want := range map[string]bool{
		"mcusercontent.com": true, "x.mcusercontent.com": true, "GALLERY.MAILCHIMP.COM.": true,
		"evilmcusercontent.com": false, "mcusercontent.com.evil.example": false, "mailchimp.com": false, "": false,
	} {
		if ImageHost(host) != want {
			t.Errorf("%q: %v", host, !want)
		}
	}
}

func TestNewestCampaigns(t *testing.T) {
	at := func(d int) *time.Time { v := time.Date(2025, 1, d, 0, 0, 0, 0, time.UTC); return &v }
	b := &Bundle{Campaigns: []*Campaign{
		{Name: "a", SentAt: at(2)}, {Name: "b"}, {Name: "c", SentAt: at(9)}, {Name: "d"}, {Name: "e", SentAt: at(5)},
	}}
	names := func(cs []*Campaign) string {
		var s []string
		for _, c := range cs {
			s = append(s, c.Name)
		}
		return strings.Join(s, "")
	}
	if got := names(NewestCampaigns(b, 0)); got != "ceadb" {
		t.Errorf("all: %s", got)
	}
	if got := names(NewestCampaigns(b, 2)); got != "ce" {
		t.Errorf("two: %s", got)
	}
}

func TestPlanEmailsNamesAndSubjects(t *testing.T) {
	long := strings.Repeat("é", 250)
	b := &Bundle{
		Templates: []*HTMLFile{{Name: "  Monthly\nnewsletter ", Path: "templates/m.html",
			HTML: `<html><body><a href="*|UNSUB|*">u</a></body></html>`}},
		Campaigns: []*Campaign{
			{Name: "abc", Path: "c/abc.html", Title: "", Subject: "Sale\r\nnow *|FNAME|*", HTML: "<p>bad \xff</p>"},
			{Name: "", Path: "c/x.html", Subject: long},
		},
	}
	// Campaigns with no send date come newest file first.
	ps := PlanEmails(b, 0)
	if len(ps) != 3 {
		t.Fatalf("%d plans", len(ps))
	}
	if ps[0].From != EmailFromTemplate || ps[0].Name != "Monthly newsletter" || len(ps[0].Errors) != 0 {
		t.Errorf("template %+v", ps[0])
	}
	if ps[2].Name != "Sale now *|FNAME|*" || ps[2].Subject != "Sale now {{first_name}}" {
		t.Errorf("campaign %q %q", ps[2].Name, ps[2].Subject)
	}
	// No unsubscribe link: an error, but the email is still planned.
	if !slices.Contains(codes(ps[2].Errors), NoteInvalidHTML) {
		t.Errorf("errors %+v", ps[2].Errors)
	}
	if n := []rune(ps[1].Name); len(n) != emails.MaxNameChars || len([]rune(ps[1].Subject)) != 200 {
		t.Errorf("long name %d", len(n))
	}
}
