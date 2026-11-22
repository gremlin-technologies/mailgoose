package cli

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const testCampaign = "c1a2b3c4-0000-4000-8000-000000000001"

// MG-113: the engagement filters go to the API as its query parameters.
func TestContactEngagementFilters(t *testing.T) {
	var queries []url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.Query())
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/contacts/count":
			if r.URL.Query().Get("clicked") == "c1a2b3c4-0000-4000-8000-000000000009" {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(`{"error":{"code":"tracking_off","message":"Campaign was sent with tracking off.","hint":""}}`))
				return
			}
			_, _ = w.Write([]byte(`{"count":1}`))
		case "/v1/contacts":
			_, _ = w.Write([]byte(`{"data":[` + testContact("pat@example.com", "subscribed") + `],"next_cursor":null}`))
		case "/v1/contacts/export":
			w.Header().Set("Content-Type", "text/csv")
			_, _ = w.Write([]byte(testExportCSV))
		}
	}))
	t.Cleanup(srv.Close)
	h := newHarness(t, "")
	h.env[EnvAPIURL] = srv.URL
	h.env[EnvAPIKey] = "mg_test_fullkey"

	filters := []string{"--opened", testCampaign, "--opened", "c1a2b3c4-0000-4000-8000-000000000002", "--not-clicked", testCampaign,
		"--never-opened", "--never-clicked", "--opened-within-days", "30", "--clicked-within-days", "7", "--status", "subscribed"}
	want := url.Values{
		"opened":              {testCampaign, "c1a2b3c4-0000-4000-8000-000000000002"},
		"not_clicked":         {testCampaign},
		"never_opened":        {"true"},
		"never_clicked":       {"true"},
		"opened_within_days":  {"30"},
		"clicked_within_days": {"7"},
		"status":              {"subscribed"},
	}
	for _, args := range [][]string{
		append([]string{"contacts", "list"}, filters...),
		append([]string{"contacts", "count"}, filters...),
		append([]string{"export"}, filters...),
	} {
		h.out.Reset()
		if code := h.run(args...); code != ExitOK {
			t.Fatalf("%v: exit %d %s", args[:2], code, h.errOut)
		}
		got := queries[len(queries)-1]
		if got.Encode() != want.Encode() {
			t.Errorf("%v sent %v, want %v", args[:2], got, want)
		}
	}

	h.out.Reset()
	if code := h.run("contacts", "count", "--never-clicked"); code != ExitOK || h.out.String() != "1 contact matches.\n" {
		t.Errorf("count: exit %d, %q", code, h.out)
	}
	h.out.Reset()
	if code := h.run("contacts", "count", "--json"); code != ExitOK || strings.TrimSpace(h.out.String()) != "{\n  \"count\": 1\n}" {
		t.Errorf("count --json: exit %d, %q", code, h.out)
	}
	if code := h.run("contacts", "count", "--clicked", "c1a2b3c4-0000-4000-8000-000000000009"); code == ExitOK || !strings.Contains(h.errOut.String(), "tracking off") {
		t.Errorf("untracked: exit %d, %q", code, h.errOut)
	}
}
