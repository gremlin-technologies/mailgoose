package cli

import (
	"bytes"
	"encoding/json"
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
		"Delivered:          196 (98.0%)",
		"Bounced:            3 (1.5%)",
		"Opens (estimated):  80 (40.0%)",
		"Clicks:             20 (10.0%) unique, 31 total",
		"Credits used:       200",
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
