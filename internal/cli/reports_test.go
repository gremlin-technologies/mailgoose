package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestPrintReport(t *testing.T) {
	var rep campaignReport
	if err := json.Unmarshal([]byte(`{"campaign_id":"3f1c","status":"sent","launched_at":"2026-11-18T09:00:00Z","tracking":true,
		"recipients":200,"delivered":196,"bounced":3,"complaints":0,"unsubscribes":2,
		"opens":{"unique":80,"estimated":true},"clicks":{"unique":20,"total":31},"credits_used":200,
		"links":[{"position":1,"url":"https://patsbakery.com/order","varies":false,"unique_clicks":15,"total_clicks":22},
			{"position":3,"url":"https://patsbakery.com","varies":true,"unique_clicks":5,"total_clicks":9}],
		"archived":false,"as_of":"2026-11-18T12:00:00Z"}`), &rep); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	a := &App{Stdout: &out, zone: time.UTC}
	a.printReport(rep)
	for _, want := range []string{
		"Campaign 3f1c, sent, launched 2026-11-18 09:00 UTC.",
		"Delivered:     196 (98.0%)",
		"Bounced:       3 (1.5%)",
		"Opens:         80 (40.8%)", // MG-111: a plain number, a share of delivered
		"Clicks:        20 (10.2%) unique, 31 total",
		"Credits used:  200",
		"1  15      22     https://patsbakery.com/order",
		"3  5       9      https://patsbakery.com (differs per contact)",
		"As of 2026-11-18 12:00 UTC.",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}

	rep.Tracking, rep.Opens, rep.Clicks, rep.Links, rep.Archived = false, nil, nil, nil, true
	out.Reset()
	a.printReport(rep)
	if !strings.Contains(out.String(), "Opens and clicks:  not tracked") || !strings.Contains(out.String(), "Saved 2026-11-18 12:00 UTC") {
		t.Errorf("untracked:\n%s", out.String())
	}
}

// MG-111: `campaign list` shows each launched campaign's numbers, and
// --json passes its summary through, null for a draft.
func TestCampaignListShowsReportNumbers(t *testing.T) {
	base := `"email_id":null,"recipients":{"all":true,"tags":[]},"send_at":null,"version":1,"last_test":null,"launch":null,` +
		`"created_at":"2026-11-18T12:00:00Z","updated_at":"2026-11-18T12:00:00Z"`
	page := `{"data":[` +
		`{"id":"c1","name":"Bread","status":"sent","tracking":true,` + base + `,"summary":{"recipients":200,"delivered":196,"bounced":3,` +
		`"opens":80,"clicks":20,"delivered_rate":0.98,"bounce_rate":0.015,"open_rate":0.40816,"click_rate":0.10204,"archived":false}},` +
		`{"id":"c2","name":"Quiet","status":"sent","tracking":false,` + base + `,"summary":{"recipients":10,"delivered":10,"bounced":0,` +
		`"opens":null,"clicks":null,"delivered_rate":1,"bounce_rate":0,"open_rate":null,"click_rate":null,"archived":false}},` +
		`{"id":"c3","name":"Draft","status":"draft","tracking":true,` + base + `,"summary":null}` +
		`],"next_cursor":null}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/account" {
			_, _ = w.Write([]byte(`{"time_zone":"UTC"}`))
			return
		}
		_, _ = w.Write([]byte(page))
	}))
	t.Cleanup(srv.Close)
	h := newHarness(t, "")
	h.env[EnvAPIURL] = srv.URL
	h.env[EnvAPIKey] = "mg_test_key"

	if code := h.run("campaign", "list"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	lines := strings.Split(h.out.String(), "\n")
	if !strings.Contains(lines[0], "DELIVERED  OPEN RATE  CLICK RATE  BOUNCE RATE") {
		t.Errorf("header: %q", lines[0])
	}
	for i, want := range []string{"196        40.8%      10.2%       1.5%", "10         -          -           0.0%", "-          -          -           -"} {
		if !strings.HasSuffix(strings.TrimRight(lines[i+1], " "), want) {
			t.Errorf("row %d = %q, want it to end %q", i+1, lines[i+1], want)
		}
	}

	if code := h.run("campaign", "list", "--json"); code != ExitOK {
		t.Fatalf("json: exit %d: %s", code, h.errOut)
	}
	var out struct {
		Data []struct {
			ID      string          `json:"id"`
			Summary json.RawMessage `json:"summary"`
		} `json:"data"`
	}
	if err := json.Unmarshal(h.out.Bytes(), &out); err != nil || len(out.Data) != 3 {
		t.Fatalf("json = %s (%v)", h.out, err)
	}
	if !strings.Contains(string(out.Data[0].Summary), `"open_rate": 0.40816`) || string(out.Data[2].Summary) != "null" {
		t.Errorf("summaries = %s, %s", out.Data[0].Summary, out.Data[2].Summary)
	}
}

// MG-112: `campaign report --who` lists the people behind a number, over
// every page, and --csv downloads the list.
func TestReportWho(t *testing.T) {
	const id = "6f3c2b9e-1a4d-4c8e-9b7a-2d5e8f0a1b3c"
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path+"?"+r.URL.RawQuery)
		switch {
		case r.URL.Path == "/v1/account":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"time_zone":"UTC"}`))
		case strings.HasSuffix(r.URL.Path, "/report/recipients/export"):
			w.Header().Set("Content-Type", "text/csv")
			_, _ = w.Write([]byte("email,first_name,last_name,contact_id,first_clicked_at\npat@example.com,Pat,,k1,2026-11-19T10:04:00Z\n"))
		case r.URL.Query().Get("cursor") == "":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"contact_id":"k1","email":"pat@example.com","first_name":"Pat","last_name":"Baker","at":"2026-11-19T10:04:00Z"}],"next_cursor":"next"}`))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"contact_id":"k2","email":"sam@example.com","first_name":null,"last_name":null,"at":"2026-11-19T11:30:00Z"}],"next_cursor":null}`))
		}
	}))
	t.Cleanup(srv.Close)
	h := newHarness(t, "")
	h.env[EnvAPIURL] = srv.URL
	h.env[EnvAPIKey] = "mg_test_key"

	if code := h.run("campaign", "report", id, "--who", "clicked", "--link", "2"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	for _, want := range []string{"EMAIL", "WHEN", "pat@example.com  Pat Baker  2026-11-19 10:04 UTC", "sam@example.com", "2026-11-19 11:30 UTC", "2 people clicked link 2."} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, h.out)
		}
	}
	if !slices.Contains(seen, "/v1/campaigns/"+id+"/report/recipients?activity=clicked&limit=100&link=2") ||
		!slices.Contains(seen, "/v1/campaigns/"+id+"/report/recipients?activity=clicked&cursor=next&limit=100&link=2") {
		t.Errorf("requests = %v", seen)
	}

	if code := h.run("campaign", "report", id, "--who", "not_opened", "--json"); code != ExitOK {
		t.Fatalf("json: exit %d: %s", code, h.errOut)
	}
	var out struct {
		Data []struct {
			Email string `json:"email"`
		} `json:"data"`
	}
	if err := json.Unmarshal(h.out.Bytes(), &out); err != nil || len(out.Data) != 2 || out.Data[1].Email != "sam@example.com" {
		t.Errorf("json = %s (%v)", h.out, err)
	}

	if code := h.run("campaign", "report", id, "--who", "clicked", "--csv"); code != ExitOK {
		t.Fatalf("csv: exit %d: %s", code, h.errOut)
	}
	if !strings.HasPrefix(h.out.String(), "email,first_name,last_name,contact_id,first_clicked_at\npat@example.com") {
		t.Errorf("csv = %s", h.out)
	}
	file := filepath.Join(t.TempDir(), "clicked.csv")
	if code := h.run("campaign", "report", id, "--who", "clicked", "--csv", "--output", file); code != ExitOK || !strings.Contains(h.out.String(), "Wrote 1 recipient to") {
		t.Errorf("csv file: exit %d: %s %s", code, h.out, h.errOut)
	}

	for _, args := range [][]string{
		{"--who", "maybe"},
		{"--who", "opened", "--link", "1"},
		{"--link", "1"},
		{"--who", "clicked", "--output", file},
	} {
		if code := h.run(append([]string{"campaign", "report", id}, args...)...); code != ExitUsage {
			t.Errorf("%v: exit %d", args, code)
		}
	}
}
