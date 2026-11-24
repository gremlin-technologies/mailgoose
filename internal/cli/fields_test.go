package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testFieldID = "6e5d4c3b-2a1f-4e0d-9c8b-7a6f5e4d3c2b"

// fieldsStub knows the field "Company size" (company_size) and the contact
// pat@example.com, and answers 409 field_limit to a create named "Full".
func fieldsStub(t *testing.T) (*harness, *[]seenRequest) {
	t.Helper()
	var seen []seenRequest
	size := `{"id":"` + testFieldID + `","key":"company_size","name":"Company size","created_at":"2026-11-18T12:00:00Z","updated_at":"2026-11-18T12:00:00Z"}`
	pat := strings.TrimSuffix(testContact("pat@example.com", "subscribed"), "}") + `,"tags":[],"fields":{"company_size":"50"}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := seenRequest{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, auth: r.Header.Get("Authorization")}
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			_ = json.Unmarshal(b, &req.body)
		}
		seen = append(seen, req)
		w.Header().Set("Content-Type", "application/json")
		one := "/v1/contact-fields/" + testFieldID
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/contact-fields":
			_, _ = w.Write([]byte(`{"data":[` + size + `],"next_cursor":null}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/contact-fields" && req.body["name"] == "Full":
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":{"code":"field_limit","message":"An account can have at most 20 contact fields, and this one has 20.","hint":""}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/contact-fields":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(size))
		case r.Method == http.MethodPatch && r.URL.Path == one:
			_, _ = w.Write([]byte(strings.Replace(size, `"Company size"`, `"Team size"`, 1)))
		case r.Method == http.MethodDelete && r.URL.Path == one:
			_, _ = w.Write([]byte(`{"values_deleted":3}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/contacts":
			_, _ = w.Write([]byte(`{"data":[` + pat + `],"next_cursor":null}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/contacts":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(pat))
		case r.URL.Path == "/v1/contacts/"+testContactID:
			_, _ = w.Write([]byte(pat))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"No contact field with that id in this account.","hint":""}}`))
		}
	}))
	t.Cleanup(srv.Close)
	h := newHarness(t, "")
	h.env[EnvAPIURL] = srv.URL
	h.env[EnvAPIKey] = "mg_test_fullkey"
	return h, &seen
}

func TestFieldsCreateRenameDelete(t *testing.T) {
	h, seen := fieldsStub(t)
	if code := h.run("fields", "create", "Company size"); code != ExitOK || !strings.Contains(h.out.String(), "company_size") {
		t.Errorf("create: exit %d %s %s", code, h.out, h.errOut)
	}
	if (*seen)[0].body["name"] != "Company size" {
		t.Errorf("create body = %v", (*seen)[0].body)
	}
	if code := h.run("field", "create", "Full"); code == ExitOK || !strings.Contains(h.errOut.String(), "at most 20") {
		t.Errorf("create past the limit: exit %d %s", code, h.errOut)
	}
	if code := h.run("fields", "list"); code != ExitOK || !strings.Contains(h.out.String(), "company_size") || !strings.Contains(h.out.String(), "Company size") {
		t.Errorf("list: exit %d %s", code, h.out)
	}

	// A field is named by key, name (any case) or id.
	for _, name := range []string{"company_size", "company SIZE", testFieldID} {
		*seen = nil
		if code := h.run("fields", "rename", name, "Team size"); code != ExitOK || !strings.Contains(h.out.String(), "company_size") {
			t.Fatalf("rename %s: exit %d %s", name, code, h.errOut)
		}
		last := (*seen)[len(*seen)-1]
		if last.method != http.MethodPatch || last.path != "/v1/contact-fields/"+testFieldID || last.body["name"] != "Team size" {
			t.Errorf("rename %s requests = %+v", name, *seen)
		}
	}
	if code := h.run("fields", "rename", "nope", "x"); code != ExitNotFound {
		t.Errorf("unknown field: exit %d", code)
	}

	*seen = nil
	if code := h.run("fields", "delete", "company_size"); code != ExitUsage || len(*seen) != 0 {
		t.Errorf("delete without --yes: exit %d, %d requests", code, len(*seen))
	}
	if code := h.run("fields", "delete", "company_size", "--yes"); code != ExitOK || !strings.Contains(h.out.String(), "3 contacts") {
		t.Errorf("delete: exit %d %s", code, h.out)
	}
}

func TestContactFieldFlags(t *testing.T) {
	h, seen := fieldsStub(t)
	if code := h.run("contacts", "add", "pat@example.com", "--field", "company_size=50", "--field", "plan=Pro", "--field", "region="); code != ExitOK {
		t.Fatalf("add: exit %d %s", code, h.errOut)
	}
	fields, _ := (*seen)[0].body["fields"].(map[string]any)
	if len(fields) != 2 || fields["company_size"] != "50" || fields["plan"] != "Pro" {
		t.Errorf("add fields = %v; an empty value is left out on add", (*seen)[0].body)
	}

	*seen = nil
	if code := h.run("contacts", "update", "pat@example.com", "--field", "company_size=75", "--field", "plan="); code != ExitOK {
		t.Fatalf("update: exit %d %s", code, h.errOut)
	}
	last := (*seen)[len(*seen)-1]
	fields, _ = last.body["fields"].(map[string]any)
	if v, ok := fields["plan"]; last.method != http.MethodPatch || fields["company_size"] != "75" || !ok || v != nil {
		t.Errorf("update body = %v; key= sends null", last.body)
	}
	if !strings.Contains(h.out.String(), "company_size") || !strings.Contains(h.out.String(), "50") {
		t.Errorf("update prints the contact's fields:\n%s", h.out)
	}

	if code := h.run("contacts", "update", "pat@example.com", "--field", "nokey"); code != ExitUsage {
		t.Errorf("--field without =: exit %d", code)
	}
	if code := h.run("contacts", "update", "pat@example.com", "--field", "=x"); code != ExitUsage {
		t.Errorf("--field without a key: exit %d", code)
	}
}
