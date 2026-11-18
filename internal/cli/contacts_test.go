package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testContactID = "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d"

func testContact(email, status string) string {
	return `{"id":"` + testContactID + `","email":"` + email + `","first_name":"Pat","last_name":null,"status":"` + status +
		`","source":"api","created_at":"2026-11-18T12:00:00Z","updated_at":"2026-11-18T12:00:00Z","status_changed_at":"2026-11-18T12:00:00Z"}`
}

// contactsStub has one contact, pat@example.com, and another address that
// contains it, so lookups must pick the exact match.
func contactsStub(t *testing.T) (*harness, *[]seenRequest) {
	t.Helper()
	var seen []seenRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := seenRequest{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, auth: r.Header.Get("Authorization")}
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			_ = json.Unmarshal(b, &req.body)
		}
		seen = append(seen, req)
		w.Header().Set("Content-Type", "application/json")
		one := "/v1/contacts/" + testContactID
		other := strings.Replace(testContact("xpat@example.com", "subscribed"), testContactID, "9f8e7d6c-5b4a-4392-8170-6f5e4d3c2b1a", 1)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/contacts" && req.body["email"] == "over@example.com":
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = w.Write([]byte(`{"error":{"code":"contact_limit","message":"A free account holds at most 500 contacts.","hint":"Delete contacts.","details":{"limit":500,"count":500}}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/contacts":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(testContact("pat@example.com", "unsubscribed")))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/contacts" && r.URL.Query().Get("email") == "nope@example.com":
			_, _ = w.Write([]byte(`{"data":[],"next_cursor":null}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/contacts" && r.URL.Query().Get("cursor") == "":
			_, _ = w.Write([]byte(`{"data":[` + other + `],"next_cursor":"p2"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/contacts":
			_, _ = w.Write([]byte(`{"data":[` + testContact("pat@example.com", "subscribed") + `],"next_cursor":null}`))
		case r.Method == http.MethodGet && r.URL.Path == one:
			_, _ = w.Write([]byte(testContact("pat@example.com", "subscribed")))
		case r.Method == http.MethodPatch && r.URL.Path == one:
			_, _ = w.Write([]byte(testContact("pat@example.com", "unsubscribed")))
		case r.Method == http.MethodDelete && r.URL.Path == one:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"No contact with that id in this account.","hint":""}}`))
		}
	}))
	t.Cleanup(srv.Close)
	h := newHarness(t, "")
	h.env[EnvAPIURL] = srv.URL
	h.env[EnvAPIKey] = "mg_test_fullkey"
	return h, &seen
}

func TestContactsAdd(t *testing.T) {
	h, seen := contactsStub(t)
	if code := h.run("contacts", "add", "pat@example.com", "--first-name", "Pat", "--status", "subscribed"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	got := (*seen)[0]
	if got.method != http.MethodPost || got.body["email"] != "pat@example.com" || got.body["first_name"] != "Pat" || got.body["status"] != "subscribed" {
		t.Errorf("request = %+v", got)
	}
	if _, ok := got.body["last_name"]; ok {
		t.Errorf("unset last name sent: %+v", got.body)
	}
	// The stub answers unsubscribed: the address was suppressed.
	if out := h.out.String(); !strings.Contains(out, "unsubscribed") || !strings.Contains(out, "keeps that status") {
		t.Errorf("output %q", out)
	}
}

func TestContactsLookupByAddress(t *testing.T) {
	h, seen := contactsStub(t)
	if code := h.run("contacts", "get", "PAT@example.com"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	// Page 1 holds only xpat@, so it reads page 2, then gets the contact.
	if len(*seen) != 3 || !strings.Contains((*seen)[0].query, "email=pat%40example.com") || (*seen)[2].path != "/v1/contacts/"+testContactID {
		t.Errorf("requests = %+v", *seen)
	}
	if !strings.Contains(h.out.String(), "pat@example.com") {
		t.Errorf("output %q", h.out)
	}
	*seen = nil
	if code := h.run("contacts", "get", testContactID); code != ExitOK || len(*seen) != 1 {
		t.Errorf("by id: exit %d, requests %+v", code, *seen)
	}
	if code := h.run("contacts", "get", "nope@example.com"); code != ExitNotFound {
		t.Errorf("unknown: exit %d %s", code, h.errOut)
	}
}

func TestContactsUpdate(t *testing.T) {
	h, seen := contactsStub(t)
	if code := h.run("contacts", "update", testContactID); code != ExitUsage {
		t.Errorf("no flags: exit %d", code)
	}
	*seen = nil
	if code := h.run("contacts", "update", testContactID, "--first-name", "", "--last-name", "Lee", "--status", "unsubscribed"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	got := (*seen)[0]
	if got.method != http.MethodPatch || got.body["last_name"] != "Lee" || got.body["status"] != "unsubscribed" {
		t.Errorf("request = %+v", got)
	}
	if v, ok := got.body["first_name"]; !ok || v != nil {
		t.Errorf("empty --first-name should send null: %+v", got.body)
	}
	if _, ok := got.body["email"]; ok {
		t.Errorf("email sent though not given: %+v", got.body)
	}
}

func TestContactsListAndDelete(t *testing.T) {
	h, seen := contactsStub(t)
	if code := h.run("contacts", "list", "--status", "subscribed", "--name", "pat", "--limit", "10"); code != ExitOK {
		t.Fatalf("list: exit %d %s", code, h.errOut)
	}
	if q := (*seen)[0].query; !strings.Contains(q, "status=subscribed") || !strings.Contains(q, "name=pat") || !strings.Contains(q, "limit=10") {
		t.Errorf("query %q", q)
	}
	if out := h.out.String(); !strings.Contains(out, "xpat@example.com") || !strings.Contains(out, "--cursor p2") {
		t.Errorf("output %q", out)
	}
	if code := h.run("contacts", "delete", testContactID); code != ExitUsage {
		t.Errorf("delete without --yes: exit %d", code)
	}
	*seen = nil
	if code := h.run("contacts", "delete", testContactID, "--yes", "--json"); code != ExitOK {
		t.Fatalf("delete: exit %d %s", code, h.errOut)
	}
	if len(*seen) != 1 || (*seen)[0].method != http.MethodDelete {
		t.Errorf("requests = %+v", *seen)
	}
}

func TestContactsAddOverLimit(t *testing.T) {
	h, _ := contactsStub(t)
	if code := h.run("contacts", "add", "over@example.com"); code != ExitError {
		t.Errorf("exit %d", code)
	}
	if !strings.Contains(h.errOut.String(), "at most 500") {
		t.Errorf("stderr = %s", h.errOut)
	}
	if code := h.run("contacts", "add", "over@example.com", "--json"); code != ExitError {
		t.Errorf("json: exit %d", code)
	}
	var body struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]int `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(h.errOut.Bytes(), &body); err != nil {
		t.Fatalf("%v: %s", err, h.errOut)
	}
	if body.Error.Code != "contact_limit" || body.Error.Details["limit"] != 500 || body.Error.Details["count"] != 500 {
		t.Errorf("json error = %s", h.errOut)
	}
}
