package mailchimp

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/dbtest"
	"github.com/gremlin-technologies/mailgoose/internal/emails"
	"github.com/gremlin-technologies/mailgoose/internal/images"
)

var pngData = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

func TestImportEmails(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	var acct string
	if err := db.QueryRow(ctx, `INSERT INTO accounts (name) VALUES ('A') RETURNING id`).Scan(&acct); err != nil {
		t.Fatal(err)
	}
	store := &images.Memory{}
	im := &EmailImporter{
		Emails: &emails.Service{DB: db},
		Images: &images.Service{DB: db, Store: store, BaseURL: "https://img.example/"},
	}
	logo := "https://mcusercontent.com/abc/images/logo.png?x=1&amp;y=2"
	var fetched []string
	im.Fetch = func(_ context.Context, u string) ([]byte, error) {
		fetched = append(fetched, u)
		if strings.Contains(u, "gone") {
			return nil, errors.New("Mailchimp answered 404")
		}
		return pngData, nil
	}
	sent := time.Date(2025, 4, 1, 14, 0, 0, 0, time.UTC)
	b := &Bundle{
		Templates: []*HTMLFile{{Name: "Monthly newsletter", Path: "templates/Monthly_newsletter.html",
			HTML: `<html><body><img src="` + logo + `"><img src="https://mcusercontent.com/abc/images/logo.png">` +
				`<a href="*|UNSUB|*">Unsubscribe</a></body></html>`}},
		Campaigns: []*Campaign{
			{Name: "abc123", Path: "campaigns_content/abc123.html", Title: "Spring sale", Subject: "Spring sale: 20% off",
				SentAt: &sent, HTML: `<html><body><img src="` + logo + `"><img src="https://gallery.mailchimp.com/gone.gif">` +
					`<script>x()</script><a href="*|UNSUB|*">Unsubscribe</a></body></html>`},
		},
	}
	got, err := im.Import(ctx, acct, b, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("%d emails", len(got))
	}
	// Each image is fetched once, unescaped, however many emails use it.
	if want := []string{"https://mcusercontent.com/abc/images/logo.png?x=1&y=2", "https://mcusercontent.com/abc/images/logo.png",
		"https://gallery.mailchimp.com/gone.gif"}; !slices.Equal(fetched, want) {
		t.Errorf("fetched %q", fetched)
	}
	if n := len(store.Keys()); n != 2 {
		t.Errorf("%d images stored", n)
	}

	tpl, err := im.Emails.Get(ctx, acct, got[0].EmailID)
	if err != nil {
		t.Fatal(err)
	}
	h := *tpl.Document.HTML
	if tpl.Name != "Monthly newsletter" || tpl.Kind != emails.KindHTML || strings.Contains(h, "mcusercontent") ||
		strings.Count(h, `src="https://img.example/`+acct+"/") != 2 {
		t.Errorf("template %q: %s", tpl.Name, h)
	}
	if len(got[0].Errors) != 0 || len(got[0].Warnings) != 0 {
		t.Errorf("template notes %+v %+v", got[0].Errors, got[0].Warnings)
	}

	camp, err := im.Emails.Get(ctx, acct, got[1].EmailID)
	if err != nil {
		t.Fatal(err)
	}
	if camp.Name != "Spring sale" || camp.Document.Settings.Subject != "Spring sale: 20% off" {
		t.Errorf("campaign %q %q", camp.Name, camp.Document.Settings.Subject)
	}
	// The script is an error and the missing image a warning; it's made anyway,
	// with the image still on Mailchimp.
	if !strings.Contains(*camp.Document.HTML, "https://gallery.mailchimp.com/gone.gif") ||
		!slices.Contains(codes(got[1].Errors), NoteInvalidHTML) ||
		!slices.Equal(codes(got[1].Warnings), []string{NoteImageNotCopied}) ||
		!strings.Contains(got[1].Warnings[0].Message, "404") {
		t.Errorf("campaign notes %+v %+v", got[1].Errors, got[1].Warnings)
	}
}

func TestImportEmailsTooLarge(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	var acct string
	if err := db.QueryRow(ctx, `INSERT INTO accounts (name) VALUES ('A') RETURNING id`).Scan(&acct); err != nil {
		t.Fatal(err)
	}
	im := &EmailImporter{Emails: &emails.Service{DB: db}, Images: &images.Service{DB: db, Store: &images.Memory{}}}
	b := &Bundle{Templates: []*HTMLFile{{Name: "Big", HTML: "<p>" + strings.Repeat("x", emails.MaxHTMLBytes) + "</p>"}}}
	got, err := im.Import(ctx, acct, b, 0)
	if err != nil {
		t.Fatal(err)
	}
	e, err := im.Emails.Get(ctx, acct, got[0].EmailID)
	if err != nil {
		t.Fatal(err)
	}
	if *e.Document.HTML != "" || !slices.Equal(codes(got[0].Errors), []string{NoteTooLarge}) {
		t.Errorf("%d bytes kept, errors %+v", len(*e.Document.HTML), got[0].Errors)
	}
}

func TestFetchImageRefusesOtherHosts(t *testing.T) {
	for _, u := range []string{
		"http://mcusercontent.com/a.png", "https://example.com/a.png", "https://mcusercontent.com:8443/a.png",
		"https://user@evil.example/a.png", "not a url\x7f",
	} {
		if _, err := FetchImage(context.Background(), u); err == nil {
			t.Errorf("%s fetched", u)
		}
	}
}

func importerWithAccount(t *testing.T) (*EmailImporter, string, *int) {
	t.Helper()
	db := dbtest.New(t)
	var acct string
	if err := db.QueryRow(t.Context(), `INSERT INTO accounts (name) VALUES ('A') RETURNING id`).Scan(&acct); err != nil {
		t.Fatal(err)
	}
	fetches := 0
	return &EmailImporter{
		Emails: &emails.Service{DB: db},
		Images: &images.Service{DB: db, Store: &images.Memory{}, BaseURL: "https://img.example/"},
		Fetch: func(context.Context, string) ([]byte, error) {
			fetches++
			return pngData, nil
		},
	}, acct, &fetches
}

func imageTags(prefix string, n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, `<img src="https://x.mcusercontent.com/%s/%d.png">`, prefix, i)
	}
	return b.String()
}

func TestImportEmailsLimitsImageFetches(t *testing.T) {
	im, acct, fetches := importerWithAccount(t)
	// An email too large to keep fetches none of its images.
	big := "<p>" + strings.Repeat("x", emails.MaxHTMLBytes) + "</p>" + imageTags("big", 50)
	b := &Bundle{Templates: []*HTMLFile{
		{Name: "A big one", Path: "templates/a.html", HTML: big},
		{Name: "Many images", Path: "templates/b.html", HTML: "<p>Hi</p>" + imageTags("many", MaxImportImages+20)},
	}}
	if plans := PlanEmails(b, 0); plans[0].Images != 0 || plans[1].Images != MaxImportImages+20 {
		t.Errorf("planned images %d, %d", plans[0].Images, plans[1].Images)
	}
	got, err := im.Import(t.Context(), acct, b, 0)
	if err != nil {
		t.Fatal(err)
	}
	if *fetches != MaxImportImages {
		t.Errorf("%d fetches, want %d", *fetches, MaxImportImages)
	}
	if slices.Contains(codes(got[0].Warnings), NoteImageNotCopied) || !slices.Contains(codes(got[0].Errors), NoteTooLarge) {
		t.Errorf("big email notes %+v %+v", got[0].Errors, got[0].Warnings)
	}
	// The 20 past the cap get ten notes of their own and one counting the rest.
	w := got[1].Warnings
	if len(w) != maxImageNotes+1 || !slices.Equal(slices.Compact(codes(w)), []string{NoteImageNotCopied}) ||
		!strings.Contains(w[0].Message, "at most 1,000 images") || !strings.Contains(w[maxImageNotes].Message, "10 more") {
		t.Errorf("warnings %+v", w)
	}
}

func TestImportEmailsStopsWhenTheAccountIsFull(t *testing.T) {
	im, acct, fetches := importerWithAccount(t)
	if _, err := im.Images.DB.Exec(t.Context(), `INSERT INTO images (account_id, key, content_type, bytes)
		SELECT $1, 'k' || i, 'image/png', 1 FROM generate_series(1, $2) i`, acct, images.MaxPerAccount); err != nil {
		t.Fatal(err)
	}
	b := &Bundle{Templates: []*HTMLFile{{Name: "Three", Path: "templates/a.html", HTML: "<p>Hi</p>" + imageTags("t", 3)}}}
	got, err := im.Import(t.Context(), acct, b, 0)
	if err != nil {
		t.Fatal(err)
	}
	if *fetches != 1 || len(got[0].Warnings) != 3 || !strings.Contains(got[0].Warnings[2].Message, "as many images as it can keep") {
		t.Errorf("%d fetches; warnings %+v", *fetches, got[0].Warnings)
	}

	// With no image storage, nothing is fetched.
	im.Images = nil
	*fetches = 0
	got, err = im.Import(t.Context(), acct, b, 0)
	if err != nil {
		t.Fatal(err)
	}
	if *fetches != 0 || len(got[0].Warnings) != 3 || !strings.Contains(got[0].Warnings[0].Message, "image storage isn't set up") {
		t.Errorf("%d fetches; warnings %+v", *fetches, got[0].Warnings)
	}
}

func TestPlanEmailsCapsTemplates(t *testing.T) {
	b := &Bundle{}
	for i := range MaxTemplates + 5 {
		b.Templates = append(b.Templates, &HTMLFile{Name: fmt.Sprint("t", i), Path: fmt.Sprintf("templates/%03d.html", i), HTML: "<p>Hi</p>"})
	}
	if n := len(PlanEmails(b, 0)); n != MaxTemplates {
		t.Errorf("%d planned", n)
	}
	if left := TemplatesLeftOut(b); !slices.Equal(left, []string{"templates/100.html", "templates/101.html",
		"templates/102.html", "templates/103.html", "templates/104.html"}) {
		t.Errorf("left out %q", left)
	}
	if left := TemplatesLeftOut(&Bundle{Templates: b.Templates[:3]}); left != nil {
		t.Errorf("left out %q", left)
	}
}
