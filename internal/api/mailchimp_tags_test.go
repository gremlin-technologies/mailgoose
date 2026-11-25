package api

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gremlin-technologies/mailgoose/internal/mailchimp"
)

// mergeTagJSON is the API's MailchimpEmailMergeTag.
type mergeTagJSON struct {
	Tag        string  `json:"tag"`
	MergeField *string `json:"merge_field"`
	Field      *struct {
		Key  string `json:"key"`
		Name string `json:"name"`
	} `json:"field"`
	NewField bool `json:"new_field"`
}

type taggedEmailJSON struct {
	Name      string         `json:"name"`
	Subject   string         `json:"subject"`
	EmailID   *string        `json:"email_id"`
	MergeTags []mergeTagJSON `json:"merge_tags"`
	Warnings  []struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"warnings"`
}

// tagsOf sums up an email's merge tags as "tag -> key" lines.
func tagsOf(e taggedEmailJSON) string {
	var out []string
	for _, t := range e.MergeTags {
		s := t.Tag + " -> "
		switch {
		case t.Field == nil:
			s += "removed"
		case t.NewField:
			s += "new " + t.Field.Key
		default:
			s += t.Field.Key
		}
		out = append(out, s)
	}
	return strings.Join(out, ", ")
}

func mergeTagExport(*testing.T) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{
		"audiences/Bakery/subscribed_members_export_x.csv": "Email Address,First Name,Company,Favourite bread\nada@example.com,Ada,Acme,Rye\n",
		"templates/Welcome.html": `<html><body><p>Hi *|FNAME|* from *|COMPANY|*, you like *|FAVOURITE_BREAD|*.*|NICKNAME|*</p>` +
			`<a href="*|UNSUB|*">Unsubscribe</a></body></html>`,
		"campaigns_content/c1.html": `<html><body><p>*|COMPANY|*</p><a href="*|UNSUB|*">u</a></body></html>`,
		"campaigns.csv":             "Unique Id,Title,Subject,Send Date\nc1,Spring,News for *|COMPANY|*,2025-04-01 14:00:00\n",
	} {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(body))
	}
	_ = zw.Close()
	return buf.Bytes()
}

// MG-134: an imported email's merge tags become the contact fields the
// same import makes, in the preview and in the emails made, and the
// preview follows fields made after the upload.
func TestMailchimpMergeTagsThroughTheAPI(t *testing.T) {
	s := draftKey(authServer)(t)
	h := importRunner{s.Handler(), s.Contacts}
	rec := do(t, h, bearer(mailchimpUpload("export.zip", mergeTagExport))(t))
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	type bundle struct {
		ID      string `json:"id"`
		Preview *struct {
			Emails []taggedEmailJSON `json:"emails"`
		} `json:"preview"`
		Emails struct {
			Made []taggedEmailJSON `json:"made"`
		} `json:"emails"`
	}
	var up bundle
	decodeInto(t, rec, &up)
	if len(up.Preview.Emails) != 2 {
		t.Fatalf("emails = %s", rec.Body)
	}
	welcome, spring := up.Preview.Emails[0], up.Preview.Emails[1]
	if got, want := tagsOf(welcome), "*|COMPANY|* -> new company, *|FAVOURITE_BREAD|* -> new favourite_bread, *|NICKNAME|* -> removed"; got != want {
		t.Errorf("welcome = %q, want %q", got, want)
	}
	if spring.Subject != "News for {{company}}" || tagsOf(spring) != "*|COMPANY|* -> new company" {
		t.Errorf("spring = %+v", spring)
	}
	if len(welcome.Warnings) != 1 || !strings.Contains(welcome.Warnings[0].Message, `add a contact field named "Nickname"`) {
		t.Errorf("welcome warnings = %+v", welcome.Warnings)
	}

	// A field made after the upload is matched when the preview is read
	// again: the export is read again for it, and the result kept.
	if rec := do(t, h, bearer(jsonRequest(http.MethodPost, "/v1/contact-fields", `{"name":"Nickname"}`))(t)); rec.Code != http.StatusCreated {
		t.Fatalf("create field: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, h, bearer(request(http.MethodGet, "/v1/imports/mailchimp/bundles/"+up.ID))(t))
	var again bundle
	decodeInto(t, rec, &again)
	if got, want := tagsOf(again.Preview.Emails[0]), "*|COMPANY|* -> new company, *|FAVOURITE_BREAD|* -> new favourite_bread, *|NICKNAME|* -> nickname"; got != want {
		t.Errorf("again = %q, want %q", got, want)
	}
	if len(again.Preview.Emails[0].Warnings) != 0 {
		t.Errorf("again warnings = %+v", again.Preview.Emails[0].Warnings)
	}

	// Confirmed leaving the bread out, the emails take Company and
	// Nickname and lose the bread, with a note saying why.
	rec = do(t, h, bearer(jsonRequest(http.MethodPost, "/v1/imports/mailchimp/bundles/"+up.ID+"/confirm",
		`{"permission_confirmed":true,"new_fields":[{"column":"Company"}]}`))(t))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Body)
	}
	job := &mailchimp.EmailJob{Store: s.Mailchimp, FieldTags: s.Contacts.MailchimpImportFieldTags,
		Importer: &mailchimp.EmailImporter{Emails: s.Emails, Images: s.Images,
			Fetch: func(context.Context, string) ([]byte, error) { return nil, io.ErrUnexpectedEOF }}}
	for {
		ran, err := job.RunNext(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if !ran {
			break
		}
	}
	rec = do(t, h, bearer(request(http.MethodGet, "/v1/imports/mailchimp/bundles/"+up.ID))(t))
	var done bundle
	decodeInto(t, rec, &done)
	if len(done.Emails.Made) != 2 {
		t.Fatalf("made = %s", rec.Body)
	}
	made := done.Emails.Made[0]
	if got, want := tagsOf(made), "*|COMPANY|* -> new company, *|FAVOURITE_BREAD|* -> removed, *|NICKNAME|* -> nickname"; got != want {
		t.Errorf("made = %q, want %q", got, want)
	}
	if len(made.Warnings) != 1 || !strings.Contains(made.Warnings[0].Message, `the merge field "Favourite bread", which this import leaves out`) {
		t.Errorf("made warnings = %+v", made.Warnings)
	}
	rec = do(t, h, bearer(request(http.MethodGet, "/v1/emails/"+*made.EmailID))(t))
	if body := rec.Body.String(); !strings.Contains(body, "Hi {{first_name}} from {{company}}, you like .{{nickname}}") {
		t.Errorf("email = %s", body)
	}
	if done.Emails.Made[1].Subject != "News for {{company}}" {
		t.Errorf("spring = %+v", done.Emails.Made[1])
	}
}
