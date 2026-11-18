package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// blockOpsEmail is an email whose blocks have known ids, for the block
// operations' contract cases.
const blockOpsEmail = `{"name":"Blocks","document":{"blocks":[
  {"id":"h1","type":"heading","text":"Hi"},
  {"id":"cols","type":"columns","columns":[{"blocks":[{"id":"left","type":"text","text":"Left"}]},{"blocks":[]}]},
  {"id":"foot","type":"footer"}
]}}`

func addedEmailFrom(body string) func(*testing.T, http.Handler) {
	return func(t *testing.T, h http.Handler) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, bearer(jsonRequest(http.MethodPost, "/v1/emails", body))(t))
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
}

var (
	addedBlockEmail = addedEmailFrom(blockOpsEmail)
	addedHTMLEmail  = addedEmailFrom(`{"name":"Plain","kind":"html","document":{"html":"<p>Hi</p><a href=\"{{unsubscribe_url}}\">Unsubscribe</a>"}}`)
)

func TestEmailBlocksFlow(t *testing.T) {
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
	errCode := func(body map[string]any) any {
		e, _ := body["error"].(map[string]any)
		return e["code"]
	}

	_, created := do(jsonRequest(http.MethodPost, "/v1/emails", blockOpsEmail))
	base := "/v1/emails/" + created["id"].(string)

	// Add into the empty right column, then list: the new block comes after
	// the left column's, and the footer stays last.
	code, added := do(jsonRequest(http.MethodPost, base+"/blocks", `{"version":1,"block":{"type":"button","text":"Go","href":"https://x.example"},"parent_id":"cols","column":1}`))
	if code != http.StatusCreated {
		t.Fatalf("add: %d %v", code, added)
	}
	nb := added["block"].(map[string]any)
	newID := nb["block"].(map[string]any)["id"].(string)
	if nb["parent_id"] != "cols" || nb["column"].(float64) != 1 || nb["path"] != "blocks[1].columns[1].blocks[0]" ||
		added["email"].(map[string]any)["version"].(float64) != 2 {
		t.Errorf("added = %v", added)
	}
	code, list := do(request(http.MethodGet, base+"/blocks"))
	var order []string
	for _, e := range list["data"].([]any) {
		order = append(order, e.(map[string]any)["block"].(map[string]any)["id"].(string))
	}
	if code != http.StatusOK || list["version"].(float64) != 2 || len(order) != 5 || order[3] != newID || order[4] != "foot" {
		t.Errorf("list: %d %v", code, order)
	}

	code, upd := do(jsonRequest(http.MethodPatch, base+"/blocks/h1", `{"version":2,"fields":{"text":"Hello","level":2}}`))
	if b := upd["block"].(map[string]any)["block"].(map[string]any); code != http.StatusOK || b["text"] != "Hello" || b["level"].(float64) != 2 {
		t.Errorf("update: %d %v", code, upd)
	}
	code, mv := do(jsonRequest(http.MethodPost, base+"/blocks/h1/move", `{"version":3,"after":"cols"}`))
	if code != http.StatusOK || mv["block"].(map[string]any)["index"].(float64) != 1 {
		t.Errorf("move: %d %v", code, mv)
	}
	code, rm := do(request(http.MethodDelete, base+"/blocks/cols?version=4"))
	if blocks := rm["document"].(map[string]any)["blocks"].([]any); code != http.StatusOK || len(blocks) != 2 {
		t.Errorf("remove: %d %v", code, rm)
	}

	for name, c := range map[string]struct {
		req    func(*testing.T) *http.Request
		status int
		code   string
	}{
		"remove the footer": {request(http.MethodDelete, base+"/blocks/foot?version=5"), http.StatusUnprocessableEntity, "footer_locked"},
		"move the footer":   {jsonRequest(http.MethodPost, base+"/blocks/foot/move", `{"version":5,"index":0}`), http.StatusUnprocessableEntity, "footer_locked"},
		"add after footer":  {jsonRequest(http.MethodPost, base+"/blocks", `{"version":5,"block":{"type":"spacer"},"after":"foot"}`), http.StatusUnprocessableEntity, "footer_locked"},
		"add a footer":      {jsonRequest(http.MethodPost, base+"/blocks", `{"version":5,"block":{"type":"footer"}}`), http.StatusUnprocessableEntity, "footer_locked"},
		"column, no parent": {jsonRequest(http.MethodPost, base+"/blocks", `{"version":5,"block":{"type":"spacer"},"column":0}`), http.StatusUnprocessableEntity, "invalid_position"},
		"bad block field":   {jsonRequest(http.MethodPost, base+"/blocks", `{"version":5,"block":{"type":"spacer","colour":"red"}}`), http.StatusUnprocessableEntity, "invalid_document"},
		"unknown block":     {jsonRequest(http.MethodPatch, base+"/blocks/nope", `{"version":5,"fields":{"text":"x"}}`), http.StatusNotFound, "block_not_found"},
		"change the type":   {jsonRequest(http.MethodPatch, base+"/blocks/h1", `{"version":5,"fields":{"type":"text"}}`), http.StatusUnprocessableEntity, "invalid_document"},
		"stale version":     {jsonRequest(http.MethodPatch, base+"/blocks/h1", `{"version":4,"fields":{"text":"x"}}`), http.StatusConflict, "version_conflict"},
		"no block":          {jsonRequest(http.MethodPost, base+"/blocks", `{"version":5}`), http.StatusBadRequest, "invalid_parameter"},
		"no fields":         {jsonRequest(http.MethodPatch, base+"/blocks/h1", `{"version":5}`), http.StatusBadRequest, "invalid_parameter"},
		"bad block id":      {jsonRequest(http.MethodPatch, base+"/blocks/a.b", `{"version":5,"fields":{"text":"x"}}`), http.StatusBadRequest, "invalid_parameter"},
	} {
		t.Run(name, func(t *testing.T) {
			code, body := do(c.req)
			if code != c.status || errCode(body) != c.code {
				t.Errorf("%d %v, want %d %s", code, body, c.status, c.code)
			}
		})
	}

	_, html := do(jsonRequest(http.MethodPost, "/v1/emails", `{"name":"Plain","kind":"html"}`))
	code, body := do(request(http.MethodGet, "/v1/emails/"+html["id"].(string)+"/blocks"))
	if code != http.StatusUnprocessableEntity || errCode(body) != "kind_mismatch" {
		t.Errorf("html list: %d %v", code, body)
	}
}
