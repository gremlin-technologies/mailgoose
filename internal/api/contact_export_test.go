package api

import (
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gremlin-technologies/mailgoose/internal/auth"
)

func TestExportDataAndEraseThroughTheAPI(t *testing.T) {
	s := draftKey(authServer)(t)
	h := importRunner{s.Handler(), s.Contacts}
	addedTag(t, h)
	rec := do(t, h, bearer(jsonRequest(http.MethodPost, "/v1/imports",
		`{"list_source":"form","csv":"email,first_name,tags\npat@example.com,Pat,\"vip, friends\"\nsam@example.com,=1+1,\nPAT@example.com,Again,\n","permission_confirmed":true}`))(t))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("import: %d %s", rec.Code, rec.Body)
	}

	// Export.
	rec = do(t, h, bearer(request(http.MethodGet, "/v1/contacts/export"))(t))
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/csv") ||
		!strings.Contains(rec.Header().Get("Content-Disposition"), "contacts.csv") {
		t.Fatalf("export: %d %v", rec.Code, rec.Header())
	}
	recs, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil || len(recs) != 3 {
		t.Fatalf("export = %q, %v", recs, err)
	}
	if got := strings.Join(recs[1][:5], "|") + "|" + recs[1][8]; got != "pat@example.com|Pat||subscribed|friends, vip|true" {
		t.Errorf("pat's row = %s", got)
	}
	if recs[2][1] != "'=1+1" {
		t.Errorf("formula not guarded: %q", recs[2][1])
	}
	var vip struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	decodeInto(t, do(t, h, bearer(request(http.MethodGet, "/v1/tags?name=vip"))(t)), &vip)
	rec = do(t, h, bearer(request(http.MethodGet, "/v1/contacts/export?tag_id="+vip.Data[0].ID))(t))
	if strings.Count(rec.Body.String(), "\n") != 2 || strings.Contains(rec.Body.String(), "sam@") {
		t.Errorf("export of one tag = %s", rec.Body)
	}

	// Data request.
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	decodeInto(t, do(t, h, bearer(request(http.MethodGet, "/v1/contacts?email=pat"))(t)), &list)
	pat := "/v1/contacts/" + list.Data[0].ID
	var data struct {
		Contact struct {
			Email string `json:"email"`
		} `json:"contact"`
		Tags   []map[string]any `json:"tags"`
		Import *struct {
			ID                  string `json:"id"`
			PermissionConfirmed bool   `json:"permission_confirmed"`
			ListSource          string `json:"list_source"`
		} `json:"import"`
		Consents []struct {
			Source   string         `json:"source"`
			IP       *string        `json:"ip"`
			ImportID *string        `json:"import_id"`
			Wording  map[string]any `json:"wording"`
		} `json:"consents"`
		SkippedRows []struct {
			Row    int    `json:"row"`
			Reason string `json:"reason"`
		} `json:"skipped_rows"`
	}
	rec = do(t, h, bearer(request(http.MethodGet, pat+"/data"))(t))
	decodeInto(t, rec, &data)
	if rec.Code != http.StatusOK || data.Contact.Email != "pat@example.com" || len(data.Tags) != 2 || data.Import == nil ||
		!data.Import.PermissionConfirmed || data.Import.ListSource != "form" || len(data.SkippedRows) != 1 || data.SkippedRows[0].Reason != "duplicate" {
		t.Errorf("data = %d %+v", rec.Code, data)
	}

	// The import's list source is the contact's consent record (MG-63).
	if len(data.Consents) != 1 || data.Consents[0].Source != "import" || data.Consents[0].IP != nil ||
		data.Consents[0].ImportID == nil || *data.Consents[0].ImportID != data.Import.ID || data.Consents[0].Wording["list_source"] != "form" {
		t.Errorf("consents = %+v", data.Consents)
	}

	// Erase, then nothing is left but the suppression.
	if rec := do(t, h, bearer(request(http.MethodDelete, pat+"?erase=true"))(t)); rec.Code != http.StatusNoContent {
		t.Fatalf("erase: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, bearer(request(http.MethodGet, pat+"/data"))(t)); rec.Code != http.StatusNotFound {
		t.Errorf("data after erase: %d", rec.Code)
	}
	rec = do(t, h, bearer(jsonRequest(http.MethodPost, "/v1/contacts", `{"email":"pat@example.com","status":"subscribed"}`))(t))
	if !strings.Contains(rec.Body.String(), `"status":"unsubscribed"`) {
		t.Errorf("re-added after erase: %s", rec.Body)
	}
}

func TestExportAndDataAreKeptPerAccount(t *testing.T) {
	s := authServer(t)
	h := s.Handler()
	mine := newTestKey(t, s, "mine@example.com", auth.ScopeFull)
	theirs := newTestKey(t, s, "theirs@example.com", auth.ScopeFull)
	as := func(k testKey, req *http.Request) *httptest.ResponseRecorder {
		req.Header.Set("Authorization", "Bearer "+k.secret)
		return do(t, h, req)
	}
	rec := as(theirs, jsonRequest(http.MethodPost, "/v1/contacts", `{"email":"theirs@example.org"}`)(t))
	var c struct {
		ID string `json:"id"`
	}
	decodeInto(t, rec, &c)
	if rec := as(mine, httptest.NewRequest(http.MethodGet, "/v1/contacts/export", nil)); strings.Contains(rec.Body.String(), "theirs@") {
		t.Error("export shows another account's contact")
	}
	if rec := as(mine, httptest.NewRequest(http.MethodGet, "/v1/contacts/"+c.ID+"/data", nil)); rec.Code != http.StatusNotFound {
		t.Errorf("data of another account's contact: %d", rec.Code)
	}
	if rec := as(mine, httptest.NewRequest(http.MethodDelete, "/v1/contacts/"+c.ID+"?erase=true", nil)); rec.Code != http.StatusNotFound {
		t.Errorf("erase of another account's contact: %d", rec.Code)
	}
}
