package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

var addedDomains sync.Map // *testing.T -> domain id

// addedDomain adds example.com through h with the test's key and keeps its
// id for domainPath.
func addedDomain(t *testing.T, h http.Handler) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, bearer(jsonRequest(http.MethodPost, "/v1/domains", `{"name":"example.com"}`))(t))
	if rec.Code != http.StatusCreated {
		t.Fatalf("add domain: %d %s", rec.Code, rec.Body)
	}
	var d struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	addedDomains.Store(t, d.ID)
	t.Cleanup(func() { addedDomains.Delete(t) })
}

// domainPath is a request to /v1/domains/{the test's domain}{suffix}, or to
// a domain that doesn't exist when the test added none.
func domainPath(method, suffix string) func(*testing.T) *http.Request {
	return func(t *testing.T) *http.Request {
		id := "00000000-0000-4000-8000-000000000000"
		if v, ok := addedDomains.Load(t); ok {
			id = v.(string)
		}
		return httptest.NewRequest(method, "/v1/domains/"+id+suffix, nil)
	}
}

func TestDomainsFlow(t *testing.T) {
	s := fullKey(authServer)(t)
	h := s.Handler()
	do := func(req func(*testing.T) *http.Request) (int, map[string]any) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, bearer(req)(t))
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec.Code, body
	}

	code, d := do(jsonRequest(http.MethodPost, "/v1/domains", `{"name":"News.Example.com"}`))
	if code != http.StatusCreated || d["name"] != "news.example.com" || d["status"] != "pending" || d["mail_from_domain"] != "bounce.news.example.com" {
		t.Fatalf("create: %d %v", code, d)
	}
	recs := d["records"].([]any)
	if len(recs) != 6 {
		t.Fatalf("records = %v", recs)
	}
	for _, r := range recs {
		if r.(map[string]any)["status"] != "unchecked" {
			t.Errorf("new record should be unchecked: %v", r)
		}
	}
	id := d["id"].(string)

	code, d = do(request(http.MethodPost, "/v1/domains/"+id+"/verify"))
	if code != http.StatusOK || d["status"] != "verified" || d["records_changed"] != false || d["ses_status"] != "success" {
		t.Fatalf("verify: %d %v", code, d)
	}
	for _, r := range d["records"].([]any) {
		rec := r.(map[string]any)
		want := "found"
		if rec["purpose"] == "dmarc" {
			want = "missing" // suggested, and the local DNS has none
		}
		if rec["status"] != want {
			t.Errorf("record %v: status %v, want %s", rec["name"], rec["status"], want)
		}
	}

	if code, d = do(request(http.MethodGet, "/v1/domains?name=news.example.com")); code != http.StatusOK || len(d["data"].([]any)) != 1 {
		t.Errorf("list by name: %d %v", code, d)
	}
	if code, d = do(jsonRequest(http.MethodPost, "/v1/domains", `{"name":"news.example.com"}`)); code != http.StatusConflict ||
		d["error"].(map[string]any)["code"] != "domain_exists" {
		t.Errorf("duplicate: %d %v", code, d)
	}
	if code, d = do(jsonRequest(http.MethodPost, "/v1/domains", `{"name":"https://example.com"}`)); code != http.StatusUnprocessableEntity ||
		d["error"].(map[string]any)["code"] != "invalid_domain" {
		t.Errorf("invalid: %d %v", code, d)
	}
	if code, d = do(jsonRequest(http.MethodPost, "/v1/domains", `{"name":"x.mailgoose.app"}`)); code != http.StatusUnprocessableEntity ||
		!strings.Contains(d["error"].(map[string]any)["code"].(string), "reserved") {
		t.Errorf("reserved: %d %v", code, d)
	}

	if code, _ = do(request(http.MethodDelete, "/v1/domains/"+id)); code != http.StatusNoContent {
		t.Errorf("delete: %d", code)
	}
	if code, _ = do(request(http.MethodGet, "/v1/domains/"+id)); code != http.StatusNotFound {
		t.Errorf("get after delete: %d", code)
	}
}
