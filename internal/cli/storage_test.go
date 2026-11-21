package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// storageReport is a report as the API gives it: the two tracked tables,
// one with a size from 7 days ago and one without, an analysed table that
// shrank and one never analysed.
const storageReport = `{"data":[
{"table":"campaign_recipients","row_estimate":1234567,"total_bytes":1288490189,"total_bytes_7_days_ago":1204604109,"change_7_days_bytes":83886080},
{"table":"send_events","row_estimate":2400000,"total_bytes":671088640,"total_bytes_7_days_ago":null,"change_7_days_bytes":null},
{"table":"contacts","row_estimate":5000,"total_bytes":2097152,"total_bytes_7_days_ago":2101248,"change_7_days_bytes":-4096},
{"table":"images","row_estimate":null,"total_bytes":16384,"total_bytes_7_days_ago":16384,"change_7_days_bytes":0}],
"next_cursor":null,"total":42,"total_bytes":1999999999,"compared_with_day":"2026-11-14"}`

// TestAdminStorage prints the table sizes, the tracked tables first as the
// API sends them, and as JSON (MG-95).
func TestAdminStorage(t *testing.T) {
	var queries []string
	empty := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/admin/storage" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		queries = append(queries, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		if empty {
			_, _ = w.Write([]byte(`{"data":[],"next_cursor":null,"total":0,"total_bytes":0,"compared_with_day":"2026-11-14"}`))
			return
		}
		_, _ = w.Write([]byte(storageReport))
	}))
	t.Cleanup(srv.Close)
	h := newHarness(t, "")
	h.env[EnvAPIURL] = srv.URL
	h.env[EnvAPIKey] = "mg_test_abc"

	if code := h.run("admin", "storage", "--limit", "4"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	out := h.out.String()
	lines := strings.Split(out, "\n")
	for _, w := range []string{"TABLE", "ROWS (EST.)", "SIZE", "7-DAY CHANGE"} {
		if !strings.Contains(lines[0], w) {
			t.Errorf("header lacks %q: %q", w, lines[0])
		}
	}
	for i, want := range [][]string{
		{"campaign_recipients", "1,234,567", "1.2 GB", "+80 MB"},
		{"send_events", "2,400,000", "640 MB", "-"},
		{"contacts", "5,000", "2 MB", "-4 KB"},
		{"images", "not analysed", "16 KB", "no change"},
	} {
		f := strings.Fields(lines[i+1])
		if !strings.HasPrefix(lines[i+1], want[0]+" ") || !strings.Contains(lines[i+1], want[1]) ||
			!strings.Contains(lines[i+1], want[2]) || strings.Join(f[len(f)-len(strings.Fields(want[3])):], " ") != want[3] {
			t.Errorf("row %d = %q, want %q", i+1, lines[i+1], want)
		}
	}
	for _, w := range []string{
		"Row counts are estimates. 7-day change is against 2026-11-14 (UTC).",
		"Showing 4 of 42 tables, 1.9 GB in all; --limit shows up to 50.",
	} {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q:\n%s", w, out)
		}
	}
	if len(queries) != 1 || queries[0] != "limit=4" {
		t.Errorf("queries = %v", queries)
	}

	// --json gives the API's data as it came.
	if code := h.run("admin", "storage", "--json"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	var got, want map[string]any
	if err := json.Unmarshal(h.out.Bytes(), &got); err != nil {
		t.Fatalf("%v: %s", err, h.out)
	}
	_ = json.Unmarshal([]byte(storageReport), &want)
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("--json =\n%s\nwant\n%s", gotJSON, wantJSON)
	}
	if queries[1] != "" {
		t.Errorf("no --limit sent %q", queries[1])
	}

	empty = true
	if code := h.run("admin", "storage"); code != ExitOK || !strings.Contains(h.out.String(), "No tables found.") {
		t.Errorf("empty: exit %d: %s", code, h.out)
	}
	if code := h.run("admin", "storage", "extra"); code != ExitUsage {
		t.Errorf("extra argument: exit %d", code)
	}
}

func TestBytesText(t *testing.T) {
	for n, want := range map[int64]string{0: "0 bytes", 1: "1 byte", 1023: "1023 bytes", 1024: "1 KB", 1536: "1.5 KB",
		83886080: "80 MB", -4096: "-4 KB", 1288490189: "1.2 GB"} {
		if got := bytesText(n); got != want {
			t.Errorf("bytesText(%d) = %q, want %q", n, got, want)
		}
	}
}
