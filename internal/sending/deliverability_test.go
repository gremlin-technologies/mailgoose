package sending

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// sentCampaign adds a sent campaign of n copies for the account, sent at at.
func (h *harness) sentCampaign(t *testing.T, accountID string, n int, at time.Time) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := h.db.QueryRow(ctx, `
		INSERT INTO campaigns (account_id, name, all_contacts, status, launched_at)
		VALUES ($1, 'Sent', true, 'sent', $2) RETURNING id`, accountID, at).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(ctx, `
		INSERT INTO campaign_recipients (account_id, campaign_id, status, sent_at, message_id)
		SELECT $1, $2::uuid, 'sent', $3, 'm-' || $2::text || '-' || i FROM generate_series(1, $4) i`,
		accountID, id, at, n); err != nil {
		t.Fatal(err)
	}
	return id
}

// reported adds an SES event of the kind for the campaign's first n
// recipients, after skipping skip of them.
func (h *harness) reported(t *testing.T, campaignID, kind, typ, subtype string, skip, n int, at time.Time) {
	t.Helper()
	if _, err := h.db.Exec(context.Background(), `
		INSERT INTO send_events (event_key, account_id, campaign_id, recipient_id, message_id, kind, type, subtype, occurred_at)
		SELECT gen_random_uuid()::text, r.account_id, r.campaign_id, r.id, r.message_id, $2, $3, nullif($4, ''), $5
		FROM campaign_recipients r WHERE r.campaign_id = $1 ORDER BY r.id OFFSET $6 LIMIT $7`,
		campaignID, kind, typ, subtype, at, skip, n); err != nil {
		t.Fatal(err)
	}
}

// deliverabilityData sets up five accounts that sent in the last 7 days to
// now, one that sent before them, and returns their ids by name.
func deliverabilityData(t *testing.T, h *harness, now time.Time) map[string]string {
	t.Helper()
	ctx := context.Background()
	ids := map[string]string{}
	add := func(name string) string {
		ids[name] = h.digestAccount(t, name, strings.ToLower(strings.ReplaceAll(name, " ", ""))+"@example.com")
		return ids[name]
	}

	// Over the early warning on complaints, in both windows: paused and in
	// test mode. A soft bounce, a test send's complaint and a second
	// complaint from one recipient don't count.
	loud := add("Loud Lists")
	c := h.sentCampaign(t, loud, 1000, now.Add(-2*time.Hour))
	h.reported(t, c, "campaign", "bounce", "permanent/general", 0, 3, now.Add(-time.Hour))
	h.reported(t, c, "campaign", "complaint", "abuse", 10, 2, now.Add(-time.Hour))
	h.reported(t, c, "campaign", "complaint", "abuse", 10, 1, now.Add(-30*time.Minute))
	h.reported(t, c, "campaign", "bounce", "transient/general", 20, 5, now.Add(-time.Hour))
	h.reported(t, c, "test", "complaint", "abuse", 30, 1, now.Add(-time.Hour))
	if _, err := h.db.Exec(ctx, `
		UPDATE accounts SET sending_paused_at = $2, sending_paused_reason = 'x', sending_mode = 'test' WHERE id = $1`,
		loud, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}

	// Over on hard bounces, 3 days ago: nothing in the last 24 hours.
	// Throttled.
	bouncy := add("Bouncy Co")
	c = h.sentCampaign(t, bouncy, 2000, now.Add(-72*time.Hour))
	h.reported(t, c, "campaign", "bounce", "permanent/general", 0, 50, now.Add(-48*time.Hour))
	if _, err := h.db.Exec(ctx, `
		INSERT INTO account_throttles (account_id, reason, created_at, until) VALUES ($1, 'x', $2, $3)`,
		bouncy, now.Add(-48*time.Hour), now.Add(5*24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	// A high rate on too few copies and complaints to judge.
	tiny := add("Tiny Shop")
	c = h.sentCampaign(t, tiny, 100, now.Add(-time.Hour))
	h.reported(t, c, "campaign", "complaint", "abuse", 0, 1, now.Add(-30*time.Minute))

	// Clean, with a throttle that has lifted.
	clean := add("Clean Co")
	h.sentCampaign(t, clean, 600, now.Add(-time.Hour))
	if _, err := h.db.Exec(ctx, `
		INSERT INTO account_throttles (account_id, reason, created_at, until) VALUES ($1, 'x', $2, $3)`,
		clean, now.Add(-20*24*time.Hour), now.Add(-13*24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	// Sent 10 days ago, complaints reported in the last 7 days.
	old := add("Old News")
	c = h.sentCampaign(t, old, 800, now.Add(-10*24*time.Hour))
	h.reported(t, c, "campaign", "complaint", "abuse", 0, 2, now.Add(-24*time.Hour-time.Minute))

	// Sent 8 days ago and nothing since: not in the report.
	quiet := add("Quiet Ltd")
	c = h.sentCampaign(t, quiet, 900, now.Add(-8*24*time.Hour))
	h.reported(t, c, "campaign", "complaint", "abuse", 0, 9, now.Add(-8*24*time.Hour+time.Hour))
	return ids
}

func pctIs(p *float64, want float64) bool { return p != nil && *p == want }

// TestDeliverabilityReport covers the rates, what counts, the state on each
// row and the order, worst first (MG-82).
func TestDeliverabilityReport(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 20, 13, 0, 0, 0, time.UTC)
	ids := deliverabilityData(t, h, now)

	rep, err := DeliverabilityReport(ctx, h.db, now, Warning{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	byName := map[string]AccountDeliverability{}
	for _, a := range rep {
		names = append(names, a.AccountName)
		byName[a.AccountName] = a
	}
	// Over the warning first (complaints 4x over before bounces 1.25x),
	// then the worst rates (Tiny Shop's 1% complaints), then the most
	// copies.
	if got, want := strings.Join(names, ", "), "Loud Lists, Bouncy Co, Tiny Shop, Clean Co, Old News"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}

	loud := byName["Loud Lists"]
	if loud.AccountID != ids["Loud Lists"] || loud.OwnerEmail == nil || *loud.OwnerEmail != "loudlists@example.com" {
		t.Errorf("loud = %+v", loud)
	}
	if loud.Day.Rates != (Rates{Sent: 1000, HardBounces: 3, Complaints: 2}) || loud.Week.Rates != loud.Day.Rates ||
		!pctIs(loud.Week.HardBouncePercent, 0.3) || !pctIs(loud.Week.ComplaintPercent, 0.2) || !loud.OverWarning {
		t.Errorf("loud rates = %+v / %+v, over %v", loud.Day, loud.Week, loud.OverWarning)
	}
	if loud.SendingPausedAt == nil || loud.SendingMode != "test" || loud.ThrottledUntil != nil || loud.SuspendedAt != nil ||
		strings.Join(loud.State(), ", ") != "paused, test mode" {
		t.Errorf("loud state = %v", loud.State())
	}

	bouncy := byName["Bouncy Co"]
	if bouncy.Day.Rates != (Rates{}) || bouncy.Day.HardBouncePercent != nil ||
		bouncy.Week.Rates != (Rates{Sent: 2000, HardBounces: 50}) || !pctIs(bouncy.Week.HardBouncePercent, 2.5) ||
		!pctIs(bouncy.Week.ComplaintPercent, 0) || !bouncy.OverWarning {
		t.Errorf("bouncy rates = %+v / %+v", bouncy.Day, bouncy.Week)
	}
	if bouncy.ThrottledUntil == nil || !bouncy.ThrottledUntil.Equal(now.Add(5*24*time.Hour)) ||
		bouncy.SendingMode != "full" || bouncy.SendingPausedAt != nil ||
		strings.Join(bouncy.State(), ", ") != "throttled until 2026-11-25 13:00 UTC" {
		t.Errorf("bouncy state = %v", bouncy.State())
	}

	// One complaint in 100 is 1%, but too few to judge.
	tiny := byName["Tiny Shop"]
	if tiny.Week.Rates != (Rates{Sent: 100, Complaints: 1}) || !pctIs(tiny.Week.ComplaintPercent, 1) || tiny.OverWarning {
		t.Errorf("tiny = %+v, over %v", tiny.Week, tiny.OverWarning)
	}
	clean := byName["Clean Co"]
	if clean.Week.Rates != (Rates{Sent: 600}) || clean.OverWarning || clean.ThrottledUntil != nil || len(clean.State()) != 0 {
		t.Errorf("clean = %+v, state %v", clean.Week, clean.State())
	}
	// Complaints a day and a minute ago: in the 7 days, not the 24 hours.
	old := byName["Old News"]
	if old.Week.Rates != (Rates{Complaints: 2}) || old.Week.ComplaintPercent != nil || old.Day.Rates != (Rates{}) || old.OverWarning {
		t.Errorf("old = %+v / %+v", old.Day, old.Week)
	}

	// Each account counted as the platform is: the accounts add up to the
	// platform's rates.
	var sum Rates
	for _, a := range rep {
		sum.Sent += a.Week.Sent
		sum.HardBounces += a.Week.HardBounces
		sum.Complaints += a.Week.Complaints
	}
	platform, err := platformRates(ctx, h.db, now.Add(-DeliverabilityWindow), now)
	if err != nil || platform != sum || sum != (Rates{Sent: 3700, HardBounces: 53, Complaints: 5}) {
		t.Errorf("platform %+v, accounts add up to %+v, %v", platform, sum, err)
	}

	// A configured warning is used: at 0.3% complaints and 3% bounces
	// neither is over.
	rep, err = DeliverabilityReport(ctx, h.db, now, Warning{ComplaintsPer10k: 30, HardBouncesPer10k: 300})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range rep {
		if a.OverWarning {
			t.Errorf("%s over 0.3%%/3%%: %+v", a.AccountName, a.Week)
		}
	}
	// Nothing sent: an empty report, not nil.
	rep, err = DeliverabilityReport(ctx, h.db, now.AddDate(1, 0, 0), Warning{})
	if err != nil || rep == nil || len(rep) != 0 {
		t.Errorf("a year on = %v, %v", rep, err)
	}
}

// TestSortWorstFirst orders by being over the warning, then the 7-day and
// 24-hour rates, then copies sent, then name.
func TestSortWorstFirst(t *testing.T) {
	row := func(name string, over bool, week, day Rates) AccountDeliverability {
		return AccountDeliverability{AccountID: name, AccountName: name, OverWarning: over,
			Week: windowRates(week), Day: windowRates(day)}
	}
	rows := []AccountDeliverability{
		row("nothing-sent", false, Rates{Complaints: 3}, Rates{}),
		row("big-clean", false, Rates{Sent: 9000}, Rates{}),
		row("small-clean", false, Rates{Sent: 10}, Rates{}),
		row("b-same", false, Rates{Sent: 10}, Rates{}),
		row("day-worse", false, Rates{Sent: 1000, HardBounces: 10}, Rates{Sent: 100, HardBounces: 10}),
		row("day-better", false, Rates{Sent: 1000, HardBounces: 10}, Rates{Sent: 100, HardBounces: 1}),
		row("over-bounces", true, Rates{Sent: 1000, HardBounces: 30}, Rates{}),
		row("over-complaints", true, Rates{Sent: 1000, Complaints: 5}, Rates{}),
		row("high-not-judged", false, Rates{Sent: 10, Complaints: 1}, Rates{}),
	}
	SortWorstFirst(rows, Warning{})
	var got []string
	for _, r := range rows {
		got = append(got, r.AccountName)
	}
	want := "over-complaints, over-bounces, high-not-judged, day-worse, day-better, big-clean, b-same, small-clean, nothing-sent"
	if strings.Join(got, ", ") != want {
		t.Errorf("order = %s\nwant    %s", strings.Join(got, ", "), want)
	}
}

// TestDigestDeliverability lists the accounts over the early warning in
// the digest, with their state and commands (MG-82).
func TestDigestDeliverability(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 20, 13, 0, 0, 0, time.UTC)
	ids := deliverabilityData(t, h, now)

	sent, err := h.digestJob(&now).RunOnce(ctx)
	if err != nil || !sent {
		t.Fatalf("sent %v, %v", sent, err)
	}
	last, _ := h.fake.Last()
	m := last.Message
	if m.Subject != "mailgoose: abuse digest for 2026-11-20, 2 accounts over the early warning, platform rates over the early warning" {
		t.Errorf("subject %q", m.Subject)
	}
	loud, bouncy := ids["Loud Lists"], ids["Bouncy Co"]
	for _, want := range []string{
		"Accounts over the early warning in the last 7 days (2)\n",
		"listed when over 0.05% complaints (and at least 2) or 2% hard bounces, on 500 or more copies. Worst first.",
		"- Loud Lists (account " + loud + "): last 7 days 1,000 sent, 3 hard bounces (0.3%), 2 complaints (0.2%). " +
			"Last 24 hours 1,000 sent, 3 hard bounces (0.3%), 2 complaints (0.2%). Now: paused, test mode.\n" +
			"    mailgoose admin account " + loud + "\n\n- Bouncy Co",
		"- Bouncy Co (account " + bouncy + "): last 7 days 2,000 sent, 50 hard bounces (2.5%), 0 complaints (0%). " +
			"Last 24 hours nothing sent. Now: throttled until 2026-11-25 13:00 UTC.\n" +
			"    mailgoose admin account " + bouncy + "\n    mailgoose admin pause " + bouncy + ` --reason "..."`,
	} {
		if !strings.Contains(m.Text, want) {
			t.Errorf("missing %q in:\n%s", want, m.Text)
		}
	}
	for _, not := range []string{"Tiny Shop", "Clean Co", "Old News", "Quiet Ltd", "more: mailgoose admin deliverability"} {
		if strings.Contains(m.Text, not) {
			t.Errorf("listed %q:\n%s", not, m.Text)
		}
	}
	// Before the platform rates.
	if strings.Index(m.Text, "Accounts over the early warning") > strings.Index(m.Text, "Platform rates") {
		t.Errorf("section after the platform rates:\n%s", m.Text)
	}
}

// TestDigestDeliverabilityAlone sends the digest for an account over the
// early warning when nothing else would.
func TestDigestDeliverabilityAlone(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 20, 13, 0, 0, 0, time.UTC)
	over := h.digestAccount(t, "Bouncy Co", "bouncy@example.com")
	c := h.sentCampaign(t, over, 500, now.Add(-3*24*time.Hour))
	h.reported(t, c, "campaign", "bounce", "permanent/general", 0, 11, now.Add(-3*24*time.Hour))
	h.sentCampaign(t, h.acc, 20000, now.Add(-2*time.Hour))

	tx, err := h.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	d, err := BuildDigest(ctx, tx, now, Warning{})
	_ = tx.Rollback(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if d.Items() != 0 || d.RatesHigh() || d.AccountsOver != 1 || len(d.Accounts) != 1 || d.Accounts[0].AccountID != over || d.Empty() {
		t.Fatalf("digest: items %d, rates high %v, over %d %+v, empty %v", d.Items(), d.RatesHigh(), d.AccountsOver, d.Accounts, d.Empty())
	}
	if sent, err := h.digestJob(&now).RunOnce(ctx); err != nil || !sent {
		t.Fatalf("sent %v, %v", sent, err)
	}
	last, _ := h.fake.Last()
	if m := last.Message; m.Subject != "mailgoose: abuse digest for 2026-11-20, 1 account over the early warning" ||
		!strings.Contains(m.Text, "Accounts over the early warning in the last 7 days (1)") ||
		!strings.Contains(m.Text, "500 sent, 11 hard bounces (2.2%)") {
		t.Errorf("message %q:\n%s", m.Subject, m.Text)
	}
}

// TestDigestDeliverabilityCapped lists at most DigestMaxAccounts and says
// how many more there are.
func TestDigestDeliverabilityCapped(t *testing.T) {
	d := Digest{At: time.Date(2026, 11, 20, 13, 0, 0, 0, time.UTC), AccountsOver: DigestMaxAccounts + 2}
	for i := range DigestMaxAccounts {
		d.Accounts = append(d.Accounts, AccountDeliverability{AccountID: fmt.Sprint(i), AccountName: fmt.Sprint("Account ", i),
			OverWarning: true, Week: windowRates(Rates{Sent: 500, HardBounces: 20})})
	}
	text := d.Text()
	if !strings.Contains(text, fmt.Sprintf("(%d)\n", DigestMaxAccounts+2)) || !strings.Contains(text, "\n... and 2 more: mailgoose admin deliverability\n") ||
		strings.Count(text, "): last 7 days 500 sent") != DigestMaxAccounts {
		t.Errorf("text:\n%s", text)
	}
	if d.Empty() || !strings.HasSuffix(d.Subject(), ", 22 accounts over the early warning") {
		t.Errorf("empty %v, subject %q", d.Empty(), d.Subject())
	}
}
