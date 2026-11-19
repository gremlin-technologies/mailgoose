package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// campaignAction is a POST to /v1/campaigns/{id}/<action> for the test's
// campaign.
func campaignAction(action, body string) func(*testing.T) *http.Request {
	return func(t *testing.T) *http.Request {
		r := campaignPath(http.MethodPost, body)(t)
		r.URL.Path += "/" + action
		return r
	}
}

// scheduledAt is when scheduledCampaign's campaign sends.
var scheduledAt = time.Now().Add(72 * time.Hour).UTC().Truncate(time.Second)

// rescheduleBody moves scheduledCampaign's campaign a day later.
var rescheduleBody = `{"send_at":"` + scheduledAt.Add(24*time.Hour).Format(time.RFC3339) + `"}`

// scheduledCampaign is readyCampaign given a send time, tested, with a
// contact and credits, and launched: scheduled.
func scheduledCampaign(t *testing.T, h http.Handler) {
	t.Helper()
	readyCampaign(t, h)
	steps := []struct {
		req    func(*testing.T) *http.Request
		status int
	}{
		{campaignPath(http.MethodPatch, `{"send_at":"`+scheduledAt.Format(time.RFC3339)+`"}`), http.StatusOK},
		{campaignTest(""), http.StatusOK},
	}
	for _, s := range steps {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, bearer(s.req)(t))
		if rec.Code != s.status {
			t.Fatalf("%s: %d %s", s.req(t).URL.Path, rec.Code, rec.Body)
		}
	}
	addedContact(t, h)
	id, _ := addedCampaigns.Load(t)
	v, _ := testDBs.Load(t)
	if _, err := v.(*pgxpool.Pool).Exec(t.Context(), `
		INSERT INTO credit_transactions (account_id, type, amount, expires_at)
		SELECT account_id, 'purchase', 100, now() + interval '1 year' FROM campaigns WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, bearer(campaignLaunch(""))(t))
	if rec.Code != http.StatusConflict {
		t.Fatalf("summary: %d %s", rec.Code, rec.Body)
	}
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, bearer(campaignLaunch(`{"confirm":"`+launchCode(t, rec)+`"}`))(t))
	if rec2.Code != http.StatusOK || !strings.Contains(rec2.Body.String(), `"status":"scheduled"`) {
		t.Fatalf("launch: %d %s", rec2.Code, rec2.Body)
	}
}

func TestCancelAndRescheduleCampaign(t *testing.T) {
	srv := authServer(t)
	h := srv.Handler()
	fullKey(func(*testing.T) *Server { return srv })(t)
	serve := func(r func(*testing.T) *http.Request) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, bearer(r)(t))
		return rec
	}
	readyCampaign(t, h)
	// A draft has nothing to cancel or move.
	if rec := serve(campaignAction("cancel", "")); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"status":"draft"`) {
		t.Errorf("cancel draft: %d %s", rec.Code, rec.Body)
	}

	srv = authServer(t)
	h = srv.Handler()
	fullKey(func(*testing.T) *Server { return srv })(t)
	scheduledCampaign(t, h)
	at := scheduledAt

	// Moved: the body takes any offset; the answer is in UTC.
	later := at.Add(24 * time.Hour)
	rec := serve(campaignAction("reschedule", `{"send_at":"`+later.In(time.FixedZone("", -6*3600)).Format(time.RFC3339)+`"}`))
	var c struct {
		Status string `json:"status"`
		SendAt string `json:"send_at"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &c) != nil || c.Status != "scheduled" || c.SendAt != later.Format(time.RFC3339) {
		t.Errorf("reschedule: %d %s", rec.Code, rec.Body)
	}
	for _, body := range []string{`{"send_at":"2020-01-01T00:00:00Z"}`, `{}`, `{"send_at":"soon"}`, `{"send_at":"` + later.Format(time.RFC3339) + `","x":1}`} {
		rec := serve(campaignAction("reschedule", body))
		if rec.Code != http.StatusUnprocessableEntity && rec.Code != http.StatusBadRequest {
			t.Errorf("reschedule %s: %d %s", body, rec.Code, rec.Body)
		}
	}
	if rec := serve(campaignAction("reschedule", `{"send_at":"2020-01-01T00:00:00Z"}`)); !strings.Contains(rec.Body.String(), `"field":"send_at"`) {
		t.Errorf("past: %s", rec.Body)
	}

	// Cancelled, twice; then it can't be moved.
	for range 2 {
		if rec := serve(campaignAction("cancel", "")); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"cancelled"`) {
			t.Errorf("cancel: %d %s", rec.Code, rec.Body)
		}
	}
	if rec := serve(campaignAction("reschedule", `{"send_at":"`+later.Format(time.RFC3339)+`"}`)); rec.Code != http.StatusConflict {
		t.Errorf("move cancelled: %d %s", rec.Code, rec.Body)
	}
	if rec := serve(func(t *testing.T) *http.Request { return httptest.NewRequest(http.MethodGet, "/v1/credits", nil) }); !strings.Contains(rec.Body.String(), `"balance":100`) {
		t.Errorf("credits after cancel: %s", rec.Body)
	}
}
