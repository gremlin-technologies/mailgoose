package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testImportID = "7c6b5a49-3827-4165-9483-726150493827"

func testImport(status string, processed, imported, skipped int) string {
	return `{"id":"` + testImportID + `","file_name":"list.csv","status":"` + status + `","contact_status":"unconfirmed","tags":[],` +
		`"columns":{"email":"Email","first_name":null,"last_name":null},"total_rows":4,"processed_rows":` + itoa(processed) +
		`,"imported":` + itoa(imported) + `,"skipped":` + itoa(skipped) +
		`,"skipped_by_reason":{"invalid_email":1,"invalid_row":0,"duplicate":0,"exists":1,"suppressed":0,"over_limit":0},` +
		`"review":"held","hold_reason":"over_contact_limit","hold_message":"It would take the account past its contact limit, so mailgoose checks it.",` +
		`"list_source":"other","list_source_detail":"Trade show","checks":{"invalid_rows":1,"role_addresses":2,"disposable_addresses":0},"error":null,"created_at":"2026-11-18T12:00:00Z","started_at":null,"finished_at":null}`
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// importsStub queues an import, answers running on the first status check and
// done after that, with two skipped rows over two pages.
func importsStub(t *testing.T) (*harness, *[]seenRequest) {
	t.Helper()
	var seen []seenRequest
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := seenRequest{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery}
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			_ = json.Unmarshal(b, &req.body)
		}
		seen = append(seen, req)
		w.Header().Set("Content-Type", "application/json")
		one := "/v1/imports/" + testImportID
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/imports" && strings.HasPrefix(req.body["csv"].(string), "name"):
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"error":{"code":"no_email_column","message":"No column looks like email addresses.","hint":"Name the columns."}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/imports/preview" && strings.HasPrefix(req.body["csv"].(string), "name"):
			_, _ = w.Write([]byte(`{"separator":"comma","rows":1,"has_header_guess":true,"columns":[{"position":1,"header":"name","name":"name","field":null,"samples":["Pat"]}],` +
				`"problem":{"code":"no_email_column","message":"No column looks like email addresses."}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/imports/preview" && strings.HasPrefix(req.body["csv"].(string), "Pat"):
			_, _ = w.Write([]byte(`{"separator":"tab","rows":1,"has_header_guess":false,"columns":[` +
				`{"position":1,"header":"","name":"#1","field":null,"samples":["Pat"]},` +
				`{"position":2,"header":"","name":"#2","field":"email","samples":["pat@example.com"]}],"problem":null}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/imports/preview" && strings.HasPrefix(req.body["csv"].(string), "Email,Organisation"):
			_, _ = w.Write([]byte(`{"separator":"comma","rows":1,"has_header_guess":true,"columns":[` +
				`{"position":1,"header":"Email","name":"Email","field":"email","custom_field":null,"new_field":null,"samples":["pat@example.com"]},` +
				`{"position":2,"header":"Organisation","name":"Organisation","field":null,"custom_field":{"id":"f1","key":"company","name":"Company"},"new_field":null,"samples":["Acme"]},` +
				`{"position":3,"header":"Size","name":"Size","field":null,"custom_field":null,"new_field":{"name":"Company size","key":"company_size"},"samples":["12"]}],"problem":null}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/imports" && strings.HasPrefix(req.body["csv"].(string), "Email,Organisation"):
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(strings.Replace(testImport("queued", 0, 0, 0), `"tags":[]`, `"tags":[],"existing_updated":0,"field_columns":[`+
				`{"field":{"id":"f1","key":"company","name":"Company"},"column":"Organisation","created":false},`+
				`{"field":{"id":"f2","key":"company_size","name":"Company size"},"column":"Size","created":true}]`, 1)))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/imports/preview":
			_, _ = w.Write([]byte(`{"separator":"tab","rows":2,"has_header_guess":true,"columns":[` +
				`{"position":1,"header":"E-mail","name":"E-mail","field":"email","samples":["pat@example.com","ana@example.com"]},` +
				`{"position":2,"header":"","name":"#2","field":"first_name","samples":["Pat",""]},` +
				`{"position":3,"header":"status","name":"status","field":null,"samples":["unsubscribed","line\nbreak"]}],"problem":null}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/imports":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(testImport("queued", 0, 0, 0)))
		case r.URL.Path == one:
			polls++
			if polls == 1 {
				_, _ = w.Write([]byte(testImport("running", 2, 1, 0)))
				return
			}
			_, _ = w.Write([]byte(testImport("done", 4, 2, 2)))
		case r.URL.Path == one+"/skipped-rows/export":
			w.Header().Set("Content-Type", "text/csv; charset=utf-8")
			_, _ = w.Write([]byte("row,source,email,reason,why\n3,,nope,invalid_email,Not an email address.\n5,,pat@example.com,exists,Already a contact; not added again.\n"))
		case r.URL.Path == one+"/skipped-rows" && r.URL.Query().Get("cursor") == "":
			_, _ = w.Write([]byte(`{"data":[{"row":3,"email":"nope","reason":"invalid_email"}],"next_cursor":"p2"}`))
		case r.URL.Path == one+"/skipped-rows":
			_, _ = w.Write([]byte(`{"data":[{"row":5,"email":"pat@example.com","reason":"exists"}],"next_cursor":null}`))
		case r.URL.Path == "/v1/imports":
			pasted := strings.Replace(testImport("done", 4, 2, 2), `"file_name":"list.csv"`, `"kind":"csv","file_name":null`, 1)
			pasted = strings.Replace(pasted, testImportID, "11111111-2222-3333-4444-555555555555", 1)
			_, _ = w.Write([]byte(`{"data":[` + testImport("done", 4, 2, 2) + `,` + pasted + `],"next_cursor":null}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"No import with that id in this account.","hint":""}}`))
		}
	}))
	t.Cleanup(srv.Close)
	h := newHarness(t, "email\nfrom@stdin.example\n")
	h.app.Sleep = func(context.Context, time.Duration) error { return nil }
	h.env[EnvAPIURL] = srv.URL
	h.env[EnvAPIKey] = "mg_test_fullkey"
	return h, &seen
}

func writeCSV(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "list.csv")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestImportWaitsAndShowsSkippedRows(t *testing.T) {
	h, seen := importsStub(t)
	path := writeCSV(t, "E-mail,Vorname\npat@example.com,Pat\n")
	code := h.run("import", path, "--list-source", "other", "--list-source-detail", "Trade show", "--map", "email=E-mail", "--map", "first-name=Vorname", "--tag", "vip", "--tag", "news", "--wait")
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	post := (*seen)[0]
	cols, _ := post.body["columns"].(map[string]any)
	if post.method != http.MethodPost || post.body["csv"] != "E-mail,Vorname\npat@example.com,Pat\n" || post.body["file_name"] != "list.csv" ||
		cols["email"] != "E-mail" || cols["first_name"] != "Vorname" || post.body["permission_confirmed"] != false ||
		post.body["list_source"] != "other" || post.body["list_source_detail"] != "Trade show" ||
		len(post.body["tags"].([]any)) != 2 {
		t.Errorf("request = %+v", post)
	}
	// Queued, then running, then done; then both pages of skipped rows.
	if len(*seen) != 5 {
		t.Errorf("%d requests: %+v", len(*seen), *seen)
	}
	out := h.out.String()
	for _, want := range []string{"is done: 2 of 4 rows added as unconfirmed contacts, 2 skipped", "Held for review. It would take the account past its contact limit",
		"List source: other: Trade show.", "Checks: 1 invalid rows, 2 role addresses (like info@), 0 disposable-domain addresses.",
		"1 not an email address (invalid_email)", "3    nope", "5    pat@example.com  exists", "--permission-confirmed"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestImportWithoutWait(t *testing.T) {
	h, seen := importsStub(t)
	if code := h.run("import", "-", "--list-source", "form", "--permission-confirmed"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	post := (*seen)[0]
	if post.body["csv"] != "email\nfrom@stdin.example\n" || post.body["permission_confirmed"] != true || post.body["file_name"] != nil ||
		post.body["list_source"] != "form" || post.body["list_source_detail"] != nil {
		t.Errorf("request = %+v", post)
	}
	if len(*seen) != 1 || !strings.Contains(h.out.String(), "mailgoose import status "+testImportID) || strings.Contains(h.out.String(), "unconfirmed, so") {
		t.Errorf("output %q after %d requests", h.out, len(*seen))
	}
}

func TestImportStatusJSON(t *testing.T) {
	h, _ := importsStub(t)
	h.run("import", "status", testImportID) // running
	if code := h.run("import", "status", testImportID, "--json"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	var got struct {
		Status      string       `json:"status"`
		SkippedRows []skippedRow `json:"skipped_rows"`
	}
	if err := json.Unmarshal(h.out.Bytes(), &got); err != nil || got.Status != "done" || len(got.SkippedRows) != 2 || got.SkippedRows[1].Reason != "exists" {
		t.Errorf("json = %s (%v)", h.out, err)
	}
}

func TestImportList(t *testing.T) {
	h, seen := importsStub(t)
	if code := h.run("import", "list", "--limit", "5"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if (*seen)[0].query != "limit=5" || !strings.Contains(h.out.String(), "list.csv") || !strings.Contains(h.out.String(), "pasted") {
		t.Errorf("query %q, output %s", (*seen)[0].query, h.out)
	}
}

func TestImportErrors(t *testing.T) {
	h, seen := importsStub(t)
	path := writeCSV(t, "email\na@example.com\n")
	for _, args := range [][]string{
		{"import"},
		{"import", path},
		{"import", path, "--list-source", " "},
		{"import", path, "--list-source", "form", "--map", "phone=Phone"},
		{"import", path, "--list-source", "form", "--map", "email"},
		{"import", filepath.Join(t.TempDir(), "missing.csv"), "--list-source", "form"},
	} {
		if code := h.run(args...); code != ExitUsage {
			t.Errorf("%v: exit %d, want %d (%s)", args, code, ExitUsage, h.errOut)
		}
	}
	if len(*seen) != 0 {
		t.Errorf("usage errors reached the API: %+v", *seen)
	}
	if code := h.run("import", path); !strings.Contains(h.errOut.String(), "--list-source is required") ||
		!strings.Contains(h.errOut.String(), "customers who bought from me") {
		t.Errorf("no list source: exit %d: %s", code, h.errOut)
	}
	if code := h.run("import", writeCSV(t, "name\nPat\n"), "--list-source", "form", "--json"); code != ExitError || !strings.Contains(h.errOut.String(), "no_email_column") {
		t.Errorf("refused file: exit %d: %s", code, h.errOut)
	}
	if code := h.run("import", "status", "nope"); code != ExitNotFound {
		t.Errorf("unknown import: exit %d", code)
	}
}

func TestImportPreview(t *testing.T) {
	h, seen := importsStub(t)
	path := writeCSV(t, "E-mail\t\tstatus\npat@example.com\tPat\tunsubscribed\n")
	if code := h.run("import", "preview", path, "--map", "first_name=#2", "--map", "status="); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	post := (*seen)[0]
	cols, _ := post.body["columns"].(map[string]any)
	if post.path != "/v1/imports/preview" || post.body["csv"] != "E-mail\t\tstatus\npat@example.com\tPat\tunsubscribed\n" ||
		cols["first_name"] != "#2" || cols["status"] != "" || len(cols) != 2 || post.body["list_source"] != nil {
		t.Errorf("request = %+v", post)
	}
	out := h.out.String()
	for _, want := range []string{"Separated by tabs, with 2 contact rows.", "COLUMN  FIELD", "E-mail  email         pat@example.com, ana@example.com",
		"#2      first_name    Pat, -", "status  not imported  unsubscribed, line break", "run \"mailgoose import\" with the same file"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	h.out.Reset()
	if code := h.run("import", "preview", writeCSV(t, "name\nPat\n"), "--json"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	var p importPreview
	if err := json.Unmarshal(h.out.Bytes(), &p); err != nil || p.Problem == nil || p.Problem.Code != "no_email_column" || len(p.Columns) != 1 {
		t.Errorf("json = %s (%v)", h.out, err)
	}
	h.out.Reset()
	h.run("import", "preview", writeCSV(t, "name\nPat\n"))
	if !strings.Contains(h.out.String(), "can't be imported as it is: No column looks like email addresses.") || !strings.Contains(h.out.String(), "--map email=") {
		t.Errorf("problem output:\n%s", h.out)
	}

	// Email can't be left out.
	n := len(*seen)
	if code := h.run("import", "preview", path, "--map", "email="); code != ExitUsage || len(*seen) != n {
		t.Errorf("--map email=: exit %d", code)
	}
}

// --no-header sends has_header false, on the import and its preview, and
// the preview says when the first row looks like a contact (MG-125).
func TestImportWithoutHeader(t *testing.T) {
	h, seen := importsStub(t)
	pasted := writeCSV(t, "Pat\tpat@example.com\n")
	if code := h.run("import", "preview", pasted); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if _, ok := (*seen)[0].body["has_header"]; ok {
		t.Errorf("has_header sent without --no-header: %+v", (*seen)[0].body)
	}
	if !strings.Contains(h.out.String(), "The first row looks like a contact, not a header. If it is, add --no-header.") {
		t.Errorf("no hint:\n%s", h.out)
	}

	h.out.Reset()
	if code := h.run("import", "preview", pasted, "--no-header"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if (*seen)[1].body["has_header"] != false || strings.Contains(h.out.String(), "looks like") {
		t.Errorf("request %+v, output:\n%s", (*seen)[1].body, h.out)
	}

	h.out.Reset()
	if code := h.run("import", "preview", writeCSV(t, "E-mail\nann@example.com\n"), "--no-header"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if !strings.Contains(h.out.String(), "The first row looks like a header. If it is, leave out --no-header.") {
		t.Errorf("no header hint:\n%s", h.out)
	}

	n := len(*seen)
	if code := h.run("import", "-", "--no-header", "--list-source", "customers", "--map", "first_name=#1"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	post := (*seen)[n]
	if post.body["has_header"] != false || post.body["csv"] != "email\nfrom@stdin.example\n" {
		t.Errorf("request = %+v", post.body)
	}
	if _, ok := post.body["file_name"]; ok {
		t.Errorf("file_name sent for standard input: %+v", post.body)
	}

	n = len(*seen)
	if code := h.run("import", pasted, "--list-source", "customers"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if _, ok := (*seen)[n].body["has_header"]; ok {
		t.Errorf("has_header sent without --no-header: %+v", (*seen)[n].body)
	}
}

// --map field= sends an empty column, so the import leaves the field out.
func TestImportLeavesAFieldOut(t *testing.T) {
	h, seen := importsStub(t)
	if code := h.run("import", writeCSV(t, "email,tags\na@example.com,vip\n"), "--list-source", "form", "--map", "tags= "); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	cols, _ := (*seen)[0].body["columns"].(map[string]any)
	if v, ok := cols["tags"]; !ok || v != "" {
		t.Errorf("columns = %+v", cols)
	}
}

func TestImportSkippedDownloadsTheCSV(t *testing.T) {
	h, _ := importsStub(t)
	if code := h.run("import", "skipped", testImportID); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if out := h.out.String(); !strings.HasPrefix(out, "row,source,email,reason,why\n3,,nope") {
		t.Errorf("stdout:\n%s", out)
	}

	h.out.Reset()
	path := filepath.Join(t.TempDir(), "skipped.csv")
	if code := h.run("import", "skipped", testImportID, "--output", path, "--json"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if out := h.out.String(); !strings.Contains(out, `"rows": 2`) {
		t.Errorf("json:\n%s", out)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("file = %v %v", fi, err)
	}

	missing := filepath.Join(t.TempDir(), "none.csv")
	if code := h.run("import", "skipped", "nope", "--output", missing); code == ExitOK {
		t.Error("an unknown import succeeded")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Errorf("a failed download left %s", missing)
	}
}

// Contact field columns (MG-131): --field and --new-field go in the request,
// and the preview and the import show them.
func TestImportContactFieldFlags(t *testing.T) {
	h, seen := importsStub(t)
	path := writeCSV(t, "Email,Organisation,Size\npat@example.com,Acme,12\n")
	if code := h.run("import", "preview", path, "--field", "company=Organisation", "--field", "plan=", "--new-field", "Size=Company size"); code != ExitOK {
		t.Fatalf("preview: exit %d: %s", code, h.errOut)
	}
	body := (*seen)[0].body
	cols, _ := body["field_columns"].(map[string]any)
	news, _ := body["new_fields"].([]any)
	if len(cols) != 2 || cols["company"] != "Organisation" || cols["plan"] != "" || len(news) != 1 ||
		news[0].(map[string]any)["column"] != "Size" || news[0].(map[string]any)["name"] != "Company size" {
		t.Errorf("preview request = %+v", body)
	}
	out := h.out.String()
	for _, want := range []string{"Organisation  field company", "Size          new field company_size (Company size)"} {
		if !strings.Contains(out, want) {
			t.Errorf("preview output lacks %q:\n%s", want, out)
		}
	}

	h.out.Reset()
	if code := h.run("import", path, "--list-source", "customers", "--field", "company=Organisation", "--new-field", "Size", "--new-field", "#4=Region"); code != ExitOK {
		t.Fatalf("import: exit %d: %s", code, h.errOut)
	}
	body = (*seen)[1].body
	news, _ = body["new_fields"].([]any)
	if body["field_columns"].(map[string]any)["company"] != "Organisation" || len(news) != 2 ||
		news[0].(map[string]any)["name"] != nil || news[1].(map[string]any)["column"] != "#4" || news[1].(map[string]any)["name"] != "Region" {
		t.Errorf("import request = %+v", body)
	}
	if want := `Contact fields: company from "Organisation", company_size from "Size" (new).`; !strings.Contains(h.out.String(), want) {
		t.Errorf("import output lacks %q:\n%s", want, h.out)
	}

	n := len(*seen)
	for _, args := range [][]string{
		{"import", path, "--list-source", "form", "--field", "company"},
		{"import", path, "--list-source", "form", "--field", "=Organisation"},
		{"import", "preview", path, "--new-field", "=Name"},
	} {
		if code := h.run(args...); code != ExitUsage {
			t.Errorf("%v: exit %d, want %d (%s)", args, code, ExitUsage, h.errOut)
		}
	}
	if len(*seen) != n {
		t.Errorf("usage errors reached the API: %+v", (*seen)[n:])
	}
}
