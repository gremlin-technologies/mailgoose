package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

var addedEmails sync.Map // *testing.T -> email id

// addedEmail creates a blocks email through h with the test's key and keeps
// its id for emailPath.
func addedEmail(t *testing.T, h http.Handler) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, bearer(jsonRequest(http.MethodPost, "/v1/emails", everyBlockEmail))(t))
	if rec.Code != http.StatusCreated {
		t.Fatalf("add email: %d %s", rec.Code, rec.Body)
	}
	var e struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatal(err)
	}
	addedEmails.Store(t, e.ID)
	t.Cleanup(func() { addedEmails.Delete(t) })
}

// emailPath is a request to /v1/emails/{the test's email}{suffix}, or to
// one that doesn't exist when the test added none.
func emailPath(method, suffix, body string) func(*testing.T) *http.Request {
	return func(t *testing.T) *http.Request {
		id := noSuchID
		if v, ok := addedEmails.Load(t); ok {
			id = v.(string)
		}
		if body != "" {
			return jsonRequest(method, "/v1/emails/"+id+suffix, body)(t)
		}
		return httptest.NewRequest(method, "/v1/emails/"+id+suffix, nil)
	}
}

// everyBlockEmail creates an email holding one block of every type.
const everyBlockEmail = `{"name":"Spring","document":{"settings":{"subject":"Spring menu","from_email":"news@bakery.example"},"blocks":[
  {"type":"heading","text":"Hello {{first_name|there}}"},
  {"type":"text","text":"**Fresh** bread.\n\n- rye\n- spelt"},
  {"type":"image","src":"https://cdn.example/bread.jpg","alt":"Bread","href":"https://bakery.example","width":300},
  {"type":"button","text":"Order","href":"https://bakery.example/order"},
  {"type":"divider"},
  {"type":"spacer","height":16},
  {"type":"columns","columns":[{"blocks":[{"type":"text","text":"Left"}]},{"blocks":[]}]},
  {"type":"social","links":[{"network":"instagram","url":"https://instagram.com/bakery"}]}
]}}`

const blocksDoc = `{"blocks":[{"type":"heading","text":"Hi"}]}`

func TestEmailsFlow(t *testing.T) {
	s := draftKey(authServer)(t)
	h := s.Handler()
	do := func(req func(*testing.T) *http.Request) (int, map[string]any, []byte) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, bearer(req)(t))
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec.Code, body, rec.Body.Bytes()
	}

	code, created, _ := do(jsonRequest(http.MethodPost, "/v1/emails", everyBlockEmail))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, created)
	}
	id := created["id"].(string)
	doc := created["document"].(map[string]any)
	blocks := doc["blocks"].([]any)
	if len(blocks) != 9 || blocks[8].(map[string]any)["type"] != "footer" || created["version"].(float64) != 1 {
		t.Fatalf("created = %v", created)
	}
	for _, b := range blocks {
		if !strings.HasPrefix(b.(map[string]any)["id"].(string), "b_") {
			t.Errorf("block without a new id: %v", b)
		}
	}

	// get --json, then replace with what it gave: the document is unchanged.
	_, got, raw := do(request(http.MethodGet, "/v1/emails/"+id))
	gotDoc, _ := json.Marshal(got["document"])
	replace, _ := json.Marshal(map[string]any{"version": got["version"], "document": got["document"]})
	code, replaced, _ := do(jsonRequest(http.MethodPut, "/v1/emails/"+id+"/document", string(replace)))
	if code != http.StatusOK || replaced["version"].(float64) != 2 {
		t.Fatalf("replace: %d %v", code, replaced)
	}
	again, _ := json.Marshal(replaced["document"])
	if string(again) != string(gotDoc) {
		t.Errorf("round trip changed the document:\n%s\n%s", gotDoc, again)
	}
	if !strings.Contains(string(raw), `"schema_version":1`) {
		t.Errorf("document without schema_version: %s", raw)
	}

	// The same version again is stale now.
	code, conflict, _ := do(jsonRequest(http.MethodPut, "/v1/emails/"+id+"/document", string(replace)))
	if code != http.StatusConflict || conflict["error"].(map[string]any)["details"].(map[string]any)["current_version"].(float64) != 2 {
		t.Errorf("stale replace: %d %v", code, conflict)
	}

	// Unknown block types and fields come back with where they are.
	code, bad, _ := do(jsonRequest(http.MethodPut, "/v1/emails/"+id+"/document",
		`{"version":2,"document":{"blocks":[{"type":"text","text":"x"},{"type":"carousel"}]}}`))
	if code != http.StatusUnprocessableEntity || bad["error"].(map[string]any)["details"].(map[string]any)["path"] != "blocks[1].type" {
		t.Errorf("unknown block: %d %v", code, bad)
	}
	code, bad, _ = do(jsonRequest(http.MethodPost, "/v1/emails", `{"name":"x","kind":"html","document":`+blocksDoc+`}`))
	if code != http.StatusUnprocessableEntity || bad["error"].(map[string]any)["code"] != "invalid_document" {
		t.Errorf("blocks document for an html email: %d %v", code, bad)
	}

	code, updated, _ := do(jsonRequest(http.MethodPatch, "/v1/emails/"+id, `{"version":2,"name":"Spring v2","settings":{"subject":"Spring is here"}}`))
	st := updated["document"].(map[string]any)["settings"].(map[string]any)
	if code != http.StatusOK || updated["name"] != "Spring v2" || st["subject"] != "Spring is here" || st["from_email"] != "news@bakery.example" {
		t.Errorf("update: %d %v", code, updated)
	}

	code, dup, _ := do(request(http.MethodPost, "/v1/emails/"+id+"/duplicate"))
	if code != http.StatusCreated || dup["name"] != "Copy of Spring v2" {
		t.Errorf("duplicate: %d %v", code, dup)
	}
	code, named, _ := do(jsonRequest(http.MethodPost, "/v1/emails/"+id+"/duplicate", `{"name":"Summer"}`))
	if code != http.StatusCreated || named["name"] != "Summer" {
		t.Errorf("duplicate with a name: %d %v", code, named)
	}

	code, html, _ := do(jsonRequest(http.MethodPost, "/v1/emails", `{"name":"Plain","kind":"html"}`))
	if code != http.StatusCreated || html["document"].(map[string]any)["html"] != "" {
		t.Errorf("html email: %d %v", code, html)
	}

	code, list, _ := do(request(http.MethodGet, "/v1/emails?limit=2"))
	data := list["data"].([]any)
	if code != http.StatusOK || len(data) != 2 || list["next_cursor"] == nil || data[0].(map[string]any)["id"] != html["id"] {
		t.Fatalf("list: %d %v", code, list)
	}
	code, rest, _ := do(request(http.MethodGet, "/v1/emails?cursor="+list["next_cursor"].(string)))
	if code != http.StatusOK || len(rest["data"].([]any)) != 2 {
		t.Errorf("second page: %d %v", code, rest)
	}
	first := rest["data"].([]any)[1].(map[string]any)
	if first["id"] != id || first["subject"] != "Spring is here" || first["block_count"].(float64) != 9 {
		t.Errorf("summary = %v", first)
	}

	if code, _, _ := do(request(http.MethodDelete, "/v1/emails/"+id)); code != http.StatusNoContent {
		t.Errorf("delete: %d", code)
	}
	if code, _, _ := do(request(http.MethodGet, "/v1/emails/"+id)); code != http.StatusNotFound {
		t.Errorf("get after delete: %d", code)
	}
}
