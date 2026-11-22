package reports

import (
	"context"
	"math"
	"testing"
	"time"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// MG-111: the campaign list's summaries are the reports' numbers, for a
// whole page at once.
func TestSummariesMatchTheReports(t *testing.T) {
	ctx := context.Background()
	// An archived campaign, launched 13 months ago and pruned.
	h := newHarness(t, time.Date(2025, 10, 1, 9, 0, 0, 0, time.UTC), true)
	h.events(t, time.Date(2025, 10, 2, 9, 0, 0, 0, time.UTC))
	if _, err := h.svc.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	// A live one with the same events, and an untracked one.
	live := newHarnessIn(t, h, time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC))
	live.events(t, h.now.Add(-time.Hour))
	untracked := newHarnessIn(t, h, time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC))
	h.exec(t, `UPDATE campaigns SET tracking = false WHERE id = $1`, untracked.campaign)
	untracked.sesEvent(t, 0, "delivery", "", h.now.Add(-time.Hour))
	untracked.sesEvent(t, 1, "delivery", "", h.now.Add(-time.Hour))
	// A draft, and another account's campaign.
	var draft, other, otherAcc string
	if err := h.db.QueryRow(ctx, `INSERT INTO campaigns (account_id, name, all_contacts) VALUES ($1, 'Draft', true) RETURNING id`, h.acc).Scan(&draft); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRow(ctx, `INSERT INTO accounts (name) VALUES ('Other') RETURNING id`).Scan(&otherAcc); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRow(ctx, `INSERT INTO campaigns (account_id, name, all_contacts, status, launched_at) VALUES ($1, 'Theirs', true, 'sent', $2) RETURNING id`,
		otherAcc, h.now).Scan(&other); err != nil {
		t.Fatal(err)
	}

	got, err := h.svc.Summaries(ctx, h.acc, []string{h.campaign, live.campaign, untracked.campaign, draft, other})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[draft] != nil || got[other] != nil {
		t.Fatalf("summaries = %+v", got)
	}
	for _, id := range []string{h.campaign, live.campaign, untracked.campaign} {
		rep, err := h.svc.Campaign(ctx, h.acc, id)
		if err != nil {
			t.Fatal(err)
		}
		want, sm := rep.Summary(), got[id]
		if sm == nil || sm.Recipients != want.Recipients || sm.Delivered != want.Delivered || sm.Bounced != want.Bounced ||
			sm.Archived != rep.Archived || !near(sm.DeliveredRate, want.DeliveredRate) || !near(sm.BounceRate, want.BounceRate) ||
			(sm.Opens == nil) != (want.Opens == nil) || (sm.Opens != nil && *sm.Opens != *want.Opens) ||
			(sm.Clicks == nil) != (want.Clicks == nil) || (sm.Clicks != nil && *sm.Clicks != *want.Clicks) {
			t.Errorf("summary of %s = %+v, report's = %+v", id, sm, want)
		}
	}

	// The tracked ones: 5 sent, 4 delivered, 1 bounced, 3 opened, 2 clicked.
	for _, id := range []string{h.campaign, live.campaign} {
		sm := got[id]
		if sm.Recipients != 5 || sm.Delivered != 4 || sm.Bounced != 1 || sm.Opens == nil || *sm.Opens != 3 || sm.Clicks == nil || *sm.Clicks != 2 ||
			!near(sm.DeliveredRate, 0.8) || !near(sm.BounceRate, 0.2) || !near(*sm.OpenRate, 0.75) || !near(*sm.ClickRate, 0.5) {
			t.Errorf("summary of %s = %+v", id, sm)
		}
	}
	if !got[h.campaign].Archived || got[live.campaign].Archived {
		t.Errorf("archived: %v, %v", got[h.campaign].Archived, got[live.campaign].Archived)
	}
	// Untracked: no opens or clicks.
	if sm := got[untracked.campaign]; sm.Delivered != 2 || sm.Opens != nil || sm.Clicks != nil || sm.OpenRate != nil || sm.ClickRate != nil {
		t.Errorf("untracked summary = %+v", sm)
	}

	// No ids, no query.
	if none, err := h.svc.Summaries(ctx, h.acc, nil); err != nil || len(none) != 0 {
		t.Errorf("no ids = %v, %v", none, err)
	}
}

func TestSummaryOfNothingSent(t *testing.T) {
	launched := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	sm := (&Report{LaunchedAt: &launched, Tracking: true, Opens: &Opens{}, Clicks: &Clicks{}}).Summary()
	if sm == nil || sm.DeliveredRate != 0 || sm.BounceRate != 0 || *sm.OpenRate != 0 || *sm.ClickRate != 0 {
		t.Errorf("summary = %+v", sm)
	}
	if (&Report{}).Summary() != nil {
		t.Error("a report with no launch has a summary")
	}
}
