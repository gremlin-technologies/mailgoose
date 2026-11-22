package api

import (
	"net/http"
	"net/http/httptest"
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
