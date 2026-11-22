package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// contactsByCampaign is GET path with filter set to the test's campaign,
// or to a campaign that doesn't exist when the test added none, then rest.
func contactsByCampaign(path, filter, rest string) func(*testing.T) *http.Request {
	return func(t *testing.T) *http.Request {
		id := "7a1b5f7e-0000-4000-8000-000000000000"
		if v, ok := addedCampaigns.Load(t); ok {
			id = v.(string)
		}
		return httptest.NewRequest(http.MethodGet, path+"?"+filter+"="+id+rest, nil)
	}
}

// A campaign id in capitals is the same campaign: ids are stored lower case.
func TestContactFiltersTakeUpperCaseIDs(t *testing.T) {
	s := draftKey(authServer)(t)
	h := s.Handler()
	addedCampaign(t, h)
	id, _ := addedCampaigns.Load(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, bearer(request(http.MethodGet, "/v1/contacts/count?opened="+strings.ToUpper(id.(string))))(t))
	if rec.Code != http.StatusOK {
		t.Errorf("count: %d %s", rec.Code, rec.Body)
	}
}

// MG-116: tag_id and status repeat, as a segment's tags and statuses do:
// every tag must be on the contact, and any one status will do.
func TestContactFiltersRepeatTagsAndStatuses(t *testing.T) {
	s := draftKey(authServer)(t)
	h := s.Handler()
	do := func(method, path, body string) (int, map[string]any) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, bearer(jsonRequest(method, path, body))(t))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	tag := func(name string) string {
		t.Helper()
		code, body := do(http.MethodPost, "/v1/tags", `{"name":"`+name+`"}`)
		if code != http.StatusCreated {
			t.Fatalf("tag: %d %v", code, body)
		}
		return body["id"].(string)
	}
	vip, gold := tag("vip"), tag("gold")
	for _, c := range []string{
		`{"email":"ann@example.com","status":"subscribed"}`,
		`{"email":"bo@example.com","status":"unconfirmed"}`,
		`{"email":"cy@example.com","status":"unsubscribed"}`,
	} {
		if code, body := do(http.MethodPost, "/v1/contacts", c); code != http.StatusCreated {
			t.Fatalf("contact: %d %v", code, body)
		}
	}
	for id, who := range map[string]string{vip: "example.com", gold: "ann"} {
		if code, body := do(http.MethodPost, "/v1/tags/"+id+"/contacts", `{"filter":{"email":"`+who+`"}}`); code != http.StatusOK {
			t.Fatalf("tag contacts: %d %v", code, body)
		}
	}
	for query, want := range map[string]float64{
		"tag_id=" + vip:                                             3,
		"tag_id=" + vip + "&tag_id=" + gold:                         1,
		"status=subscribed":                                         1,
		"status=subscribed&status=unconfirmed":                      2,
		"tag_id=" + vip + "&status=unconfirmed&status=unsubscribed": 2,
	} {
		code, body := do(http.MethodGet, "/v1/contacts/count?"+query, "")
		if code != http.StatusOK || body["count"] != want {
			t.Errorf("%s: %d %v, want count %v", query, code, body, want)
		}
	}
	code, list := do(http.MethodGet, "/v1/contacts?tag_id="+vip+"&tag_id="+gold, "")
	if data, _ := list["data"].([]any); code != http.StatusOK || len(data) != 1 {
		t.Errorf("list: %d %v", code, list)
	}
	if code, body := do(http.MethodGet, "/v1/contacts/count?status=subscribed&status=gone", ""); code != http.StatusBadRequest {
		t.Errorf("a bad status: %d %v", code, body)
	}
}
