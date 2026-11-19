package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var addedCampaigns sync.Map // *testing.T -> campaign id

// addedCampaign creates an email and a draft campaign for it through h,
// and keeps the campaign's id for campaignPath.
func addedCampaign(t *testing.T, h http.Handler) {
	t.Helper()
	addedEmail(t, h)
	v, _ := addedEmails.Load(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, bearer(jsonRequest(http.MethodPost, "/v1/campaigns", `{"name":"Spring","email_id":"`+v.(string)+`","recipients":{"all":true}}`))(t))
	if rec.Code != http.StatusCreated {
		t.Fatalf("add campaign: %d %s", rec.Code, rec.Body)
	}
	var c struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	addedCampaigns.Store(t, c.ID)
	t.Cleanup(func() { addedCampaigns.Delete(t) })
}

// sendingCampaign is addedCampaign moved on to sending, which no endpoint
// does yet.
func sendingCampaign(t *testing.T, h http.Handler) {
	t.Helper()
	addedCampaign(t, h)
	id, _ := addedCampaigns.Load(t)
	db, _ := testDBs.Load(t)
	if _, err := db.(*pgxpool.Pool).Exec(t.Context(), `UPDATE campaigns SET status = 'sending' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
}

// campaignPath is a request to /v1/campaigns/{the test's campaign}, or to
// one that doesn't exist when the test added none.
func campaignPath(method, body string) func(*testing.T) *http.Request {
	return func(t *testing.T) *http.Request {
		id := noSuchID
		if v, ok := addedCampaigns.Load(t); ok {
			id = v.(string)
		}
		if body != "" {
			return jsonRequest(method, "/v1/campaigns/"+id, body)(t)
		}
		return httptest.NewRequest(method, "/v1/campaigns/"+id, nil)
	}
}

// newCampaign is a create request for the test's email.
func newCampaign(body string) func(*testing.T) *http.Request {
	return func(t *testing.T) *http.Request {
		id := noSuchID
		if v, ok := addedEmails.Load(t); ok {
			id = v.(string)
		}
		return jsonRequest(http.MethodPost, "/v1/campaigns", strings.ReplaceAll(body, "EMAIL", id))(t)
	}
}

type campaignOut struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	EmailID    *string `json:"email_id"`
	Recipients struct {
		All  bool `json:"all"`
		Tags []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"tags"`
	} `json:"recipients"`
	RecipientCount *int       `json:"recipient_count"`
	SendAt         *time.Time `json:"send_at"`
	Status         string     `json:"status"`
	Version        int        `json:"version"`
}

func TestCampaigns(t *testing.T) {
	srv := fullKey(authServer)(t)
	h := srv.Handler()
	do := func(req func(*testing.T) *http.Request) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, bearer(req)(t))
		return rec
	}
	addedEmail(t, h)
	tagID := func(name string) string {
		rec := do(jsonRequest(http.MethodPost, "/v1/tags", `{"name":"`+name+`"}`))
		if rec.Code != http.StatusCreated {
			t.Fatalf("tag: %s", rec.Body)
		}
		return decode[struct {
			ID string `json:"id"`
		}](t, rec).ID
	}
	vip := tagID("VIP")
	for _, c := range []string{`{"email":"a@example.com","status":"subscribed"}`, `{"email":"b@example.com","status":"subscribed"}`,
		`{"email":"c@example.com","status":"unconfirmed"}`} {
		if rec := do(jsonRequest(http.MethodPost, "/v1/contacts", c)); rec.Code != http.StatusCreated {
			t.Fatalf("contact: %s", rec.Body)
		}
	}
	// Only subscribed contacts count.
	all := decode[struct {
		RecipientCount int `json:"recipient_count"`
	}](t, do(jsonRequest(http.MethodPost, "/v1/campaigns/recipient-count", `{"recipients":{"all":true}}`))).RecipientCount
	if all != 2 {
		t.Errorf("count all = %d", all)
	}
	if rec := do(jsonRequest(http.MethodPost, "/v1/campaigns/recipient-count", `{"recipients":{"tag_ids":["`+vip+`"]}}`)); rec.Code != http.StatusOK ||
		decode[map[string]int](t, rec)["recipient_count"] != 0 {
		t.Errorf("count by tag: %d %s", rec.Code, rec.Body)
	}

	at := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	rec := do(newCampaign(`{"name":"Spring","email_id":"EMAIL","recipients":{"tag_ids":["` + vip + `"]},"send_at":"` + at.Format(time.RFC3339) + `"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	c := decode[campaignOut](t, rec)
	if c.Status != "draft" || c.Version != 1 || c.Recipients.All || len(c.Recipients.Tags) != 1 || c.Recipients.Tags[0].Name != "VIP" ||
		c.SendAt == nil || !c.SendAt.Equal(at) || c.RecipientCount == nil || *c.RecipientCount != 0 {
		t.Errorf("created %+v", c)
	}

	// send_at null sends when launched; left out, it stays.
	rec = do(jsonRequest(http.MethodPatch, "/v1/campaigns/"+c.ID, `{"recipients":{"all":true},"send_at":null,"version":1}`))
	u := decode[campaignOut](t, rec)
	if rec.Code != http.StatusOK || u.SendAt != nil || !u.Recipients.All || len(u.Recipients.Tags) != 0 || u.Version != 2 || *u.RecipientCount != all {
		t.Errorf("update: %d %s", rec.Code, rec.Body)
	}
	if rec := do(jsonRequest(http.MethodPatch, "/v1/campaigns/"+c.ID, `{"name":"Renamed"}`)); decode[campaignOut](t, rec).SendAt != nil {
		t.Errorf("rename kept no send time: %s", rec.Body)
	}

	errOf := func(rec *httptest.ResponseRecorder) (string, map[string]any) {
		var e struct {
			Error struct {
				Code    string         `json:"code"`
				Details map[string]any `json:"details"`
			} `json:"error"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &e)
		return e.Error.Code, e.Error.Details
	}
	for body, want := range map[string]string{
		`{"version":1}`:                                         "version_conflict",
		`{"send_at":"2001-01-01T00:00:00Z"}`:                    "invalid_campaign:send_at",
		`{"recipients":{}}`:                                     "invalid_campaign:recipients",
		`{"recipients":{"all":true,"tag_ids":["` + vip + `"]}}`: "invalid_campaign:recipients",
		`{"recipients":{"tag_ids":["` + noSuchID + `"]}}`:       "unknown_tag",
		`{"email_id":"` + noSuchID + `"}`:                       "unknown_email",
		`{"send_at":"tomorrow"}`:                                "invalid_json",
		`{"name":""}`:                                           "invalid_campaign:name",
	} {
		code, details := errOf(do(jsonRequest(http.MethodPatch, "/v1/campaigns/"+c.ID, body)))
		if field, ok := details["field"].(string); ok {
			code += ":" + field
		}
		if code != want {
			t.Errorf("%s: got %s", body, code)
		}
	}
	for body, want := range map[string]string{
		`{"recipients":{}}`: "invalid_campaign",
		`{"recipients":{"tag_ids":["` + noSuchID + `"]}}`:       "unknown_tag",
		`{"recipients":{"tag_ids":["nope"]}}`:                   "unknown_tag",
		`{"recipients":{"all":true,"tag_ids":["` + vip + `"]}}`: "invalid_campaign",
	} {
		if code, _ := errOf(do(jsonRequest(http.MethodPost, "/v1/campaigns/recipient-count", body))); code != want {
			t.Errorf("count %s: got %s", body, code)
		}
	}

	// A campaign past draft can't be edited or deleted.
	if _, err := srv.Campaigns.DB.Exec(t.Context(), `UPDATE campaigns SET status = 'sending' WHERE id = $1`, c.ID); err != nil {
		t.Fatal(err)
	}
	for _, req := range []func(*testing.T) *http.Request{
		jsonRequest(http.MethodPatch, "/v1/campaigns/"+c.ID, `{"name":"x"}`),
		request(http.MethodDelete, "/v1/campaigns/"+c.ID),
	} {
		rec := do(req)
		if code, d := errOf(rec); rec.Code != http.StatusConflict || code != "campaign_locked" || d["status"] != "sending" {
			t.Errorf("locked: %d %s", rec.Code, rec.Body)
		}
	}

	rec = do(request(http.MethodGet, "/v1/campaigns?status=sending"))
	list := decode[struct {
		Data []campaignOut `json:"data"`
	}](t, rec)
	if rec.Code != http.StatusOK || len(list.Data) != 1 || list.Data[0].RecipientCount != nil {
		t.Errorf("list sending: %s", rec.Body)
	}
	if rec := do(request(http.MethodGet, "/v1/campaigns?status=draft")); len(decode[struct {
		Data []campaignOut `json:"data"`
	}](t, rec).Data) != 0 {
		t.Errorf("list draft: %s", rec.Body)
	}
	if rec := do(request(http.MethodGet, "/v1/campaigns?status=done")); rec.Code != http.StatusBadRequest {
		t.Errorf("bad status: %d", rec.Code)
	}
}
