package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

type accountBody struct {
	Name           string   `json:"name"`
	FromName       *string  `json:"from_name"`
	FromEmail      *string  `json:"from_email"`
	PostalAddress  *string  `json:"postal_address"`
	TimeZone       string   `json:"time_zone"`
	MissingForSend []string `json:"missing_for_send"`
}

func TestAccountThroughTheAPI(t *testing.T) {
	h := authServer(t).Handler()
	signedIn(t, h)
	read := func(req func(*testing.T) *http.Request, want int) accountBody {
		t.Helper()
		rec := do(t, h, withSession(req)(t))
		if rec.Code != want {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		var b accountBody
		_ = json.Unmarshal(rec.Body.Bytes(), &b)
		return b
	}

	a := read(request(http.MethodGet, "/v1/account"), http.StatusOK)
	if a.TimeZone != "UTC" || len(a.MissingForSend) != 1 || a.MissingForSend[0] != "postal_address" {
		t.Errorf("new account = %+v", a)
	}

	a = read(jsonRequest(http.MethodPatch, "/v1/account", accountUpdate), http.StatusOK)
	if a.Name != "Pat's Bakery" || a.PostalAddress == nil || *a.PostalAddress != "Pat's Bakery\n12 Main St\nSpringfield, IL 62701" ||
		len(a.MissingForSend) != 0 || a.MissingForSend == nil {
		t.Errorf("after update = %+v", a)
	}

	// Only the field sent changes, and null removes it.
	a = read(jsonRequest(http.MethodPatch, "/v1/account", `{"from_email":null}`), http.StatusOK)
	if a.FromEmail != nil || a.FromName == nil || a.TimeZone != "America/Chicago" {
		t.Errorf("after clearing from_email = %+v", a)
	}

	read(jsonRequest(http.MethodPatch, "/v1/account", `{"time_zone":"EST5EDT "}`), http.StatusUnprocessableEntity)
	read(jsonRequest(http.MethodPatch, "/v1/account", `{"plan":"paid"}`), http.StatusBadRequest)
	read(jsonRequest(http.MethodPatch, "/v1/account", `{"name":5}`), http.StatusBadRequest)
}
