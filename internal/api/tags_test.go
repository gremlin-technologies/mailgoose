package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

var addedTags sync.Map // *testing.T -> tag id

// addedTag creates the tag "vip" through h with the test's key and keeps
// its id for tagPath.
func addedTag(t *testing.T, h http.Handler) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, bearer(jsonRequest(http.MethodPost, "/v1/tags", `{"name":"vip"}`))(t))
	if rec.Code != http.StatusCreated {
		t.Fatalf("add tag: %d %s", rec.Code, rec.Body)
	}
	var tg struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &tg); err != nil {
		t.Fatal(err)
	}
	addedTags.Store(t, tg.ID)
	t.Cleanup(func() { addedTags.Delete(t) })
}

// tagPath is a request to /v1/tags/{the test's tag}{suffix}, or to a tag
// that doesn't exist when the test added none.
func tagPath(method, suffix, body string) func(*testing.T) *http.Request {
	return func(t *testing.T) *http.Request {
		id := "00000000-0000-4000-8000-000000000000"
		if v, ok := addedTags.Load(t); ok {
			id = v.(string)
		}
		if body != "" {
			return jsonRequest(method, "/v1/tags/"+id+suffix, body)(t)
		}
		return httptest.NewRequest(method, "/v1/tags/"+id+suffix, nil)
	}
}

func TestTagsFlow(t *testing.T) {
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
	id := func(body map[string]any) string { return body["id"].(string) }

	_, vip := do(jsonRequest(http.MethodPost, "/v1/tags", `{"name":"VIP"}`))
	_, pat := do(jsonRequest(http.MethodPost, "/v1/contacts", `{"email":"pat@example.com"}`))
	_, sam := do(jsonRequest(http.MethodPost, "/v1/contacts", `{"email":"sam@example.org","status":"unconfirmed"}`))
	if code, c := do(jsonRequest(http.MethodPost, "/v1/tags", `{"name":"vip"}`)); code != http.StatusConflict || c["error"].(map[string]any)["code"] != "tag_exists" {
		t.Errorf("duplicate tag: %d %v", code, c)
	}

	code, ch := do(jsonRequest(http.MethodPost, "/v1/tags/"+id(vip)+"/contacts", `{"contact_ids":["`+id(pat)+`"]}`))
	if code != http.StatusOK || ch["matched"] != 1.0 || ch["changed"] != 1.0 {
		t.Fatalf("tag one: %d %v", code, ch)
	}
	if code, ch = do(jsonRequest(http.MethodPost, "/v1/tags/"+id(vip)+"/contacts", `{"filter":{"email":"example"}}`)); code != http.StatusOK || ch["matched"] != 2.0 || ch["changed"] != 1.0 {
		t.Errorf("tag by filter: %d %v", code, ch)
	}
	_, c := do(request(http.MethodGet, "/v1/contacts/"+id(pat)))
	if tags := c["tags"].([]any); len(tags) != 1 || tags[0].(map[string]any)["name"] != "VIP" {
		t.Errorf("contact tags = %v", c["tags"])
	}
	if code, c = do(request(http.MethodGet, "/v1/contacts?tag_id="+id(vip)+"&status=unconfirmed")); code != http.StatusOK || len(c["data"].([]any)) != 1 {
		t.Errorf("list by tag: %d %v", code, c)
	}
	if code, ch = do(jsonRequest(http.MethodPost, "/v1/tags/"+id(vip)+"/contacts/remove", `{"contact_ids":["`+id(sam)+`"]}`)); code != http.StatusOK || ch["changed"] != 1.0 {
		t.Errorf("untag: %d %v", code, ch)
	}
	for _, body := range []string{`{}`, `{"contact_ids":[]}`, `{"contact_ids":["x"]}`, `{"filter":{"status":"gone"}}`, `{"contact_ids":["` + id(pat) + `"],"filter":{}}`} {
		if code, c = do(jsonRequest(http.MethodPost, "/v1/tags/"+id(vip)+"/contacts", body)); code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %v", body, code, c)
		}
	}

	if code, c = do(jsonRequest(http.MethodPatch, "/v1/tags/"+id(vip), `{"name":"Gold"}`)); code != http.StatusOK || c["name"] != "Gold" || c["contact_count"] != 1.0 {
		t.Errorf("rename: %d %v", code, c)
	}
	if code, c = do(request(http.MethodGet, "/v1/tags?name=gold")); code != http.StatusOK || len(c["data"].([]any)) != 1 {
		t.Errorf("list by name: %d %v", code, c)
	}
	if code, c = do(request(http.MethodDelete, "/v1/tags/"+id(vip))); code != http.StatusOK || c["contacts_untagged"] != 1.0 || c["campaigns_updated"] != 0.0 {
		t.Errorf("delete: %d %v", code, c)
	}
	if _, c = do(request(http.MethodGet, "/v1/contacts/"+id(pat))); len(c["tags"].([]any)) != 0 {
		t.Errorf("tags after delete = %v", c["tags"])
	}
}

func TestTagListPages(t *testing.T) {
	s := fullKey(authServer)(t)
	h := s.Handler()
	for _, n := range []string{"b", "A", "c"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, bearer(jsonRequest(http.MethodPost, "/v1/tags", `{"name":"`+n+`"}`))(t))
	}
	var names []string
	path := "/v1/tags?limit=2"
	for path != "" {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, bearer(request(http.MethodGet, path))(t))
		var page struct {
			Data       []struct{ Name string } `json:"data"`
			NextCursor *string                 `json:"next_cursor"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		for _, d := range page.Data {
			names = append(names, d.Name)
		}
		path = ""
		if page.NextCursor != nil {
			path = "/v1/tags?limit=2&cursor=" + *page.NextCursor
		}
	}
	if len(names) != 3 || names[0] != "A" || names[2] != "c" {
		t.Errorf("names = %v", names)
	}
}
