package sending

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gremlin-technologies/mailgoose/internal/mail"
)

// Account-level pauses (MG-88): the account's last 30 days are judged
// whatever the size of each campaign.

// checkOn runs CheckCampaign for an event on the campaign.
func (h *harness) checkOn(t *testing.T, campaignID string) Health {
	t.Helper()
	ctx := context.Background()
	var out Health
	if err := pgx.BeginFunc(ctx, h.db, func(tx pgx.Tx) error {
		var err error
		out, err = CheckCampaign(ctx, tx, campaignID, h.now, Warning{})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// unpause lifts the account's pause as an admin does, logged at at.
func (h *harness) unpause(t *testing.T, actor string, at time.Time) {
	t.Helper()
	ctx := context.Background()
	if _, err := h.db.Exec(ctx, `
		UPDATE accounts SET sending_paused_at = NULL, sending_paused_reason = NULL WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(ctx, `
		INSERT INTO admin_actions (actor, action, account_id, reason, created_at) VALUES ($1, 'unpause', $2, 'Looked into it.', $3)`,
		actor, h.acc, at); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) campaignMarked(t *testing.T, campaignID string) bool {
	t.Helper()
	var marked bool
	if err := h.db.QueryRow(context.Background(), `SELECT auto_paused_at IS NOT NULL FROM campaigns WHERE id = $1`,
		campaignID).Scan(&marked); err != nil {
		t.Fatal(err)
	}
	return marked
}

func TestAccountPauseOnComplaintsAcrossSmallCampaigns(t *testing.T) {
	h := newHarness(t)
	// One campaign of 300 with a complaint: under 500 copies in 30 days,
	// and too small to be judged alone.
	a := h.sentCampaign(t, h.acc, 300, h.now.AddDate(0, 0, -20))
	h.reported(t, a, "campaign", "complaint", "abuse", 0, 1, h.now.AddDate(0, 0, -19))
	if hl := h.checkOn(t, a); hl.Pause != nil || hl.Throttle != nil {
		t.Fatalf("300 copies: %+v", hl)
	}
	// A second of 300 with one more: 2 of 600 is 0.33%, over 0.1%, though
	// neither campaign is judged on its own.
	b := h.sentCampaign(t, h.acc, 300, h.now.AddDate(0, 0, -2))
	h.reported(t, b, "campaign", "complaint", "abuse", 0, 1, h.now.AddDate(0, 0, -1))
	hl := h.checkOn(t, b)
	ap := hl.Pause
	if ap == nil || hl.Throttle != nil {
		t.Fatalf("not paused at 2 of 600 over 30 days: %+v", hl)
	}
	if !ap.Account || ap.AccountID != h.acc || ap.AccountName != "Pat's Bakery" || ap.CampaignID != b ||
		ap.Rates != (Rates{Sent: 600, Complaints: 2}) || !ap.Since.Equal(h.now.Add(-HealthWindow)) {
		t.Errorf("pause = %+v", ap)
	}
	paused, reason := h.paused(t)
	want := "It was paused automatically: over the last 30 days, 0.33% of the people sent this account's campaigns " +
		"marked them as spam (2 of 600), over the 0.1% limit for an account."
	if !paused || reason != want {
		t.Errorf("paused %v, reason %q\nwant %q", paused, reason, want)
	}
	// Logged as an automatic pause, with the account's rates.
	logged := h.autoPauses(t)
	if len(logged) != 1 || logged[0]["actor"] != "automatic" || logged[0]["target"] != b || logged[0]["reason"] != want {
		t.Fatalf("logged %+v", logged)
	}
	d := logged[0]["details"].(map[string]any)
	if d["scope"] != "account" || d["sent"] != float64(600) || d["complaints"] != float64(2) {
		t.Errorf("details %+v", d)
	}
	// No campaign is marked as having paused it, so each can still pause
	// on its own later.
	if h.campaignMarked(t, a) || h.campaignMarked(t, b) {
		t.Error("a campaign was marked")
	}
	// While it's paused, more events change nothing and log nothing.
	h.reported(t, b, "campaign", "complaint", "abuse", 1, 1, h.now)
	if hl := h.checkOn(t, b); hl.Pause != nil {
		t.Errorf("paused again while paused: %+v", hl.Pause)
	}
	if n := len(h.autoPauses(t)); n != 1 {
		t.Errorf("%d pauses logged", n)
	}
}

func TestAccountPauseOnHardBouncesAcrossSmallCampaigns(t *testing.T) {
	h := newHarness(t)
	var last string
	for i := range 3 {
		last = h.sentCampaign(t, h.acc, 200, h.now.AddDate(0, 0, -10+i))
		h.reported(t, last, "campaign", "bounce", "permanent/General", 0, 7, h.now.AddDate(0, 0, -10+i))
		// Soft bounces don't count.
		h.reported(t, last, "campaign", "bounce", "transient/MailboxFull", 7, 50, h.now.AddDate(0, 0, -10+i))
		ap := h.checkOn(t, last).Pause
		// 3.5% each time, but only the third makes 500 copies.
		if i < 2 && ap != nil {
			t.Fatalf("paused at %d copies: %+v", 200*(i+1), ap)
		}
		if i == 2 && (ap == nil || !ap.Account || ap.Rates != (Rates{Sent: 600, HardBounces: 21})) {
			t.Fatalf("at 600 copies: %+v", ap)
		}
	}
	want := "It was paused automatically: over the last 30 days, 3.5% of the addresses this account's campaigns went to " +
		"don't exist (hard bounces, 21 of 600), over the 3% limit for an account."
	if _, reason := h.paused(t); reason != want {
		t.Errorf("reason %q\nwant %q", reason, want)
	}
}

func TestAccountPauseLeavesHealthyAccounts(t *testing.T) {
	h := newHarness(t)
	// 900 copies over three campaigns: one complaint (under the two the
	// rate counts from) and 2.9% hard bounces.
	var ids []string
	for i := range 3 {
		ids = append(ids, h.sentCampaign(t, h.acc, 300, h.now.AddDate(0, 0, -9+3*i)))
	}
	h.reported(t, ids[0], "campaign", "complaint", "abuse", 0, 1, h.now.AddDate(0, 0, -8))
	h.reported(t, ids[1], "campaign", "bounce", "permanent/General", 0, 13, h.now.AddDate(0, 0, -5))
	h.reported(t, ids[2], "campaign", "bounce", "permanent/General", 0, 13, h.now.AddDate(0, 0, -2))
	for _, id := range ids {
		if hl := h.checkOn(t, id); hl.Pause != nil || hl.Throttle != nil {
			t.Fatalf("healthy account: %+v", hl)
		}
	}
	if paused, _ := h.paused(t); paused || len(h.autoPauses(t)) != 0 {
		t.Error("paused")
	}
}

func TestAccountPauseCountsOnlyTheLast30Days(t *testing.T) {
	h := newHarness(t)
	// 300 copies 40 days ago, all bounced then, don't count: the 400 in
	// the last 30 days, at 5% hard bounces, are under 500 copies.
	old := h.sentCampaign(t, h.acc, 300, h.now.AddDate(0, 0, -40))
	h.reported(t, old, "campaign", "bounce", "permanent/General", 0, 300, h.now.AddDate(0, 0, -40))
	recent := h.sentCampaign(t, h.acc, 400, h.now.AddDate(0, 0, -1))
	h.reported(t, recent, "campaign", "bounce", "permanent/General", 0, 20, h.now)
	if hl := h.checkOn(t, recent); hl.Pause != nil {
		t.Fatalf("paused on 400 copies in 30 days: %+v", hl.Pause)
	}
	if paused, _ := h.paused(t); paused || len(h.autoPauses(t)) != 0 {
		t.Error("paused")
	}
}

func TestAccountPauseKeepsAnExistingPause(t *testing.T) {
	h := newHarness(t)
	if _, err := h.db.Exec(context.Background(), `
		UPDATE accounts SET sending_paused_at = $2, sending_paused_reason = 'Checking your list.' WHERE id = $1`,
		h.acc, h.now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	c := h.sentCampaign(t, h.acc, 300, h.now.AddDate(0, 0, -2))
	h.reported(t, c, "campaign", "bounce", "permanent/General", 0, 10, h.now)
	c2 := h.sentCampaign(t, h.acc, 300, h.now.AddDate(0, 0, -1))
	h.reported(t, c2, "campaign", "bounce", "permanent/General", 0, 10, h.now)
	// 600 copies, 20 hard bounces (3.3%), in two campaigns of 300.
	if hl := h.checkOn(t, c2); hl.Pause != nil {
		t.Fatalf("reported a pause of a paused account: %+v", hl.Pause)
	}
	if _, reason := h.paused(t); reason != "Checking your list." {
		t.Errorf("reason replaced: %q", reason)
	}
	if n := len(h.autoPauses(t)); n != 0 {
		t.Errorf("%d pauses logged", n)
	}
}

func TestAccountPauseNeedsNewEvidenceAfterUnpause(t *testing.T) {
	h := newHarness(t)
	a := h.sentCampaign(t, h.acc, 300, h.now.AddDate(0, 0, -5))
	b := h.sentCampaign(t, h.acc, 300, h.now.AddDate(0, 0, -4))
	h.reported(t, a, "campaign", "complaint", "abuse", 0, 1, h.now.AddDate(0, 0, -3))
	h.reported(t, b, "campaign", "complaint", "abuse", 0, 1, h.now.AddDate(0, 0, -3))
	if ap := h.checkOn(t, b).Pause; ap == nil {
		t.Fatal("not paused")
	}
	// An admin lifts it. The same 30 days, and late complaints on the
	// copies they looked at, don't pause it again.
	h.now = h.now.Add(time.Hour)
	h.unpause(t, "admin@mailgoose.example", h.now)
	h.now = h.now.Add(time.Hour)
	h.reported(t, a, "campaign", "complaint", "abuse", 1, 3, h.now)
	if ap := h.checkOn(t, a).Pause; ap != nil {
		t.Fatalf("paused again on the old copies: %+v", ap)
	}
	// New copies since the unpause are judged on their own: 400 with two
	// complaints are under 500 copies...
	c := h.sentCampaign(t, h.acc, 400, h.now)
	h.now = h.now.Add(time.Hour)
	h.reported(t, c, "campaign", "complaint", "abuse", 0, 2, h.now)
	if ap := h.checkOn(t, c).Pause; ap != nil {
		t.Fatalf("paused on 400 new copies: %+v", ap)
	}
	// ...and 700 are over the line: paused again, saying since when.
	unpausedAt := h.now.Add(-2 * time.Hour)
	d := h.sentCampaign(t, h.acc, 300, h.now)
	h.reported(t, d, "campaign", "complaint", "abuse", 0, 1, h.now)
	ap := h.checkOn(t, d).Pause
	if ap == nil || ap.Rates != (Rates{Sent: 700, Complaints: 3}) || ap.After != "unpause" || !ap.Since.Equal(unpausedAt) {
		t.Fatalf("pause = %+v", ap)
	}
	want := "It was paused automatically: since sending was last unpaused (Nov 18, 2026), 0.43% of the people sent " +
		"this account's campaigns marked them as spam (3 of 700), over the 0.1% limit for an account."
	if _, reason := h.paused(t); reason != want {
		t.Errorf("reason %q\nwant %q", reason, want)
	}
	// It's the second automatic pause in 30 days, so it suspends the
	// account (MG-65), as any other would.
	if ap.Suspension == nil || !strings.Contains(ap.Suspension.Reason, "paused automatically 2 times in 30 days") ||
		!strings.Contains(ap.Suspension.Reason, "since sending was last unpaused") {
		t.Errorf("suspension = %+v", ap.Suspension)
	}
}

func TestAccountPauseIgnoresAutomaticUnpause(t *testing.T) {
	h := newHarness(t)
	// A plan payment going through lifts its own pause automatically;
	// nobody looked at the sending, so the 30 days still count.
	h.unpause(t, "automatic", h.now.Add(-time.Hour))
	c := h.sentCampaign(t, h.acc, 300, h.now.AddDate(0, 0, -3))
	d := h.sentCampaign(t, h.acc, 300, h.now.AddDate(0, 0, -2))
	h.reported(t, c, "campaign", "complaint", "abuse", 0, 1, h.now.AddDate(0, 0, -2))
	h.reported(t, d, "campaign", "complaint", "abuse", 0, 1, h.now.AddDate(0, 0, -1))
	if ap := h.checkOn(t, d).Pause; ap == nil || ap.After != "" {
		t.Fatalf("pause = %+v", ap)
	}
}

func TestCampaignPauseComesFirst(t *testing.T) {
	h := newHarness(t)
	// A campaign of 500 or more over the line pauses as before, with the
	// campaign's reason and mark.
	c := h.sentCampaign(t, h.acc, 1000, h.now.AddDate(0, 0, -1))
	h.reported(t, c, "campaign", "complaint", "abuse", 0, 2, h.now)
	ap := h.checkOn(t, c).Pause
	if ap == nil || ap.Account || ap.Rates != (Rates{Sent: 1000, Complaints: 2}) || !h.campaignMarked(t, c) {
		t.Fatalf("pause = %+v", ap)
	}
	if _, reason := h.paused(t); !strings.Contains(reason, `0.2% of the people sent "Sent" marked it as spam, over the 0.1% limit.`) {
		t.Errorf("reason %q", reason)
	}
	// Once lifted without a log entry, the account's same copies don't
	// pause it again either: the campaign's pause started a new window.
	if _, err := h.db.Exec(context.Background(), `UPDATE accounts SET sending_paused_at = NULL WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	if ap := h.checkOn(t, c).Pause; ap != nil {
		t.Errorf("paused again: %+v", ap)
	}
}

func TestCampaignThrottleWithAccountPause(t *testing.T) {
	h := newHarness(t)
	// Earlier small campaigns are over the pause line together; a campaign
	// of 500 over only the early warning throttles the account, and the
	// account's 30 days then pause it.
	old := h.sentCampaign(t, h.acc, 400, h.now.AddDate(0, 0, -10))
	h.reported(t, old, "campaign", "bounce", "permanent/General", 0, 40, h.now.AddDate(0, 0, -10))
	c := h.sentCampaign(t, h.acc, 500, h.now.AddDate(0, 0, -1))
	h.reported(t, c, "campaign", "bounce", "permanent/General", 0, 12, h.now)
	hl := h.checkOn(t, c)
	if hl.Throttle == nil || hl.Pause == nil || !hl.Pause.Account || hl.Pause.Rates != (Rates{Sent: 900, HardBounces: 52}) {
		t.Fatalf("health = %+v %+v", hl.Throttle, hl.Pause)
	}
}

func TestAccountPauseReasonAndAlert(t *testing.T) {
	since := time.Date(2026, 11, 3, 9, 30, 0, 0, time.UTC)
	ap := &AutoPause{AccountID: "acc-1", AccountName: "Pat's Bakery", CampaignID: "c-1", CampaignName: "Bread week",
		Account: true, Since: since, Rates: Rates{Sent: 1200, Complaints: 1, HardBounces: 40}}
	want := "It was paused automatically: over the last 30 days, 3.33% of the addresses this account's campaigns went to " +
		"don't exist (hard bounces, 40 of 1,200), over the 3% limit for an account."
	if got := accountPauseReason(ap); got != want {
		t.Errorf("reason %q\nwant %q", got, want)
	}
	ap.After = "auto_pause"
	if got := accountPauseReason(ap); !strings.HasPrefix(got, "It was paused automatically: since it was last paused automatically (Nov 3, 2026), 3.33%") {
		t.Errorf("reason %q", got)
	}
	ap.After = ""
	ap.Reason = accountPauseReason(ap)

	fake := &mail.Fake{}
	a := &OpsAlert{Mail: fake, From: mail.Address{Email: "alerts@mail.localhost"}, To: "ops@example.com", Log: slog.New(slog.DiscardHandler)}
	a.AutoPaused(context.Background(), *ap)
	s, ok := fake.Last()
	if !ok || !strings.Contains(s.Message.Text,
		`The account's campaigns since 2026-11-03 09:30 UTC (30 days), the latest event on "Bread week" (c-1): `+
			`1200 sent, 40 hard bounces (3.33%), 1 complaints (0.08%).`) ||
		!strings.Contains(s.Message.Text, want) || !strings.Contains(s.Message.Text, "mailgoose admin unpause acc-1") {
		t.Errorf("alert = %s", s.Message.Text)
	}
}

// The ramp (accountClean) still reads the 30 days as it did: events on
// older copies count.
func TestAccountRatesSentSince(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	old := h.sentCampaign(t, h.acc, 100, h.now.AddDate(0, 0, -40))
	h.reported(t, old, "campaign", "complaint", "abuse", 0, 3, h.now.AddDate(0, 0, -1))
	recent := h.sentCampaign(t, h.acc, 600, h.now.AddDate(0, 0, -2))
	h.reported(t, recent, "campaign", "complaint", "abuse", 0, 1, h.now.AddDate(0, 0, -1))
	since := h.now.Add(-HealthWindow)
	all, err := accountRates(ctx, h.db, h.acc, since, false)
	if err != nil || all != (Rates{Sent: 600, Complaints: 4}) {
		t.Errorf("all = %+v, %v", all, err)
	}
	onSent, err := accountRates(ctx, h.db, h.acc, since, true)
	if err != nil || onSent != (Rates{Sent: 600, Complaints: 1}) {
		t.Errorf("on copies sent = %+v, %v", onSent, err)
	}
	if clean, err := accountClean(ctx, h.db, h.acc, h.now); err != nil || clean {
		t.Errorf("clean = %v, %v", clean, err)
	}
}
