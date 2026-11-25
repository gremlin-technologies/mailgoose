package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testBundleID = "5e1f0a2b-3c4d-4e5f-8a9b-0c1d2e3f4a5b"

const testBundlePreview = `{"id":"` + testBundleID + `","file_name":"export.zip","bytes":4412,"status":"unconfirmed",
"created_at":"2026-11-21T12:00:00Z","expires_at":"2026-11-28T12:00:00Z","confirmed_at":null,"import_id":null,"campaigns":10,
"emails":{"status":"none","total":0,"made":[],"error":null},
"preview":{"source":"account_export","campaigns_available":12,
"audiences":[{"name":"Newsletter","members":5,"by_status":{"subscribed":2,"unsubscribed":1,"cleaned":1,"pending":0,"transactional":1,"archived":0},"groups":["Interests: Baking"],"double_optin":false}],
"contacts":{"new":{"subscribed":2,"unconfirmed":0,"unsubscribed":1,"bounced":1},"existing_tagged":3,"existing_suppressed":1,
"skipped":{"invalid_email":1,"invalid_row":0,"suppressed":0,"not_subscribed":1,"archived":0},"tags_to_create":["audience: Newsletter","VIP"],
"renamed_tags":[{"from":"Smith, Jones","to":"Smith Jones"}],"merge_suppressed":0,"dropped_fields":["MEMBER_RATING"],"holds":["over_contact_limit"],"waits":true,
"merge_fields":[{"name":"Phone Number","audiences":["Newsletter"],"members":4,"samples":["555-0100"],"custom_field":null,"new_field":{"name":"Phone Number","key":"phone_number"},"skip_reason":null},
{"name":"Company","audiences":["Newsletter"],"members":2,"samples":["Acme"],"custom_field":{"id":"f1","key":"company","name":"Company"},"new_field":null,"skip_reason":null},
{"name":"Pet","audiences":["Newsletter"],"members":1,"samples":["Rex"],"custom_field":null,"new_field":null,"skip_reason":"field_limit"}],
"fields":{"limit":20,"existing":19,"new":1,"left_out":1},
"plan":{"plan":"paid","upgrade":true,"contacts":3000,"limit":500,"month_cents":1000,"year_cents":10000,"tester":false,"units":2}},
"emails":[{"from":"template","file":"templates/a.html","name":"Monthly","subject":"","images":0,"errors":[],"warnings":[],"email_id":null,
"merge_tags":[{"tag":"*|MMERGE3|*","merge_field":"Company","field":{"key":"company","name":"Company"},"new_field":false},
{"tag":"*|PET|*","merge_field":"Pet","field":null,"new_field":false},{"tag":"*|FAX|*","merge_field":null,"field":null,"new_field":false}]},
{"from":"campaign","file":"campaigns_content/b.html","name":"Spring sale","subject":"Spring","images":2,"errors":[{"code":"mailchimp_conditional","message":"x","line":3}],"warnings":[],"email_id":null}],
"ignored_files":[{"path":"events.csv","reason":"unknown_file"}]}}`

func testBundleConfirmed(status string, made int) string {
	m := []string{}
	for i := 0; i < made; i++ {
		m = append(m, `{"from":"campaign","file":"c.html","name":"Spring sale","subject":"Spring","images":0,"errors":[],"warnings":[],"email_id":"e`+itoa(i)+`",
"merge_tags":[{"tag":"*|COMPANY|*","merge_field":null,"field":{"key":"company","name":"Company"},"new_field":true}]}`)
	}
	return `{"id":"` + testBundleID + `","file_name":"export.zip","bytes":4412,"status":"confirmed","created_at":"2026-11-21T12:00:00Z",
"expires_at":"2026-11-28T12:00:00Z","confirmed_at":"2026-11-21T12:01:00Z","import_id":"` + testImportID + `","campaigns":3,"preview":null,
"emails":{"status":"` + status + `","total":2,"made":[` + strings.Join(m, ",") + `],"error":null},
"forms":[{"id":"9a8b7c6d-1111-4c55-9a0e-2f9b8b6f4a10","name":"Newsletter","audience":"Newsletter","new":true},
{"id":"9a8b7c6d-2222-4c55-9a0e-2f9b8b6f4a10","name":"Recipe Club","audience":"Recipe Club","new":false}]}`
}

type seenUpload struct {
	fileName, file, audience string
	groups                   []string
}

func mailchimpStub(t *testing.T) (*harness, *[]seenRequest, *seenUpload) {
	t.Helper()
	var seen []seenRequest
	up := &seenUpload{}
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := seenRequest{method: r.Method, path: r.URL.Path}
		w.Header().Set("Content-Type", "application/json")
		one := "/v1/imports/mailchimp/bundles/" + testBundleID
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/imports/mailchimp/bundles":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Errorf("multipart: %v", err)
			}
			f, fh, err := r.FormFile("file")
			if err == nil {
				b, _ := io.ReadAll(f)
				up.file, up.fileName = string(b), fh.Filename
			}
			up.audience, up.groups = r.FormValue("audience_name"), r.MultipartForm.Value["group_columns"]
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(testBundlePreview))
		case r.Method == http.MethodPost && r.URL.Path == one+"/confirm":
			if b, _ := io.ReadAll(r.Body); len(b) > 0 {
				_ = json.Unmarshal(b, &req.body)
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(testBundleConfirmed("queued", 0)))
		case r.Method == http.MethodDelete && r.URL.Path == one:
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == one:
			polls++
			if polls == 1 {
				_, _ = w.Write([]byte(testBundleConfirmed("running", 1)))
				break
			}
			_, _ = w.Write([]byte(testBundleConfirmed("done", 2)))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"No uploaded Mailchimp export with that id in this account.","hint":""}}`))
		}
		seen = append(seen, req)
	}))
	t.Cleanup(srv.Close)
	h := newHarness(t, "")
	h.app.Sleep = func(context.Context, time.Duration) error { return nil }
	h.env[EnvAPIURL] = srv.URL
	h.env[EnvAPIKey] = "mg_test_fullkey"
	return h, &seen, up
}

func TestImportMailchimpUploadShowsThePreview(t *testing.T) {
	h, seen, up := mailchimpStub(t)
	path := filepath.Join(t.TempDir(), "export.zip")
	if err := os.WriteFile(path, []byte("PK zip bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := h.run("import", "mailchimp", "upload", path, "--audience-name", "News", "--group-column", "Interests", "--group-column", "Region"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if len(*seen) != 1 || up.fileName != "export.zip" || up.file != "PK zip bytes" || up.audience != "News" || strings.Join(up.groups, ",") != "Interests,Region" {
		t.Errorf("upload = %+v after %d requests", up, len(*seen))
	}
	out := h.out.String()
	for _, want := range []string{"Nothing is imported yet", `Audience "Newsletter": 5 members (2 subscribed, 1 unsubscribed, 1 cleaned, 1 transactional)`,
		"New contacts: 2 subscribed, 0 unconfirmed, 1 unsubscribed, 1 bounced.", "3 gain tags and field values; 1 come off the list",
		"Mailchimp's own columns mailgoose doesn't keep: MEMBER_RATING.", "new field phone_number", "field company",
		"skipped: no room under the plan's field limit", "The plan allows 20 contact fields; the account has 19, and this would make 1.",
		"1 merge fields are skipped for the limit", "--new-field",
		"Tags to create: audience: Newsletter, VIP.", `Tag "Smith, Jones" becomes "Smith Jones".`, "needs the paid plan, $10.00 a month",
		"wait for the paid plan", "Held for review: it would take the account past its contact limit",
		"1 templates and the newest 1 of 12 campaigns", "Spring sale", "1 files in the export",
		"Groups: Interests: Baking.", "double opt-in was off in Mailchimp; mailgoose's form has it on",
		"Each audience gets a draft signup form", `--form-tag "<group>"`,
		"mailgoose import mailchimp confirm " + testBundleID + " --permission-confirmed", "--campaigns N",
		"*|MMERGE3|*  {{company}} (field Company)", "*|PET|*      taken out: merge field Pet isn't imported",
		"*|FAX|*      taken out: no contact field matches it", "mailgoose fields create <name>"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	if code := h.run("import", "mailchimp", "upload", path, "--json"); code != ExitOK {
		t.Fatalf("json exit %d: %s", code, h.errOut)
	}
	var got map[string]any
	if err := json.Unmarshal(h.out.Bytes(), &got); err != nil || got["id"] != testBundleID || got["preview"] == nil {
		t.Errorf("json = %s (%v)", h.out, err)
	}
}

func TestImportMailchimpUploadRefusesAMissingFile(t *testing.T) {
	h, seen, _ := mailchimpStub(t)
	if code := h.run("import", "mailchimp", "upload", filepath.Join(t.TempDir(), "none.zip")); code != ExitUsage {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if len(*seen) != 0 {
		t.Errorf("%d requests", len(*seen))
	}
}

func TestImportMailchimpConfirmNeedsPermission(t *testing.T) {
	h, seen, _ := mailchimpStub(t)
	if code := h.run("import", "mailchimp", "confirm", testBundleID); code != ExitUsage {
		t.Fatalf("exit %d", code)
	}
	if len(*seen) != 0 || !strings.Contains(h.errOut.String(), "permission to email them") {
		t.Errorf("%d requests; stderr %q", len(*seen), h.errOut)
	}
}

func TestImportMailchimpConfirmWaitsForTheEmails(t *testing.T) {
	h, seen, _ := mailchimpStub(t)
	if code := h.run("import", "mailchimp", "confirm", testBundleID, "--permission-confirmed", "--campaigns", "3", "--wait",
		"--form-tag", "Interests: Baking", "--form-tag", "Region: North"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if len(*seen) != 3 || (*seen)[0].body["permission_confirmed"] != true || (*seen)[0].body["campaigns"] != float64(3) ||
		fmt.Sprint((*seen)[0].body["form_tags"]) != "[Interests: Baking Region: North]" {
		t.Errorf("requests = %+v", *seen)
	}
	out := h.out.String()
	for _, want := range []string{"is confirmed", "mailgoose import status " + testImportID, "Emails: all 2 made", "e1",
		"*|COMPANY|*  {{company}} (new field Company)  2", "has no fallback",
		"9a8b7c6d-1111-4c55-9a0e-2f9b8b6f4a10  Newsletter (draft)", "Recipe Club (made by an earlier import)",
		"form update <form-id> --live"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestImportMailchimpConfirmChoosesMergeFields(t *testing.T) {
	h, seen, _ := mailchimpStub(t)
	if code := h.run("import", "mailchimp", "confirm", testBundleID, "--permission-confirmed",
		"--field", "company=", "--field", "notes=Company", "--new-field", "Phone Number=Phone", "--new-field", "Pet"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	body := (*seen)[0].body
	if fmt.Sprint(body["field_columns"]) != "map[company: notes:Company]" ||
		fmt.Sprint(body["new_fields"]) != "[map[column:Phone Number name:Phone] map[column:Pet]]" {
		t.Errorf("body = %+v", body)
	}

	h, seen, _ = mailchimpStub(t)
	if code := h.run("import", "mailchimp", "confirm", testBundleID, "--permission-confirmed", "--no-new-fields"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if news, ok := (*seen)[0].body["new_fields"].([]any); !ok || len(news) != 0 {
		t.Errorf("--no-new-fields sent new_fields %v", (*seen)[0].body["new_fields"])
	}

	h, seen, _ = mailchimpStub(t)
	for _, args := range [][]string{{"--field", "company"}, {"--new-field", "=Phone"}, {"--no-new-fields", "--new-field", "Pet"}} {
		if code := h.run(append([]string{"import", "mailchimp", "confirm", testBundleID, "--permission-confirmed"}, args...)...); code != ExitUsage {
			t.Errorf("%q: exit %d", args, code)
		}
	}
	if len(*seen) != 0 {
		t.Errorf("a refused confirm sent %d requests", len(*seen))
	}
}

func TestImportMailchimpPreviewOfAnUnknownUpload(t *testing.T) {
	h, _, _ := mailchimpStub(t)
	if code := h.run("import", "mailchimp", "preview", "00000000-0000-4000-8000-000000000000"); code == ExitOK {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(h.errOut.String(), "No uploaded Mailchimp export") {
		t.Errorf("stderr %q", h.errOut)
	}
}

func TestImportCancelAndTheWaitingNote(t *testing.T) {
	var seen []seenRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, seenRequest{method: r.Method, path: r.URL.Path})
		w.Header().Set("Content-Type", "application/json")
		one := "/v1/imports/" + testImportID
		switch {
		case r.Method == http.MethodGet && r.URL.Path == one:
			j := strings.Replace(testImport("waiting", 1, 0, 0), `"error":null`, `"waiting_note":"A tester account keeps at most 500 contacts, so this import won't carry on.","error":null`, 1)
			_, _ = w.Write([]byte(j))
		case r.Method == http.MethodPost && r.URL.Path == one+"/cancel":
			_, _ = w.Write([]byte(testImport("cancelled", 1, 0, 0)))
		default:
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":{"code":"import_not_waiting","message":"Only an import that's waiting for the paid plan can be cancelled.","hint":""}}`))
		}
	}))
	t.Cleanup(srv.Close)
	h := newHarness(t, "")
	h.env[EnvAPIURL] = srv.URL
	h.env[EnvAPIKey] = "mg_test_fullkey"

	if code := h.run("import", "status", testImportID); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	for _, want := range []string{"waiting for the paid plan", "won't carry on", "mailgoose import cancel " + testImportID} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("status lacks %q:\n%s", want, h.out)
		}
	}
	if code := h.run("import", "cancel", testImportID); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if last := seen[len(seen)-1]; last.method != http.MethodPost || last.path != "/v1/imports/"+testImportID+"/cancel" ||
		!strings.Contains(h.out.String(), "was cancelled") {
		t.Errorf("last request %+v; out %s", last, h.out)
	}
	if code := h.run("import", "cancel", "00000000-0000-4000-8000-000000000000"); code == ExitOK ||
		!strings.Contains(h.errOut.String(), "waiting for the paid plan") {
		t.Errorf("exit %d: %s", code, h.errOut)
	}
}

func TestImportMailchimpDelete(t *testing.T) {
	h, seen, _ := mailchimpStub(t)
	if code := h.run("import", "mailchimp", "delete", testBundleID); code != ExitUsage || !strings.Contains(h.errOut.String(), "--yes") {
		t.Fatalf("without --yes: exit %d, %s", code, h.errOut)
	}
	if len(*seen) != 0 {
		t.Fatalf("requests without --yes: %v", *seen)
	}
	if code := h.run("import", "mailchimp", "delete", testBundleID, "--yes"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if !strings.Contains(h.out.String(), "Deleted upload "+testBundleID) || len(*seen) != 1 || (*seen)[0].method != http.MethodDelete {
		t.Errorf("out %q, seen %v", h.out, *seen)
	}
}

func TestImportMailchimpConnect(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/imports/mailchimp/connect":
			_, _ = w.Write([]byte(`{"authorize_url":"https://login.mailchimp.com/oauth2/authorize?client_id=1&state=abc","expires_at":"2026-11-21T12:10:00Z"}`))
		case "/v1/imports/mailchimp/connection":
			_, _ = w.Write([]byte(`{"connected":true,"account_name":"Riverside\u001b[31m Bakery","data_center":"us21","connected_at":"2026-11-21T12:02:00Z","expires_at":"2026-11-22T12:02:00Z"}`))
		case "/v1/imports/mailchimp/disconnect":
			_, _ = w.Write([]byte(`{"connected":false,"account_name":null,"data_center":null,"connected_at":null,"expires_at":null}`))
		}
	}))
	t.Cleanup(srv.Close)
	h := newHarness(t, "")
	h.env[EnvAPIURL] = srv.URL
	h.env[EnvAPIKey] = "mg_test_fullkey"

	if code := h.run("import", "mailchimp", "connect"); code != ExitOK {
		t.Fatalf("connect exit %d: %s", code, h.errOut)
	}
	if out := h.out.String(); !strings.Contains(out, "https://login.mailchimp.com/oauth2/authorize?client_id=1&state=abc") ||
		!strings.Contains(out, "signed in to mailgoose") {
		t.Errorf("connect output:\n%s", out)
	}
	if code := h.run("import", "mailchimp", "connect", "--json"); code != ExitOK {
		t.Fatalf("connect --json exit %d", code)
	}
	var got map[string]any
	if err := json.Unmarshal(h.out.Bytes(), &got); err != nil || got["authorize_url"] == nil {
		t.Errorf("json = %s (%v)", h.out, err)
	}
	if code := h.run("import", "mailchimp", "connection"); code != ExitOK {
		t.Fatalf("connection exit %d", code)
	}
	if out := h.out.String(); !strings.Contains(out, `"Riverside[31m Bakery" (us21) is connected`) || strings.Contains(out, "\x1b") {
		t.Errorf("connection output: %q", out)
	}
	if code := h.run("import", "mailchimp", "disconnect"); code != ExitOK {
		t.Fatalf("disconnect exit %d", code)
	}
	if out := h.out.String(); !strings.Contains(out, "No Mailchimp account is connected") {
		t.Errorf("disconnect output:\n%s", out)
	}
	want := []string{"POST /v1/imports/mailchimp/connect", "POST /v1/imports/mailchimp/connect",
		"GET /v1/imports/mailchimp/connection", "POST /v1/imports/mailchimp/disconnect"}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Errorf("requests = %v", seen)
	}
}

func TestImportMailchimpConnectNotSetUp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"code":"mailchimp_not_configured","message":"Connecting a Mailchimp account isn't set up here.","hint":"Upload Mailchimp's export instead: ` + "`mailgoose import mailchimp upload <file>`" + `."}}`))
	}))
	t.Cleanup(srv.Close)
	h := newHarness(t, "")
	h.env[EnvAPIURL] = srv.URL
	h.env[EnvAPIKey] = "mg_test_fullkey"
	if code := h.run("import", "mailchimp", "connect"); code == ExitOK {
		t.Fatal("connect succeeded")
	}
	if e := h.errOut.String(); !strings.Contains(e, "isn't set up here") || !strings.Contains(e, "mailgoose import mailchimp upload") {
		t.Errorf("stderr: %s", e)
	}
}
