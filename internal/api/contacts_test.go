package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

var addedContacts sync.Map // *testing.T -> contact id

// addedContact adds pat@example.com through h with the test's key and keeps
// its id for contactPath.
func addedContact(t *testing.T, h http.Handler) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, bearer(jsonRequest(http.MethodPost, "/v1/contacts", `{"email":"pat@example.com"}`))(t))
	if rec.Code != http.StatusCreated {
		t.Fatalf("add contact: %d %s", rec.Code, rec.Body)
	}
	var c struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	addedContacts.Store(t, c.ID)
	t.Cleanup(func() { addedContacts.Delete(t) })
}

// contactPath is a request to /v1/contacts/{the test's contact}, or to a
// contact that doesn't exist when the test added none.
func contactPath(method, body string) func(*testing.T) *http.Request {
	return contactPathSuffix(method, "", body)
}

// contactPathSuffix is contactPath with suffix after the contact's id.
func contactPathSuffix(method, suffix, body string) func(*testing.T) *http.Request {
	return func(t *testing.T) *http.Request {
		id := "00000000-0000-4000-8000-000000000000"
		if v, ok := addedContacts.Load(t); ok {
			id = v.(string)
		}
		if body != "" {
			return jsonRequest(method, "/v1/contacts/"+id+suffix, body)(t)
		}
		return httptest.NewRequest(method, "/v1/contacts/"+id+suffix, nil)
	}
}

func TestContactsFlow(t *testing.T) {
	s := draftKey(authServer)(t)
	h := s.Handler()
	do := func(req func(*testing.T) *http.Request) (int, map[string]any) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, bearer(req)(t))
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec.Code, body
	}
	errCode := func(body map[string]any) any { return body["error"].(map[string]any)["code"] }

	code, c := do(jsonRequest(http.MethodPost, "/v1/contacts", `{"email":"Pat@Example.com","first_name":"Pat","status":"unsubscribed"}`))
	if code != http.StatusCreated || c["email"] != "pat@example.com" || c["status"] != "unsubscribed" || c["source"] != "api" || c["last_name"] != nil {
		t.Fatalf("create: %d %v", code, c)
	}
	id := c["id"].(string)

	if code, c = do(jsonRequest(http.MethodPatch, "/v1/contacts/"+id, `{"status":"subscribed"}`)); code != http.StatusUnprocessableEntity || errCode(c) != "contact_suppressed" {
		t.Errorf("resubscribe: %d %v", code, c)
	}
	if code, c = do(jsonRequest(http.MethodPatch, "/v1/contacts/"+id, `{"first_name":null,"last_name":"Lee"}`)); code != http.StatusOK || c["first_name"] != nil || c["last_name"] != "Lee" {
		t.Errorf("update names: %d %v", code, c)
	}
	if code, c = do(jsonRequest(http.MethodPatch, "/v1/contacts/"+id, `{}`)); code != http.StatusBadRequest || errCode(c) != "empty_update" {
		t.Errorf("empty update: %d %v", code, c)
	}
	if code, c = do(jsonRequest(http.MethodPost, "/v1/contacts", `{"email":"pat@example.com"}`)); code != http.StatusConflict || errCode(c) != "contact_exists" {
		t.Errorf("duplicate: %d %v", code, c)
	}
	if code, c = do(jsonRequest(http.MethodPost, "/v1/contacts", `{"email":"sam@example.com","source":"import"}`)); code != http.StatusUnprocessableEntity || errCode(c) != "invalid_source" {
		t.Errorf("source import: %d %v", code, c)
	}
	if code, c = do(jsonRequest(http.MethodPost, "/v1/contacts", `{"email":"sam@example.com","status":"bounced"}`)); code != http.StatusUnprocessableEntity || errCode(c) != "invalid_status" {
		t.Errorf("status bounced: %d %v", code, c)
	}
	if code, c = do(jsonRequest(http.MethodPost, "/v1/contacts", `{"email":"sam@example.com","source":"manual","status":"unconfirmed"}`)); code != http.StatusCreated || c["source"] != "manual" {
		t.Errorf("manual: %d %v", code, c)
	}

	if code, c = do(request(http.MethodGet, "/v1/contacts?status=unconfirmed")); code != http.StatusOK || len(c["data"].([]any)) != 1 {
		t.Errorf("filter status: %d %v", code, c)
	}
	if code, c = do(request(http.MethodGet, "/v1/contacts?status=gone")); code != http.StatusBadRequest {
		t.Errorf("bad status: %d %v", code, c)
	}
	code, c = do(request(http.MethodGet, "/v1/contacts?limit=1"))
	if code != http.StatusOK || len(c["data"].([]any)) != 1 || c["next_cursor"] == nil {
		t.Fatalf("page 1: %d %v", code, c)
	}
	first := c["data"].([]any)[0].(map[string]any)["email"]
	code, c = do(request(http.MethodGet, "/v1/contacts?limit=1&cursor="+c["next_cursor"].(string)))
	if code != http.StatusOK || len(c["data"].([]any)) != 1 || c["data"].([]any)[0].(map[string]any)["email"] == first || c["next_cursor"] != nil {
		t.Errorf("page 2: %d %v", code, c)
	}

	// Deleting a suppressed contact and adding it again keeps the status.
	if code, _ = do(request(http.MethodDelete, "/v1/contacts/"+id)); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if code, _ = do(request(http.MethodGet, "/v1/contacts/"+id)); code != http.StatusNotFound {
		t.Errorf("get after delete: %d", code)
	}
	if code, c = do(jsonRequest(http.MethodPost, "/v1/contacts", `{"email":"pat@example.com","status":"subscribed"}`)); code != http.StatusCreated || c["status"] != "unsubscribed" {
		t.Errorf("re-add: %d %v", code, c)
	}
}
