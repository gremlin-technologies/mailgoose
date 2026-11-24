package api

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

var addedFields sync.Map // *testing.T -> field id

// addedField creates the contact field "Company" through h with the test's
// key and keeps its id for fieldPath.
func addedField(t *testing.T, h http.Handler) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, bearer(jsonRequest(http.MethodPost, "/v1/contact-fields", `{"name":"Company"}`))(t))
	if rec.Code != http.StatusCreated {
		t.Fatalf("add field: %d %s", rec.Code, rec.Body)
	}
	var f struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &f); err != nil {
		t.Fatal(err)
	}
	addedFields.Store(t, f.ID)
	t.Cleanup(func() { addedFields.Delete(t) })
}

// fieldPath is a request to /v1/contact-fields/{the test's field}, or to a
// field that doesn't exist when the test added none.
func fieldPath(method, body string) func(*testing.T) *http.Request {
	return func(t *testing.T) *http.Request {
		id := "00000000-0000-4000-8000-000000000000"
		if v, ok := addedFields.Load(t); ok {
			id = v.(string)
		}
		if body != "" {
			return jsonRequest(method, "/v1/contact-fields/"+id, body)(t)
		}
		return httptest.NewRequest(method, "/v1/contact-fields/"+id, nil)
	}
}

func TestContactFieldsFlow(t *testing.T) {
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
	errCode := func(body map[string]any) string {
		e, _ := body["error"].(map[string]any)
		c, _ := e["code"].(string)
		return c
	}

	code, size := do(jsonRequest(http.MethodPost, "/v1/contact-fields", `{"name":"Company size"}`))
	if code != http.StatusCreated || size["key"] != "company_size" || size["name"] != "Company size" {
		t.Fatalf("create: %d %v", code, size)
	}
	if code, b := do(jsonRequest(http.MethodPost, "/v1/contact-fields", `{"name":"company SIZE"}`)); code != http.StatusConflict || errCode(b) != "field_exists" {
		t.Errorf("duplicate name: %d %v", code, b)
	}
	if code, b := do(jsonRequest(http.MethodPost, "/v1/contact-fields", `{"name":"Company-size"}`)); code != http.StatusConflict || errCode(b) != "field_exists" ||
		b["error"].(map[string]any)["details"].(map[string]any)["key"] != "company_size" {
		t.Errorf("duplicate key: %d %v", code, b)
	}
	if code, b := do(jsonRequest(http.MethodPost, "/v1/contact-fields", `{"name":"Email"}`)); code != http.StatusUnprocessableEntity || errCode(b) != "reserved_field_name" {
		t.Errorf("reserved key: %d %v", code, b)
	}

	// Contacts carry values by key.
	code, pat := do(jsonRequest(http.MethodPost, "/v1/contacts", `{"email":"pat@example.com","fields":{"company_size":"50"}}`))
	if code != http.StatusCreated || pat["fields"].(map[string]any)["company_size"] != "50" {
		t.Fatalf("create contact with a field: %d %v", code, pat)
	}
	patPath := "/v1/contacts/" + pat["id"].(string)
	if code, b := do(jsonRequest(http.MethodPost, "/v1/contacts", `{"email":"sam@example.com","fields":{"nope":"x"}}`)); code != http.StatusUnprocessableEntity ||
		errCode(b) != "unknown_field" || !strings.Contains(b["error"].(map[string]any)["message"].(string), "nope") {
		t.Errorf("unknown key on create: %d %v", code, b)
	}
	if code, b := do(jsonRequest(http.MethodPatch, patPath, `{"fields":{"company_size":"`+strings.Repeat("x", 501)+`"}}`)); code != http.StatusUnprocessableEntity ||
		errCode(b) != "invalid_field_value" {
		t.Errorf("long value: %d %v", code, b)
	}
	if code, b := do(jsonRequest(http.MethodPatch, patPath, `{"fields":{}}`)); code != http.StatusBadRequest || errCode(b) != "empty_update" {
		t.Errorf("empty fields: %d %v", code, b)
	}
	_, plan := do(jsonRequest(http.MethodPost, "/v1/contact-fields", `{"name":"Plan"}`))
	if code, b := do(jsonRequest(http.MethodPatch, patPath, `{"fields":{"plan":"Pro"}}`)); code != http.StatusOK ||
		b["fields"].(map[string]any)["company_size"] != "50" || b["fields"].(map[string]any)["plan"] != "Pro" {
		t.Errorf("set one, keep the other: %d %v", code, b)
	}
	if code, b := do(jsonRequest(http.MethodPatch, patPath, `{"fields":{"company_size":null,"plan":""}}`)); code != http.StatusOK || len(b["fields"].(map[string]any)) != 0 {
		t.Errorf("clear both: %d %v", code, b)
	}
	do(jsonRequest(http.MethodPatch, patPath, `{"fields":{"company_size":"75"}}`))

	// A rename keeps the key and values, and the export heads the column
	// with the name.
	if code, b := do(jsonRequest(http.MethodPatch, "/v1/contact-fields/"+size["id"].(string), `{"name":"Team size"}`)); code != http.StatusOK ||
		b["key"] != "company_size" || b["name"] != "Team size" {
		t.Errorf("rename: %d %v", code, b)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, bearer(request(http.MethodGet, "/v1/contacts/export"))(t))
	recs, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil || len(recs) != 2 {
		t.Fatalf("export: %v %v", recs, err)
	}
	if hdr := recs[0]; hdr[len(hdr)-2] != "Team size" || hdr[len(hdr)-1] != "Plan" || recs[1][len(hdr)-2] != "75" || recs[1][len(hdr)-1] != "" {
		t.Errorf("export field columns: %q / %q", recs[0], recs[1])
	}

	code, list := do(request(http.MethodGet, "/v1/contact-fields"))
	if data := list["data"].([]any); code != http.StatusOK || len(data) != 2 || data[0].(map[string]any)["key"] != "company_size" {
		t.Errorf("list: %d %v", code, list)
	}
	if code, b := do(request(http.MethodDelete, "/v1/contact-fields/"+size["id"].(string))); code != http.StatusOK || b["values_deleted"] != 1.0 {
		t.Errorf("delete: %d %v", code, b)
	}
	if _, b := do(request(http.MethodGet, patPath)); len(b["fields"].(map[string]any)) != 0 {
		t.Errorf("contact after the delete: %v", b["fields"])
	}
	if code, _ := do(request(http.MethodDelete, "/v1/contact-fields/"+plan["id"].(string))); code != http.StatusOK {
		t.Errorf("delete plan: %d", code)
	}

	// The plan says how many fields the account may have, and the next one
	// past it is refused, naming the limit.
	_, p := do(request(http.MethodGet, "/v1/plan"))
	limit, _ := p["contact_field_limit"].(float64)
	if limit != 20 {
		t.Fatalf("contact_field_limit = %v, want 20", p["contact_field_limit"])
	}
	for i := range int(limit) {
		if code, b := do(jsonRequest(http.MethodPost, "/v1/contact-fields", fmt.Sprintf(`{"name":"Field %d"}`, i))); code != http.StatusCreated {
			t.Fatalf("field %d: %d %v", i, code, b)
		}
	}
	code, b := do(jsonRequest(http.MethodPost, "/v1/contact-fields", `{"name":"One more"}`))
	if code != http.StatusConflict || errCode(b) != "field_limit" || !strings.Contains(b["error"].(map[string]any)["message"].(string), "20") {
		t.Errorf("past the limit: %d %v", code, b)
	}
}
