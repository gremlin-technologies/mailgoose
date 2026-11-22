package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gremlin-technologies/mailgoose/internal/auth"
)

// campaignReportPath is GET /v1/campaigns/{the test's campaign}/report.
func campaignReportPath() func(*testing.T) *http.Request {
	return func(t *testing.T) *http.Request {
		r := campaignPath(http.MethodGet, "")(t)
		r.URL.Path += "/report"
		return r
	}
}

func TestCampaignReport(t *testing.T) {
	f := newTrackingFixture(t)
	ctx := context.Background()
	// Two clicks on link 1 and an open, as tracked; the API counts them.
	for _, path := range []string{
		f.links.Click(f.rcpt, 1, "https://example.com/a"),
		f.links.Click(f.rcpt, 1, "https://example.com/a"),
		f.links.Open(f.rcpt),
	} {
		f.get(path)
	}
	var acc, campaign, key string
	if err := f.db.QueryRow(ctx, `SELECT account_id, campaign_id FROM campaign_recipients WHERE id = $1`, f.rcpt).Scan(&acc, &campaign); err != nil {
		t.Fatal(err)
	}
	var user string
	if _, err := f.db.Exec(ctx, `UPDATE accounts SET status = 'active' WHERE id = $1`, acc); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(ctx, `INSERT INTO users (account_id, email) VALUES ($1, 'pat@example.com') RETURNING id`, acc).Scan(&user); err != nil {
		t.Fatal(err)
	}
	k, err := f.srv.Auth.CreateKey(ctx, acc, user, "test", auth.ScopeDraft)
	if err != nil {
		t.Fatal(err)
	}
	key = k.Secret
	r := httptest.NewRequest(http.MethodGet, "/v1/campaigns/"+campaign+"/report", nil)
	r.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var rep struct {
		Recipients int `json:"recipients"`
		Opens      struct {
			Unique    int  `json:"unique"`
			Estimated bool `json:"estimated"`
		} `json:"opens"`
		Clicks struct{ Unique, Total int } `json:"clicks"`
		Links  []struct {
			Position    int    `json:"position"`
			URL         string `json:"url"`
			TotalClicks int    `json:"total_clicks"`
		} `json:"links"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Recipients != 1 || rep.Opens.Unique != 1 || !rep.Opens.Estimated || rep.Clicks.Unique != 1 || rep.Clicks.Total != 2 ||
		len(rep.Links) != 1 || rep.Links[0].URL != "https://example.com/a" || rep.Links[0].TotalClicks != 2 {
		t.Errorf("report %s", rec.Body)
	}

	// MG-111: the campaign list gives the launched campaign the report's
	// numbers, and a draft none.
	if _, err := f.db.Exec(ctx, `UPDATE campaigns SET launched_at = now() WHERE id = $1`, campaign); err != nil {
		t.Fatal(err)
	}
	var draft string
	if err := f.db.QueryRow(ctx, `INSERT INTO campaigns (account_id, name, all_contacts) VALUES ($1, 'Draft', true) RETURNING id`, acc).Scan(&draft); err != nil {
		t.Fatal(err)
	}
	r = httptest.NewRequest(http.MethodGet, "/v1/campaigns", nil)
	r.Header.Set("Authorization", "Bearer "+key)
	rec = httptest.NewRecorder()
	f.h.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	var list struct {
		Data []struct {
			ID      string          `json:"id"`
			Summary json.RawMessage `json:"summary"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	summaries := map[string]string{}
	for _, c := range list.Data {
		summaries[c.ID] = string(c.Summary)
	}
	if want := `{"recipients":1,"delivered":0,"bounced":0,"opens":1,"clicks":1,"delivered_rate":0,"bounce_rate":0,"open_rate":0,"click_rate":0,"archived":false}`; summaries[campaign] != want {
		t.Errorf("summary = %s, want %s", summaries[campaign], want)
	}
	if summaries[draft] != "null" {
		t.Errorf("draft summary = %q in %s", summaries[draft], rec.Body)
	}
}
