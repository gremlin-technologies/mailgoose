package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func init() {
	const path = "/v1/admin/storage"
	contractCases["getStorageReport"] = []contractCase{
		{name: "found", server: asAdmin(authServer), before: sendingTarget, req: bearer(request(http.MethodGet, path)), status: http.StatusOK},
		{name: "limit", server: asAdmin(authServer), req: bearer(request(http.MethodGet, path+"?limit=1")), status: http.StatusOK},
		{name: "bad limit", server: asAdmin(authServer), req: bearer(request(http.MethodGet, path+"?limit=51")), status: http.StatusBadRequest},
		{name: "no credentials", server: authServer, req: request(http.MethodGet, path), status: http.StatusUnauthorized},
		{name: "not an admin", server: fullKey(authServer), req: bearer(request(http.MethodGet, path)), status: http.StatusForbidden},
		{name: "rate limited", server: asAdmin(rateLimitedAuthServer), before: send(bearer(request(http.MethodGet, "/v1/campaigns"))),
			req: bearer(request(http.MethodGet, path)), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: bearer(request(http.MethodGet, path)), status: http.StatusServiceUnavailable},
	}
}

// TestStorageThroughTheAPI reads the table sizes as an admin (MG-95).
func TestStorageThroughTheAPI(t *testing.T) {
	h := asAdmin(authServer)(t).Handler()
	sendingTarget(t, h)

	for _, bad := range []string{"0", "51", "ten"} {
		if rec := do(t, h, bearer(request(http.MethodGet, "/v1/admin/storage?limit="+bad))(t)); rec.Code != http.StatusBadRequest {
			t.Errorf("limit %s: %d %s", bad, rec.Code, rec.Body)
		}
	}

	rec := do(t, h, bearer(request(http.MethodGet, "/v1/admin/storage"))(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var rep struct {
		Data []struct {
			Table              string `json:"table"`
			RowEstimate        *int64 `json:"row_estimate"`
			TotalBytes         int64  `json:"total_bytes"`
			TotalBytes7DaysAgo *int64 `json:"total_bytes_7_days_ago"`
			Change7DaysBytes   *int64 `json:"change_7_days_bytes"`
		} `json:"data"`
		NextCursor      *string `json:"next_cursor"`
		Total           int     `json:"total"`
		TotalBytes      int64   `json:"total_bytes"`
		ComparedWithDay string  `json:"compared_with_day"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Data) != 10 || rep.NextCursor != nil || rep.Total <= 10 || rep.TotalBytes <= 0 {
		t.Fatalf("report = %s", rec.Body)
	}
	if want := time.Now().UTC().AddDate(0, 0, -7).Format(time.DateOnly); rep.ComparedWithDay != want {
		t.Errorf("compared_with_day = %q, want %q", rep.ComparedWithDay, want)
	}
	var sum int64
	for i, d := range rep.Data {
		sum += d.TotalBytes
		if d.TotalBytes <= 0 || d.TotalBytes7DaysAgo != nil || d.Change7DaysBytes != nil {
			t.Errorf("row %d = %+v", i, d)
		}
	}
	if rep.Data[0].Table != "campaign_recipients" || rep.Data[1].Table != "send_events" || sum > rep.TotalBytes {
		t.Errorf("tables = %s", rec.Body)
	}

	rec = do(t, h, bearer(request(http.MethodGet, "/v1/admin/storage?limit=1"))(t))
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil || rec.Code != http.StatusOK || len(rep.Data) != 1 ||
		rep.Data[0].Table != "campaign_recipients" {
		t.Errorf("limit 1: %d %s", rec.Code, rec.Body)
	}
}
