package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testExportCSV = "email,first_name,last_name,status,tags,source,created_at,status_changed_at,permission_confirmed\n" +
		"pat@example.com,Pat,,subscribed,vip,api,2026-11-18T12:00:00Z,2026-11-18T12:00:00Z,\n"
)

// exportStub answers export, tag and contact lookups, data requests and
// deletes, and records every request. With denied set, export answers 401.
func exportStub(t *testing.T, denied bool) (*harness, *[]seenRequest) {
	t.Helper()
	var seen []seenRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := seenRequest{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery}
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			_ = json.Unmarshal(b, &req.body)
		}
		seen = append(seen, req)
		switch {
		case r.URL.Path == "/v1/contacts/export" && denied:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"That API key isn't valid.","hint":""}}`))
		case r.URL.Path == "/v1/contacts/export":
			w.Header().Set("Content-Type", "text/csv; charset=utf-8")
			_, _ = w.Write([]byte(testExportCSV))
		case r.URL.Path == "/v1/tags":
			_, _ = w.Write([]byte(`{"data":[{"id":"` + testTagID + `","name":"vip","contacts":1,"created_at":"2026-11-18T12:00:00Z"}],"next_cursor":null}`))
		case r.URL.Path == "/v1/contacts":
			_, _ = w.Write([]byte(`{"data":[{"id":"` + testContactID + `","email":"pat@example.com","tags":[],"first_name":null,"last_name":null,` +
				`"status":"subscribed","source":"api","created_at":"2026-11-18T12:00:00Z","updated_at":"2026-11-18T12:00:00Z",` +
				`"status_changed_at":"2026-11-18T12:00:00Z"}],"next_cursor":null}`))
		case r.URL.Path == "/v1/contacts/"+testContactID+"/data":
			_, _ = w.Write([]byte(`{"contact":{"email":"pat@example.com"},"tags":[],"import":null,"skipped_rows":[]}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/contacts/"+testContactID:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	h := newHarness(t, "")
	h.env[EnvAPIURL] = srv.URL
	h.env[EnvAPIKey] = "mg_test_fullkey"
	return h, &seen
}

func TestExportToStandardOutput(t *testing.T) {
	h, seen := exportStub(t, false)
	if code := h.run("export"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if h.out.String() != testExportCSV || len(*seen) != 1 || (*seen)[0].path != "/v1/contacts/export" || (*seen)[0].query != "" {
		t.Errorf("output %q after %+v", h.out, *seen)
	}
}

func TestExportToAFile(t *testing.T) {
	h, seen := exportStub(t, false)
	out := filepath.Join(t.TempDir(), "vip.csv")
	if code := h.run("export", "--tag", "vip", "--status", "subscribed", "--output", out, "--json"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if q := (*seen)[1].query; q != "status=subscribed&tag_id="+testTagID {
		t.Errorf("export query = %q", q)
	}
	data, err := os.ReadFile(out)
	if err != nil || string(data) != testExportCSV {
		t.Fatalf("file = %q, %v", data, err)
	}
	if fi, _ := os.Stat(out); fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v", fi.Mode().Perm())
	}
	var got struct {
		File     string `json:"file"`
		Contacts int    `json:"contacts"`
	}
	if err := json.Unmarshal(h.out.Bytes(), &got); err != nil || got.File != out || got.Contacts != 1 {
		t.Errorf("json = %s", h.out)
	}

	// A failed export leaves no file behind.
	h, _ = exportStub(t, true)
	failed := filepath.Join(t.TempDir(), "failed.csv")
	if code := h.run("export", "--output", failed); code != ExitAuth {
		t.Errorf("exit %d: %s", code, h.errOut)
	}
	if _, err := os.Stat(failed); !os.IsNotExist(err) {
		t.Errorf("failed export left %s: %v", failed, err)
	}
}

func TestContactsExportData(t *testing.T) {
	h, seen := exportStub(t, false)
	if code := h.run("contacts", "export-data", "pat@example.com"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	var got map[string]any
	if err := json.Unmarshal(h.out.Bytes(), &got); err != nil || got["skipped_rows"] == nil || !strings.Contains(h.out.String(), "\n  \"contact\"") {
		t.Errorf("output = %s", h.out)
	}
	if last := (*seen)[len(*seen)-1]; last.path != "/v1/contacts/"+testContactID+"/data" {
		t.Errorf("last request = %+v", last)
	}
}

func TestContactsDeleteErase(t *testing.T) {
	h, seen := exportStub(t, false)
	if code := h.run("contacts", "delete", "pat@example.com", "--erase"); code != ExitUsage {
		t.Errorf("without --yes: exit %d", code)
	}
	if code := h.run("contacts", "delete", "pat@example.com", "--erase", "--yes"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	last := (*seen)[len(*seen)-1]
	if last.method != http.MethodDelete || last.query != "erase=true" || !strings.Contains(h.out.String(), "Erased pat@example.com") {
		t.Errorf("request %+v, output %q", last, h.out)
	}
	h.out.Reset()
	if code := h.run("contacts", "delete", "pat@example.com", "--yes", "--json"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if last := (*seen)[len(*seen)-1]; last.query != "" || !strings.Contains(h.out.String(), `"erased": false`) {
		t.Errorf("plain delete: %+v %s", last, h.out)
	}
}

func TestImportMapsStatusAndTagsColumns(t *testing.T) {
	h, seen := importsStub(t)
	path := writeCSV(t, "Mail,State,Labels\npat@example.com,unsubscribed,vip\n")
	if code := h.run("import", path, "--map", "email=Mail", "--map", "status=State", "--map", "tags=Labels"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	cols, _ := (*seen)[0].body["columns"].(map[string]any)
	if cols["status"] != "State" || cols["tags"] != "Labels" || cols["email"] != "Mail" {
		t.Errorf("columns = %v", cols)
	}
}
