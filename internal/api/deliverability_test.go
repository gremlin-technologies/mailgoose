package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// sendingTarget is targetAccount, which sent 1,000 copies of a campaign an
// hour ago: 30 hard bounced and 2 complained, and it's throttled and in test
// mode. One of its send batches took 12 seconds.
func sendingTarget(t *testing.T, h http.Handler) {
	t.Helper()
	targetAccount(t, h)
	id, _ := targetAccounts.Load(t)
	db, _ := testDBs.Load(t)
	pool := db.(*pgxpool.Pool)
	ago := time.Now().Add(-time.Hour)
	var campaign string
	if err := pool.QueryRow(t.Context(), `
		INSERT INTO campaigns (account_id, name, all_contacts, status, launched_at)
		VALUES ($1, 'Sent', true, 'sent', $2) RETURNING id`, id, ago).Scan(&campaign); err != nil {
		t.Fatal(err)
	}
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO campaign_recipients (account_id, campaign_id, status, sent_at, message_id)
			SELECT $1, $2, 'sent', $3, 'm-' || i FROM generate_series(1, 1000) i`, []any{id, campaign, ago}},
		{`INSERT INTO send_events (event_key, account_id, campaign_id, recipient_id, message_id, kind, type, subtype, occurred_at)
			SELECT 'b' || r.id, r.account_id, r.campaign_id, r.id, r.message_id, 'campaign', 'bounce', 'permanent/general', $2
			FROM campaign_recipients r WHERE r.campaign_id = $1 ORDER BY r.id LIMIT 30`, []any{campaign, ago}},
		{`INSERT INTO send_events (event_key, account_id, campaign_id, recipient_id, message_id, kind, type, subtype, occurred_at)
			SELECT 'c' || r.id, r.account_id, r.campaign_id, r.id, r.message_id, 'campaign', 'complaint', 'abuse', $2
			FROM campaign_recipients r WHERE r.campaign_id = $1 ORDER BY r.id DESC LIMIT 2`, []any{campaign, ago}},
		{`INSERT INTO account_throttles (account_id, reason, created_at, until) VALUES ($1, 'x', $2, $3)`,
			[]any{id, ago, ago.Add(7 * 24 * time.Hour)}},
		{`UPDATE accounts SET sending_mode = 'test' WHERE id = $1`, []any{id}},
		// One send batch's timing (MG-91), so send_throughput has numbers.
		{`INSERT INTO send_batch_timings (account_id, campaign_id, started_at, finished_at, batch_size, sent, skipped, failed)
			VALUES ($1, $2, $3, $4, 100, 100, 0, 0)`, []any{id, campaign, ago, ago.Add(12 * time.Second)}},
	} {
		if _, err := pool.Exec(t.Context(), q.sql, q.args...); err != nil {
			t.Fatalf("%s: %v", q.sql, err)
		}
	}
}

func init() {
	const path = "/v1/admin/deliverability"
	contractCases["getDeliverabilityReport"] = []contractCase{
		{name: "found", server: asAdmin(authServer), before: sendingTarget, req: bearer(request(http.MethodGet, path)), status: http.StatusOK},
		{name: "none", server: asAdmin(authServer), req: bearer(request(http.MethodGet, path+"?limit=1")), status: http.StatusOK},
		{name: "bad limit", server: asAdmin(authServer), req: bearer(request(http.MethodGet, path+"?limit=101")), status: http.StatusBadRequest},
		{name: "no credentials", server: authServer, req: request(http.MethodGet, path), status: http.StatusUnauthorized},
		{name: "not an admin", server: fullKey(authServer), req: bearer(request(http.MethodGet, path)), status: http.StatusForbidden},
		{name: "rate limited", server: asAdmin(rateLimitedAuthServer), before: send(bearer(request(http.MethodGet, "/v1/campaigns"))),
			req: bearer(request(http.MethodGet, path)), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: bearer(request(http.MethodGet, path)), status: http.StatusServiceUnavailable},
	}
}

// TestDeliverabilityThroughTheAPI reads the per-customer deliverability
// report as an admin (MG-82).
func TestDeliverabilityThroughTheAPI(t *testing.T) {
	h := asAdmin(authServer)(t).Handler()
	sendingTarget(t, h)
	target, _ := targetAccounts.Load(t)

	rec := do(t, h, bearer(request(http.MethodGet, "/v1/admin/deliverability"))(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var rep struct {
		Data []struct {
			AccountID      string          `json:"account_id"`
			AccountName    string          `json:"account_name"`
			OwnerEmail     *string         `json:"owner_email"`
			SendingMode    string          `json:"sending_mode"`
			ThrottledUntil *time.Time      `json:"throttled_until"`
			Day            json.RawMessage `json:"last_24_hours"`
			Week           struct {
				Sent              int      `json:"sent"`
				HardBounces       int      `json:"hard_bounces"`
				Complaints        int      `json:"complaints"`
				HardBouncePercent *float64 `json:"hard_bounce_percent"`
				ComplaintPercent  *float64 `json:"complaint_percent"`
			} `json:"last_7_days"`
			OverWarning bool `json:"over_warning"`
		} `json:"data"`
		NextCursor   *string `json:"next_cursor"`
		Total        int     `json:"total"`
		EarlyWarning struct {
			ComplaintPercent  float64 `json:"complaint_percent"`
			HardBouncePercent float64 `json:"hard_bounce_percent"`
			MinComplaints     int     `json:"min_complaints"`
			MinCopies         int     `json:"min_copies"`
		} `json:"early_warning"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Total != 1 || len(rep.Data) != 1 || rep.NextCursor != nil {
		t.Fatalf("report = %s", rec.Body)
	}
	if w := rep.EarlyWarning; w.ComplaintPercent != 0.05 || w.HardBouncePercent != 2 || w.MinComplaints != 2 || w.MinCopies != 500 {
		t.Errorf("early warning = %+v", w)
	}
	row := rep.Data[0]
	if row.AccountID != target || row.AccountName != "Target" || row.OwnerEmail == nil || *row.OwnerEmail != "target@example.com" ||
		row.SendingMode != "test" || row.ThrottledUntil == nil || !row.OverWarning {
		t.Errorf("row = %s", rec.Body)
	}
	if w := row.Week; w.Sent != 1000 || w.HardBounces != 30 || w.Complaints != 2 || w.HardBouncePercent == nil ||
		*w.HardBouncePercent != 3 || w.ComplaintPercent == nil || *w.ComplaintPercent != 0.2 {
		t.Errorf("7 days = %+v", w)
	}
}
