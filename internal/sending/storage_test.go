package sending

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestSizeText(t *testing.T) {
	for n, want := range map[int64]string{
		0:                             "0 bytes",
		1:                             "1 byte",
		512:                           "512 bytes",
		1024:                          "1 KB",
		1536:                          "1.5 KB",
		16 * 1024:                     "16 KB",
		10 * 1024 * 1024:              "10 MB",
		1288490189:                    "1.2 GB",
		-3 * 1024 * 1024:              "-3 MB",
		5 * 1024 * 1024 * 1024 * 1024: "5 TB",
	} {
		if got := SizeText(n); got != want {
			t.Errorf("SizeText(%d) = %q, want %q", n, got, want)
		}
	}
	for n, want := range map[int64]string{0: "no change", 80 * 1024 * 1024: "+80 MB", -2048: "-2 KB"} {
		if got := changeText(n); got != want {
			t.Errorf("changeText(%d) = %q, want %q", n, got, want)
		}
	}
}

// sentCopies adds n sent copies of a new campaign of h's account, each
// with a delivery event.
func (h *harness) sentCopies(t *testing.T, n int, at time.Time) {
	t.Helper()
	ctx := context.Background()
	var campaign string
	if err := h.db.QueryRow(ctx, `
		INSERT INTO campaigns (account_id, name, all_contacts, status, launched_at)
		VALUES ($1, 'Sent', true, 'sent', $2) RETURNING id`, h.acc, at).Scan(&campaign); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(ctx, `
		INSERT INTO campaign_recipients (account_id, campaign_id, status, sent_at, message_id)
		SELECT $1, $2::uuid, 'sent', $3, 'm-' || $2::text || '-' || i FROM generate_series(1, $4::int) i`, h.acc, campaign, at, n); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(ctx, `
		INSERT INTO send_events (event_key, account_id, campaign_id, recipient_id, message_id, kind, type, occurred_at)
		SELECT 'd' || r.id, r.account_id, r.campaign_id, r.id, r.message_id, 'campaign', 'delivery', $2
		FROM campaign_recipients r WHERE r.campaign_id = $1`, campaign, at); err != nil {
		t.Fatal(err)
	}
}

// TestStorageSizes lists campaign_recipients and send_events first, then
// the largest other tables, with the planner's row estimates and the size
// recorded 7 days before (MG-95).
func TestStorageSizes(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	h.sentCopies(t, 200, now.Add(-time.Hour))
	for _, q := range []string{
		// The largest table after the tracked two, analysed: an exact
		// estimate for a table this small.
		`CREATE TABLE big_one (x text)`,
		`INSERT INTO big_one SELECT repeat('x', 1000) FROM generate_series(1, 2000)`,
		`ANALYZE big_one`,
		// Never analysed: no estimate.
		`CREATE TABLE never_analysed (x text)`,
		`INSERT INTO table_size_snapshots (day, table_name, total_bytes, row_estimate, taken_at)
			VALUES ('2026-11-14', 'big_one', 1024, 0, '2026-11-14 13:00Z'),
				('2026-11-13', 'send_events', 1024, 0, '2026-11-13 13:00Z')`,
	} {
		if _, err := h.db.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	rep, err := StorageSizes(ctx, h.db, now, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Tables) != 3 || rep.Tables[0].Name != "campaign_recipients" || rep.Tables[1].Name != "send_events" ||
		rep.Tables[2].Name != "big_one" {
		t.Fatalf("tables = %+v", rep.Tables)
	}
	if !rep.CompareDay.Equal(time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("compare day = %v", rep.CompareDay)
	}
	big := rep.Tables[2]
	if big.RowEstimate == nil || *big.RowEstimate != 2000 || big.TotalBytes < 2_000_000 {
		t.Errorf("big_one = %+v", big)
	}
	if big.BytesThen == nil || *big.BytesThen != 1024 || big.Change() == nil || *big.Change() != big.TotalBytes-1024 {
		t.Errorf("big_one's change = %v, %v", big.BytesThen, big.Change())
	}
	// send_events' snapshot is from another day: nothing to compare.
	if se := rep.Tables[1]; se.BytesThen != nil || se.Change() != nil || se.TotalBytes <= 0 {
		t.Errorf("send_events = %+v", se)
	}

	all, err := StorageSizes(ctx, h.db, now, MaxStorageTables)
	if err != nil {
		t.Fatal(err)
	}
	var sum int64
	var sawNever bool
	for i, tb := range all.Tables {
		sum += tb.TotalBytes
		if i > 2 && tb.TotalBytes > all.Tables[i-1].TotalBytes {
			t.Errorf("%s (%d) after %s (%d): not largest first", tb.Name, tb.TotalBytes, all.Tables[i-1].Name, all.Tables[i-1].TotalBytes)
		}
		if tb.Name == "never_analysed" {
			sawNever = true
			if tb.RowEstimate != nil {
				t.Errorf("never_analysed estimate = %d", *tb.RowEstimate)
			}
		}
	}
	if all.TotalTables < 40 || all.TotalTables != rep.TotalTables || all.TotalBytes != rep.TotalBytes {
		t.Errorf("totals = %d tables, %d bytes; limited %d, %d", all.TotalTables, all.TotalBytes, rep.TotalTables, rep.TotalBytes)
	}
	if len(all.Tables) == all.TotalTables && (sum != all.TotalBytes || !sawNever) {
		t.Errorf("sum %d of %d tables, total %d, never_analysed listed %v", sum, len(all.Tables), all.TotalBytes, sawNever)
	}
	// The limit is kept to 1 to MaxStorageTables.
	if one, err := StorageSizes(ctx, h.db, now, 0); err != nil || len(one.Tables) != 1 || one.Tables[0].Name != "campaign_recipients" {
		t.Errorf("limit 0 = %+v, %v", one.Tables, err)
	}
}

// TestRecordTableSizes writes every table once a UTC day and keeps 8
// weeks (MG-95).
func TestRecordTableSizes(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 21, 13, 0, 0, 0, time.UTC)
	if _, err := h.db.Exec(ctx, `
		INSERT INTO table_size_snapshots (day, table_name, total_bytes, row_estimate, taken_at) VALUES
			('2026-09-25', 'send_events', 1, NULL, '2026-09-25 13:00Z'),
			('2026-09-26', 'send_events', 1, NULL, '2026-09-26 13:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := RecordTableSizes(ctx, h.db, now); err != nil {
		t.Fatal(err)
	}
	rep, err := StorageSizes(ctx, h.db, now, MaxStorageTables)
	if err != nil {
		t.Fatal(err)
	}
	count := func(day string) int {
		t.Helper()
		var n int
		if err := h.db.QueryRow(ctx, `SELECT count(*) FROM table_size_snapshots WHERE day = $1`, day).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count("2026-11-21"); n != rep.TotalTables {
		t.Errorf("%d rows for %d tables", n, rep.TotalTables)
	}
	var first int64
	if err := h.db.QueryRow(ctx, `
		SELECT total_bytes FROM table_size_snapshots WHERE day = '2026-11-21' AND table_name = 'campaign_recipients'`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	// 56 days back is kept, 57 isn't.
	if count("2026-09-25") != 0 || count("2026-09-26") != 1 {
		t.Errorf("pruned: 57 days %d, 56 days %d", count("2026-09-25"), count("2026-09-26"))
	}

	// Later the same day (in another zone): the day's row stays as it was.
	h.sentCopies(t, 500, now)
	if err := RecordTableSizes(ctx, h.db, time.Date(2026, 11, 21, 15, 0, 0, 0, time.FixedZone("EST", -5*60*60))); err != nil {
		t.Fatal(err)
	}
	var again int64
	if err := h.db.QueryRow(ctx, `
		SELECT total_bytes FROM table_size_snapshots WHERE day = '2026-11-21' AND table_name = 'campaign_recipients'`).Scan(&again); err != nil {
		t.Fatal(err)
	}
	if again != first || count("2026-11-21") != rep.TotalTables || count("2026-11-22") != 0 {
		t.Errorf("rewrote the day: %d then %d", first, again)
	}
}

// TestDigestStorageLine gives the digest one line with the two tracked
// tables' sizes and their change over 7 days, or says there's no size
// from 7 days ago (MG-95). The job records the sizes on quiet days too.
func TestDigestStorageLine(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 12, 13, 0, 0, 0, time.UTC)
	j := h.digestJob(&now)
	h.sentCopies(t, 100, now.Add(-time.Hour))
	if sent, err := j.RunOnce(ctx); err != nil || sent {
		t.Fatalf("quiet day: sent %v, %v", sent, err)
	}
	size := func(day, table string) int64 {
		t.Helper()
		var n int64
		if err := h.db.QueryRow(ctx, `SELECT total_bytes FROM table_size_snapshots WHERE day = $1 AND table_name = $2`,
			day, table).Scan(&n); err != nil {
			t.Fatalf("%s on %s: %v", table, day, err)
		}
		return n
	}
	_ = size("2026-11-12", "campaign_recipients")

	// Six days later: no size from 7 days before.
	now = time.Date(2026, 11, 18, 13, 0, 0, 0, time.UTC)
	h.logAction(t, "automatic", "auto_pause", h.acc, "Paused", now.Add(-time.Hour))
	if sent, err := j.RunOnce(ctx); err != nil || !sent {
		t.Fatalf("day 6: sent %v, %v", sent, err)
	}
	last, _ := h.fake.Last()
	want := "Storage: campaign_recipients " + SizeText(size("2026-11-18", "campaign_recipients")) +
		" (no size from 7 days ago to compare), send_events " + SizeText(size("2026-11-18", "send_events")) +
		" (no size from 7 days ago to compare). `mailgoose admin storage` lists the largest tables.\n"
	if !strings.Contains(last.Message.Text, "\n"+want) {
		t.Errorf("missing %q in:\n%s", want, last.Message.Text)
	}

	// Seven days on, after more sending: the change since the 12th.
	h.sentCopies(t, 3000, now)
	now = time.Date(2026, 11, 19, 14, 0, 0, 0, time.UTC)
	h.logAction(t, "automatic", "auto_pause", h.acc, "Paused again", now.Add(-time.Hour))
	if sent, err := j.RunOnce(ctx); err != nil || !sent {
		t.Fatalf("day 7: sent %v, %v", sent, err)
	}
	last, _ = h.fake.Last()
	cr, cr0 := size("2026-11-19", "campaign_recipients"), size("2026-11-12", "campaign_recipients")
	se, se0 := size("2026-11-19", "send_events"), size("2026-11-12", "send_events")
	if cr <= cr0 || se <= se0 {
		t.Fatalf("didn't grow: %d to %d, %d to %d", cr0, cr, se0, se)
	}
	want = "Storage: campaign_recipients " + SizeText(cr) + " (+" + SizeText(cr-cr0) + " in 7 days), send_events " +
		SizeText(se) + " (+" + SizeText(se-se0) + " in 7 days). `mailgoose admin storage` lists the largest tables.\n"
	if !strings.Contains(last.Message.Text, "\n"+want) {
		t.Errorf("missing %q in:\n%s", want, last.Message.Text)
	}
	// It's the digest's last line.
	if !strings.HasSuffix(last.Message.Text, want) {
		t.Errorf("not last:\n%s", last.Message.Text)
	}
}
