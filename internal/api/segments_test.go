package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

var addedSegments sync.Map // *testing.T -> segment id

// withTag is a request whose body has the test's tag id in place of TAG.
func withTag(method, path, body string) func(*testing.T) *http.Request {
	return func(t *testing.T) *http.Request {
		id := "00000000-0000-4000-8000-000000000000"
		if v, ok := addedTags.Load(t); ok {
			id = v.(string)
		}
		return jsonRequest(method, path, strings.ReplaceAll(body, "TAG", id))(t)
	}
}

// addedSegment adds a tag and a segment of the subscribed contacts with it,
// named VIPs, and keeps the segment's id for segmentPath.
func addedSegment(t *testing.T, h http.Handler) {
	t.Helper()
	addedTag(t, h)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, bearer(withTag(http.MethodPost, "/v1/segments", `{"name":"VIPs","conditions":{"tag_ids":["TAG"],"statuses":["subscribed"]}}`))(t))
	if rec.Code != http.StatusCreated {
		t.Fatalf("add segment: %d %s", rec.Code, rec.Body)
	}
	var sg struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &sg); err != nil {
		t.Fatal(err)
	}
	addedSegments.Store(t, sg.ID)
	t.Cleanup(func() { addedSegments.Delete(t) })
}

// brokenSegment is addedSegment with its tag deleted since.
func brokenSegment(t *testing.T, h http.Handler) {
	t.Helper()
	addedSegment(t, h)
	send(bearer(tagPath(http.MethodDelete, "", "")))(t, h)
}

// segmentPath is a request to /v1/segments/{the test's segment}{suffix}, or
// to a segment that doesn't exist when the test added none.
func segmentPath(method, suffix, body string) func(*testing.T) *http.Request {
	return func(t *testing.T) *http.Request {
		id := "00000000-0000-4000-8000-000000000000"
		if v, ok := addedSegments.Load(t); ok {
			id = v.(string)
		}
		if body != "" {
			return withTag(method, "/v1/segments/"+id+suffix, body)(t)
		}
		return httptest.NewRequest(method, "/v1/segments/"+id+suffix, nil)
	}
}

func TestSegmentsFlow(t *testing.T) {
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
	addedTag(t, h)
	for _, c := range []string{
		`{"email":"ann@example.com","status":"subscribed"}`,
		`{"email":"bo@example.com","status":"subscribed"}`,
		`{"email":"cy@example.com","status":"unsubscribed"}`,
	} {
		if code, body := do(jsonRequest(http.MethodPost, "/v1/contacts", c)); code != http.StatusCreated {
			t.Fatalf("add contact: %d %v", code, body)
		}
	}
	if code, body := do(tagPath(http.MethodPost, "/contacts", `{"filter":{"email":"ann"}}`)); code != http.StatusOK {
		t.Fatalf("tag: %d %v", code, body)
	}
	if code, body := do(tagPath(http.MethodPost, "/contacts", `{"filter":{"email":"cy"}}`)); code != http.StatusOK {
		t.Fatalf("tag: %d %v", code, body)
	}

	code, sg := do(withTag(http.MethodPost, "/v1/segments", `{"name":"VIPs","conditions":{"tag_ids":["TAG"],"statuses":["subscribed","unconfirmed"]}}`))
	if code != http.StatusCreated || sg["valid"] != true || len(sg["problems"].([]any)) != 0 {
		t.Fatalf("create: %d %v", code, sg)
	}
	id := sg["id"].(string)
	members := func() (int, map[string]any) {
		return do(func(t *testing.T) *http.Request {
			return httptest.NewRequest(http.MethodGet, "/v1/segments/"+id+"/contacts", nil)
		})
	}
	code, page := members()
	if code != http.StatusOK || page["count"] != float64(1) || len(page["data"].([]any)) != 1 ||
		page["data"].([]any)[0].(map[string]any)["email"] != "ann@example.com" {
		t.Fatalf("contacts: %d %v", code, page)
	}

	// New conditions replace the old ones.
	code, sg = do(func(t *testing.T) *http.Request {
		return jsonRequest(http.MethodPatch, "/v1/segments/"+id, `{"conditions":{"statuses":["subscribed"]}}`)(t)
	})
	if code != http.StatusOK || sg["name"] != "VIPs" || sg["conditions"].(map[string]any)["tag_ids"] != nil {
		t.Fatalf("update: %d %v", code, sg)
	}
	if code, page = members(); code != http.StatusOK || page["count"] != float64(2) {
		t.Fatalf("contacts after update: %d %v", code, page)
	}

	// Back to the tag, then delete it: the segment says which condition
	// broke, and won't list contacts.
	if code, sg = do(func(t *testing.T) *http.Request {
		return withTag(http.MethodPatch, "/v1/segments/"+id, `{"name":"Subscribed VIPs","conditions":{"tag_ids":["TAG"]}}`)(t)
	}); code != http.StatusOK {
		t.Fatalf("update: %d %v", code, sg)
	}
	tagID, _ := addedTags.Load(t)
	if code, body := do(tagPath(http.MethodDelete, "", "")); code != http.StatusOK {
		t.Fatalf("delete tag: %d %v", code, body)
	}
	code, sg = do(func(t *testing.T) *http.Request { return httptest.NewRequest(http.MethodGet, "/v1/segments/"+id, nil) })
	problems, _ := sg["problems"].([]any)
	if code != http.StatusOK || sg["valid"] != false || len(problems) != 1 {
		t.Fatalf("get: %d %v", code, sg)
	}
	if p := problems[0].(map[string]any); p["condition"] != "tag_ids" || p["id"] != tagID || p["code"] != "tag_deleted" {
		t.Errorf("problem %v", p)
	}
	code, body := members()
	if e, _ := body["error"].(map[string]any); code != http.StatusUnprocessableEntity || e["code"] != "segment_invalid" {
		t.Fatalf("contacts of an invalid segment: %d %v", code, body)
	}

	code, list := do(func(t *testing.T) *http.Request {
		return httptest.NewRequest(http.MethodGet, "/v1/segments?name=subscribed+vips", nil)
	})
	if code != http.StatusOK || len(list["data"].([]any)) != 1 {
		t.Fatalf("list: %d %v", code, list)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, bearer(func(t *testing.T) *http.Request {
		return httptest.NewRequest(http.MethodDelete, "/v1/segments/"+id, nil)
	})(t))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if code, _ := do(func(t *testing.T) *http.Request { return httptest.NewRequest(http.MethodGet, "/v1/segments/"+id, nil) }); code != http.StatusNotFound {
		t.Errorf("get after delete: %d", code)
	}
}

// segmentByCampaign is a segment request with the test's campaign, or one
// that doesn't exist, in the condition named.
func segmentByCampaign(method, path, condition string) func(*testing.T) *http.Request {
	return func(t *testing.T) *http.Request {
		id := "7a1b5f7e-0000-4000-8000-000000000000"
		if v, ok := addedCampaigns.Load(t); ok {
			id = v.(string)
		}
		return jsonRequest(method, path, `{"name":"By campaign","conditions":{"`+condition+`":["`+id+`"]}}`)(t)
	}
}
