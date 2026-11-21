package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// deliverabilityReport is a report as the API gives it: one account over
// the early warning (paused, throttled and in test mode), one clean tester
// account (MG-86), and one with complaints on copies sent before the 7 days.
const deliverabilityReport = `{"data":[
{"account_id":"` + testAccountID + `","account_name":"Loud Lists","owner_email":"loud@example.com","sending_mode":"test",
 "sending_paused_at":"2026-11-20T10:00:00Z","suspended_at":null,"throttled_until":"2026-11-25T10:00:00Z","tester_until":null,
 "last_24_hours":{"sent":1000,"hard_bounces":3,"complaints":2,"hard_bounce_percent":0.3,"complaint_percent":0.2},
 "last_7_days":{"sent":12500,"hard_bounces":30,"complaints":9,"hard_bounce_percent":0.24,"complaint_percent":0.072},
 "over_warning":true},
{"account_id":"acc-2","account_name":"Clean Co","owner_email":null,"sending_mode":"full",
 "sending_paused_at":null,"suspended_at":null,"throttled_until":null,"tester_until":"2026-12-20T23:59:59Z",
 "last_24_hours":{"sent":0,"hard_bounces":0,"complaints":0,"hard_bounce_percent":null,"complaint_percent":null},
 "last_7_days":{"sent":600,"hard_bounces":0,"complaints":0,"hard_bounce_percent":0,"complaint_percent":0},
 "over_warning":false},
{"account_id":"acc-3","account_name":"Old News","owner_email":null,"sending_mode":"full",
 "sending_paused_at":null,"suspended_at":"2026-11-19T08:00:00Z","throttled_until":null,"tester_until":null,
 "last_24_hours":{"sent":0,"hard_bounces":0,"complaints":0,"hard_bounce_percent":null,"complaint_percent":null},
 "last_7_days":{"sent":0,"hard_bounces":0,"complaints":2,"hard_bounce_percent":null,"complaint_percent":null},
 "over_warning":false}],
"next_cursor":null,"total":7,
"early_warning":{"complaint_percent":0.05,"hard_bounce_percent":2,"min_complaints":2,"min_copies":500},
"send_throughput":{"since":"2026-11-20T12:00:00Z","batches":42,"copies":4150,"busy_seconds":1037.5,"copies_per_second":4,
 "batch_seconds":1210.25,"per_batch_copies_per_second":3.43,"send_rate_per_second":10}}`

// TestAdminDeliverability prints the report as a table, worst first as the
// API sends it, with each account's state, and the last 24 hours' send
// throughput (MG-91), and as JSON (MG-82).
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
				`"early_warning":{"complaint_percent":0.05,"hard_bounce_percent":2,"min_complaints":2,"min_copies":500},` +
				`"send_throughput":{"since":"2026-11-20T12:00:00Z","batches":1,"copies":200,"busy_seconds":40.2,"copies_per_second":4.98,` +
				`"batch_seconds":40.2,"per_batch_copies_per_second":4.98,"send_rate_per_second":10}}`))
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
		{"acc-2", "Clean Co", "600", "0% (0)", "tester until 2026-12-20"},
		{"acc-3", "Old News", "- (2)", "suspended"},
	} {
		for _, w := range want {
			if !strings.Contains(lines[i+1], w) {
				t.Errorf("row %d lacks %q: %q", i+1, w, lines[i+1])
			}
		}
	}
	for _, w := range []string{
		"1 account shown is OVER WARNING: the last 7 days are over 0.05% complaints (and at least 2) or 2% hard bounces, on 500 or more copies.",
		"Showing the worst 3 of 7 accounts; --limit shows up to 100.",
		"Throughput, last 24 hours: 4,150 copies in 42 batches over 1037.5 seconds of sending: 4.00 a second (SEND_RATE_PER_SECOND is 10).",
		"Batches overlapped and shared the rate: one batch sent 3.43 a second (over 1210.2 batch-seconds).",
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
	// Simulator copies are left out of the accounts' rates but count for
	// throughput, so it shows with no accounts too; batches that didn't
	// overlap say so once.
	if code := h.run("admin", "deliverability"); code != ExitOK || !strings.Contains(h.out.String(), "No account sent campaign email in the last 7 days.") ||
		!strings.Contains(h.out.String(), "Throughput, last 24 hours: 200 copies in 1 batch over 40.2 seconds of sending: 4.98 a second (SEND_RATE_PER_SECOND is 10).") ||
		strings.Contains(h.out.String(), "overlapped") {
		t.Errorf("empty: exit %d: %s", code, h.out)
	}
	if code := h.run("admin", "deliverability", "extra"); code != ExitUsage {
		t.Errorf("extra argument: exit %d", code)
	}
}

// With no batch in the last 24 hours, the throughput line still names the
// configured rate (MG-91).
func TestSendThroughputNoBatches(t *testing.T) {
	got := sendThroughput{SendRatePerSecond: 10}.lines()
	if len(got) != 1 || got[0] != "Throughput, last 24 hours: no batch sent a copy (SEND_RATE_PER_SECOND is 10)." {
		t.Errorf("lines = %q", got)
	}
}

func TestEarlyWarningLineSaysHowManyAreOver(t *testing.T) {
	w := earlyWarning{ComplaintPercent: 0.05, HardBouncePercent: 2, MinComplaints: 2, MinCopies: 500}
	rule := "the last 7 days are over 0.05% complaints (and at least 2) or 2% hard bounces, on 500 or more copies."
	for over, want := range map[int]string{
		0: "No account shown is over the early warning. OVER WARNING in STATE means " + rule,
		1: "1 account shown is OVER WARNING: " + rule,
		3: "3 accounts shown are OVER WARNING: " + rule,
	} {
		if got := w.line(over); got != want {
			t.Errorf("line(%d) = %q, want %q", over, got, want)
		}
	}
}
