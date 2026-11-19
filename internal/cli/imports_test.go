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
		case r.URL.Path == one+"/skipped-rows" && r.URL.Query().Get("cursor") == "":
			_, _ = w.Write([]byte(`{"data":[{"row":3,"email":"nope","reason":"invalid_email"}],"next_cursor":"p2"}`))
		case r.URL.Path == one+"/skipped-rows":
			_, _ = w.Write([]byte(`{"data":[{"row":5,"email":"pat@example.com","reason":"exists"}],"next_cursor":null}`))
		case r.URL.Path == "/v1/imports":
			_, _ = w.Write([]byte(`{"data":[` + testImport("done", 4, 2, 2) + `],"next_cursor":null}`))
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
	if (*seen)[0].query != "limit=5" || !strings.Contains(h.out.String(), "list.csv") {
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
