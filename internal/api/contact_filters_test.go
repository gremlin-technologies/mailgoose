package api

import (
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
