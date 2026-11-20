package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// deliverabilityReport is a report as the API gives it: one account over
// the early warning (paused, throttled and in test mode), one clean, and
// one with complaints on copies sent before the 7 days.
const deliverabilityReport = `{"data":[
{"account_id":"` + testAccountID + `","account_name":"Loud Lists","owner_email":"loud@example.com","sending_mode":"test",
 "sending_paused_at":"2026-11-20T10:00:00Z","suspended_at":null,"throttled_until":"2026-11-25T10:00:00Z",
 "last_24_hours":{"sent":1000,"hard_bounces":3,"complaints":2,"hard_bounce_percent":0.3,"complaint_percent":0.2},
 "last_7_days":{"sent":12500,"hard_bounces":30,"complaints":9,"hard_bounce_percent":0.24,"complaint_percent":0.072},
 "over_warning":true},
{"account_id":"acc-2","account_name":"Clean Co","owner_email":null,"sending_mode":"full",
 "sending_paused_at":null,"suspended_at":null,"throttled_until":null,
 "last_24_hours":{"sent":0,"hard_bounces":0,"complaints":0,"hard_bounce_percent":null,"complaint_percent":null},
 "last_7_days":{"sent":600,"hard_bounces":0,"complaints":0,"hard_bounce_percent":0,"complaint_percent":0},
 "over_warning":false},
{"account_id":"acc-3","account_name":"Old News","owner_email":null,"sending_mode":"full",
 "sending_paused_at":null,"suspended_at":"2026-11-19T08:00:00Z","throttled_until":null,
 "last_24_hours":{"sent":0,"hard_bounces":0,"complaints":0,"hard_bounce_percent":null,"complaint_percent":null},
 "last_7_days":{"sent":0,"hard_bounces":0,"complaints":2,"hard_bounce_percent":null,"complaint_percent":null},
 "over_warning":false}],
"next_cursor":null,"total":7,
"early_warning":{"complaint_percent":0.05,"hard_bounce_percent":2,"min_complaints":2,"min_copies":500}}`

// TestAdminDeliverability prints the report as a table, worst first as the
// API sends it, with each account's state, and as JSON (MG-82).
func TestAdminDeliverability(t *testing.T) {
	var queries []string
	empty := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/admin/deliverability" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		queries = append(queries, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		if empty {
			_, _ = w.Write([]byte(`{"data":[],"next_cursor":null,"total":0,` +
				`"early_warning":{"complaint_percent":0.05,"hard_bounce_percent":2,"min_complaints":2,"min_copies":500}}`))
			return
		}
		_, _ = w.Write([]byte(deliverabilityReport))
	}))
	t.Cleanup(srv.Close)
	h := newHarness(t, "")
	h.env[EnvAPIURL] = srv.URL
	h.env[EnvAPIKey] = "mg_test_abc"

	if code := h.run("admin", "deliverability", "--limit", "3"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	out := h.out.String()
	lines := strings.Split(out, "\n")
	if !strings.HasPrefix(lines[0], "ID") || !strings.Contains(lines[0], "24H SENT") || !strings.Contains(lines[0], "7D HARD BOUNCES") ||
		!strings.Contains(lines[0], "7D COMPLAINTS") || !strings.Contains(lines[0], "STATE") {
		t.Errorf("header = %q", lines[0])
	}
	for i, want := range [][]string{
		{testAccountID, "Loud Lists", "1,000", "0.3% (3)", "0.2% (2)", "12,500", "0.24% (30)", "0.072% (9)",
			"OVER WARNING, paused, throttled until 2026-11-25", "test mode"},
		{"acc-2", "Clean Co", "600", "0% (0)", "  ok"},
		{"acc-3", "Old News", "- (2)", "suspended"},
	} {
		for _, w := range want {
			if !strings.Contains(lines[i+1], w) {
				t.Errorf("row %d lacks %q: %q", i+1, w, lines[i+1])
			}
		}
	}
	for _, w := range []string{
		"OVER WARNING: the last 7 days are over 0.05% complaints (and at least 2) or 2% hard bounces, on 500 or more copies.",
		"Showing the worst 3 of 7 accounts; --limit shows up to 100.",
	} {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q:\n%s", w, out)
		}
	}
	if len(queries) != 1 || queries[0] != "limit=3" {
		t.Errorf("queries = %v", queries)
	}

	// --json gives the API's data as it came.
	if code := h.run("admin", "deliverability", "--json"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	var got, want map[string]any
	if err := json.Unmarshal(h.out.Bytes(), &got); err != nil {
		t.Fatalf("%v: %s", err, h.out)
	}
	_ = json.Unmarshal([]byte(deliverabilityReport), &want)
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("--json =\n%s\nwant\n%s", gotJSON, wantJSON)
	}
	if queries[1] != "" {
		t.Errorf("no --limit sent %q", queries[1])
	}

	empty = true
	if code := h.run("admin", "deliverability"); code != ExitOK || !strings.Contains(h.out.String(), "No account sent campaign email in the last 7 days.") {
		t.Errorf("empty: exit %d: %s", code, h.out)
	}
	if code := h.run("admin", "deliverability", "extra"); code != ExitUsage {
		t.Errorf("extra argument: exit %d", code)
	}
}
