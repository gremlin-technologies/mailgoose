package admin

import (
	"context"
	"testing"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/sending"
)

// TestDeliverability lists accounts' rates worst first, at most the limit,
// judged by the configured early warning (MG-82).
func TestDeliverability(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	send := func(name string, n, bounces int) string {
		t.Helper()
		id := h.account(t, name, name+"@example.com")
		var campaign string
		if err := h.db.QueryRow(ctx, `
			INSERT INTO campaigns (account_id, name, all_contacts, status, launched_at)
			VALUES ($1, 'Sent', true, 'sent', $2) RETURNING id`, id, h.now.Add(-time.Hour)).Scan(&campaign); err != nil {
			t.Fatal(err)
		}
		if _, err := h.db.Exec(ctx, `
			INSERT INTO campaign_recipients (account_id, campaign_id, status, sent_at, message_id)
			SELECT $1, $2, 'sent', $3, 'm-' || i FROM generate_series(1, $4::int) i`, id, campaign, h.now.Add(-time.Hour), n); err != nil {
			t.Fatal(err)
		}
		if _, err := h.db.Exec(ctx, `
			INSERT INTO send_events (event_key, account_id, campaign_id, recipient_id, message_id, kind, type, subtype, occurred_at)
			SELECT 'b' || r.id, r.account_id, r.campaign_id, r.id, r.message_id, 'campaign', 'bounce', 'permanent/general', $2
			FROM campaign_recipients r WHERE r.campaign_id = $1 ORDER BY r.id LIMIT $3`, campaign, h.now.Add(-time.Minute), bounces); err != nil {
			t.Fatal(err)
		}
		return id
	}
	bad := send("bad", 1000, 25)    // 2.5%
	_ = send("fine", 1000, 15)      // 1.5%
	worse := send("worse", 600, 30) // 5%

	rep, err := h.svc.Deliverability(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Total != 3 || len(rep.Accounts) != 2 || rep.Accounts[0].AccountID != worse || rep.Accounts[1].AccountID != bad ||
		!rep.Accounts[0].OverWarning || rep.Warning != sending.DefaultWarning {
		t.Fatalf("report = %+v", rep)
	}
	// A configured warning is used: at 3%, only the worst is over.
	h.svc.Warning = sending.Warning{ComplaintsPer10k: 5, HardBouncesPer10k: 300}
	rep, err = h.svc.Deliverability(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Accounts) != 1 || !rep.Accounts[0].OverWarning || rep.Warning.HardBouncesPer10k != 300 {
		t.Fatalf("with 3%%, limit 1 = %+v", rep)
	}
	if rep, err = h.svc.Deliverability(ctx, 500); err != nil || len(rep.Accounts) != 3 || rep.Accounts[1].OverWarning {
		t.Fatalf("all = %+v, %v", rep, err)
	}
}

// TestDeliverabilityThroughput gives the last 24 hours' send throughput
// with the report, against the configured rate (MG-91).
func TestDeliverabilityThroughput(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.svc.Limits = sending.Limits{PerSecond: 10}
	rep, err := h.svc.Deliverability(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if tp := rep.Throughput; tp.Batches != 0 || tp.CopiesPerSecond != nil || tp.SendRatePerSecond != 10 {
		t.Fatalf("nothing sent: %+v", tp)
	}
	var campaign string
	if err := h.db.QueryRow(ctx, `
		INSERT INTO campaigns (account_id, name, all_contacts, status, launched_at)
		VALUES ($1, 'Sent', true, 'sent', $2) RETURNING id`, h.acc, h.now.Add(-time.Hour)).Scan(&campaign); err != nil {
		t.Fatal(err)
	}
	// Two batches of 100 copies, 25 seconds each, one after the other.
	if _, err := h.db.Exec(ctx, `
		INSERT INTO send_batch_timings (account_id, campaign_id, started_at, finished_at, batch_size, sent, skipped, failed)
		VALUES ($1, $2, $3::timestamptz, $3::timestamptz + interval '25 seconds', 100, 100, 0, 0),
			($1, $2, $3::timestamptz + interval '30 seconds', $3::timestamptz + interval '55 seconds', 100, 100, 0, 0)`,
		h.acc, campaign, h.now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	rep, err = h.svc.Deliverability(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	tp := rep.Throughput
	if tp.Batches != 2 || tp.Copies != 200 || tp.BusySeconds != 50 || tp.CopiesPerSecond == nil || *tp.CopiesPerSecond != 4 ||
		tp.SendRatePerSecond != 10 || !tp.Since.Equal(h.now.Add(-24*time.Hour)) {
		t.Errorf("throughput %+v", tp)
	}
}
