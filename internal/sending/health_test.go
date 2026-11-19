package sending

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gremlin-technologies/mailgoose/internal/campaigns"
	"github.com/gremlin-technologies/mailgoose/internal/mail"
)

func TestLimitSchedule(t *testing.T) {
	day := 24 * time.Hour
	cases := []struct {
		age   time.Duration
		clean bool
		want  int
	}{
		{0, true, 1000},
		{6*day + 23*time.Hour, true, 1000},
		{7 * day, true, 2000},
		{13 * day, true, 2000},
		{14 * day, true, 4000},
		{21 * day, true, 8000},
		{28 * day, true, 10000},
		{400 * day, true, 10000},
		{400 * day, false, 1000},
		{10 * day, false, 1000},
	}
	for _, c := range cases {
		if got := LimitSchedule(1000, 10000, c.age, c.clean); got != c.want {
			t.Errorf("age %s, clean %v: %d, want %d", c.age, c.clean, got, c.want)
		}
	}
	// A usual limit under the new-account one never lowers it.
	if got := LimitSchedule(1000, 500, 30*day, true); got != 1000 {
		t.Errorf("misconfigured: %d", got)
	}
}

func TestRates(t *testing.T) {
	cases := []struct {
		r    Rates
		want bool
	}{
		{Rates{Sent: 499, Complaints: 100}, false}, // too small to judge
		{Rates{Sent: 1000, Complaints: 3}, false},  // 0.3% is the limit, not over it
		{Rates{Sent: 1000, Complaints: 4}, true},
		{Rates{Sent: 1000, HardBounces: 50}, false},
		{Rates{Sent: 1000, HardBounces: 51}, true},
	}
	for _, c := range cases {
		if got := c.r.Unhealthy(); got != c.want {
			t.Errorf("%+v: %v, want %v", c.r, got, c.want)
		}
	}
}

// eventsFor adds SES events for the campaign's first n sent rows.
func (h *harness) eventsFor(t *testing.T, typ, subtype string, n int, at time.Time) {
	t.Helper()
	if _, err := h.db.Exec(context.Background(), `
		INSERT INTO send_events (event_key, account_id, campaign_id, recipient_id, message_id, kind, type, subtype, occurred_at)
		SELECT $3 || coalesce($4, '') || r.id, r.account_id, r.campaign_id, r.id, 'm' || r.id, 'campaign', $3, nullif($4, ''), $5
		FROM campaign_recipients r WHERE r.campaign_id = $1 AND r.status = 'sent' ORDER BY r.id LIMIT $2`,
		h.campaign.ID, n, typ, subtype, at); err != nil {
		t.Fatal(err)
	}
}

// markSent marks every row of the campaign sent, as if the pipeline ran.
func (h *harness) markSent(t *testing.T, at time.Time) {
	t.Helper()
	if _, err := h.db.Exec(context.Background(), `
		UPDATE campaign_recipients SET status = 'sent', sent_at = $2, message_id = 'm' || id WHERE campaign_id = $1`,
		h.campaign.ID, at); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) check(t *testing.T) *AutoPause {
	t.Helper()
	ctx := context.Background()
	var ap *AutoPause
	err := pgx.BeginFunc(ctx, h.db, func(tx pgx.Tx) error {
		var err error
		ap, err = CheckCampaign(ctx, tx, h.campaign.ID, h.now)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return ap
}

func (h *harness) paused(t *testing.T) (bool, string) {
	t.Helper()
	var at *time.Time
	var reason *string
	if err := h.db.QueryRow(context.Background(), `SELECT sending_paused_at, sending_paused_reason FROM accounts WHERE id = $1`,
		h.acc).Scan(&at, &reason); err != nil {
		t.Fatal(err)
	}
	return at != nil, trimmed(reason)
}

func TestAutoPauseOnComplaints(t *testing.T) {
	h := launchedHarness(t, 999)
	h.markSent(t, h.now)
	h.eventsFor(t, "complaint", "abuse", 3, h.now)
	if ap := h.check(t); ap != nil {
		t.Fatalf("paused at 0.3%%: %+v", ap)
	}
	// A fourth complaint is 0.4%: paused.
	if _, err := h.db.Exec(context.Background(), `DELETE FROM send_events`); err != nil {
		t.Fatal(err)
	}
	h.eventsFor(t, "complaint", "abuse", 4, h.now)
	ap := h.check(t)
	if ap == nil {
		t.Fatal("not paused at 0.4%")
	}
	if ap.AccountID != h.acc || ap.CampaignName != "Bread week" || ap.AccountName != "Pat's Bakery" ||
		ap.Rates != (Rates{Sent: 1000, Complaints: 4}) {
		t.Errorf("pause = %+v", ap)
	}
	paused, reason := h.paused(t)
	if !paused || !strings.Contains(reason, `0.4% of the people sent "Bread week" marked it as spam`) {
		t.Errorf("paused %v, reason %q", paused, reason)
	}
	var actor, action string
	var details []byte
	if err := h.db.QueryRow(context.Background(), `SELECT actor, action, details FROM admin_actions WHERE account_id = $1`, h.acc).
		Scan(&actor, &action, &details); err != nil {
		t.Fatal(err)
	}
	var d Rates
	_ = json.Unmarshal(details, &d)
	if actor != "automatic" || action != "auto_pause" || d.Complaints != 4 {
		t.Errorf("logged %s %s %s", actor, action, details)
	}
	// Once lifted, the same campaign doesn't pause it again.
	if _, err := h.db.Exec(context.Background(), `UPDATE accounts SET sending_paused_at = NULL WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	if ap := h.check(t); ap != nil {
		t.Errorf("paused twice for one campaign: %+v", ap)
	}
}

func TestAutoPauseOnHardBounces(t *testing.T) {
	h := launchedHarness(t, 499)
	h.markSent(t, h.now)
	h.eventsFor(t, "bounce", "permanent/General", 25, h.now)
	h.eventsFor(t, "bounce", "transient/MailboxFull", 100, h.now.Add(time.Second))
	if ap := h.check(t); ap != nil {
		t.Fatalf("paused at 5%% (soft bounces don't count): %+v", ap)
	}
	if _, err := h.db.Exec(context.Background(), `DELETE FROM send_events WHERE subtype LIKE 'permanent/%'`); err != nil {
		t.Fatal(err)
	}
	h.eventsFor(t, "bounce", "permanent/General", 26, h.now)
	if ap := h.check(t); ap == nil {
		t.Fatal("not paused at 5.2%")
	}
	if _, reason := h.paused(t); !strings.Contains(reason, `5.2% of the addresses "Bread week" went to don't exist`) {
		t.Errorf("reason %q", reason)
	}
}

func TestAutoPauseKeepsAnAdminPause(t *testing.T) {
	h := launchedHarness(t, 499)
	h.markSent(t, h.now)
	if _, err := h.db.Exec(context.Background(), `
		UPDATE accounts SET sending_paused_at = $2, sending_paused_reason = 'Checking your list.' WHERE id = $1`,
		h.acc, h.now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	h.eventsFor(t, "complaint", "", 5, h.now)
	if ap := h.check(t); ap == nil {
		t.Fatal("not reported")
	}
	if _, reason := h.paused(t); reason != "Checking your list." {
		t.Errorf("reason replaced: %q", reason)
	}
}

func TestOpsAlert(t *testing.T) {
	fake := &mail.Fake{}
	ap := AutoPause{AccountID: "acc-1", AccountName: "Pat's Bakery", CampaignID: "c-1", CampaignName: "Bread week",
		Rates: Rates{Sent: 1000, Complaints: 4}, Reason: "It was paused automatically."}
	quiet := slog.New(slog.DiscardHandler)
	(&OpsAlert{Mail: fake, Log: quiet}).AutoPaused(context.Background(), ap)
	if len(fake.Sent()) != 0 {
		t.Error("emailed with no ops address")
	}
	a := &OpsAlert{Mail: fake, From: mail.Address{Name: "mailgoose", Email: "alerts@mail.localhost"}, To: "ops@example.com", Log: quiet}
	a.AutoPaused(context.Background(), ap)
	s, ok := fake.Last()
	if !ok || s.Message.To.Email != "ops@example.com" || s.Message.Kind != mail.KindSystem ||
		!strings.Contains(s.Message.Subject, "Pat's Bakery paused") ||
		!strings.Contains(s.Message.Text, "4 complaints (0.4%)") || !strings.Contains(s.Message.Text, "mailgoose admin unpause acc-1") {
		t.Errorf("alert = %+v", s.Message)
	}
}

func TestDailyLimitGrowsWhileClean(t *testing.T) {
	h := launchedHarness(t, 99)
	ctx := context.Background()
	p := h.pipeline(Limits{PerSecond: 100000, Daily: 50, NewDaily: 10})
	limit := func() int {
		t.Helper()
		n, err := p.dailyLimit(ctx, h.db, h.acc, nil, h.now.AddDate(0, 0, -15), h.now)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	// Two weeks clean: four times the new-account limit.
	if got := limit(); got != 40 {
		t.Fatalf("clean at 15 days: %d, want 40", got)
	}
	// Complaints over 0.3% of 500 or more copies in the last 30 days hold
	// it at the new-account limit.
	h.markSent(t, h.now.AddDate(0, 0, -3))
	if _, err := h.db.Exec(ctx, `
		INSERT INTO campaign_recipients (account_id, campaign_id, status, sent_at, message_id)
		SELECT $1, $2, 'sent', $3, 'x' || i FROM generate_series(1, 400) i`, h.acc, h.campaign.ID, h.now.AddDate(0, 0, -3)); err != nil {
		t.Fatal(err)
	}
	h.eventsFor(t, "complaint", "", 1, h.now.AddDate(0, 0, -2))
	if got := limit(); got != 40 {
		t.Fatalf("1 of 500: %d, want 40", got)
	}
	h.eventsFor(t, "bounce", "permanent/General", 30, h.now.AddDate(0, 0, -2))
	if got := limit(); got != 10 {
		t.Errorf("30 hard bounces in 500: %d, want 10", got)
	}
	// They age out after 30 days.
	h.now = h.now.AddDate(0, 0, 31)
	if got, err := p.dailyLimit(ctx, h.db, h.acc, nil, h.now.AddDate(0, 0, -46), h.now); err != nil || got != 50 {
		t.Errorf("a month later: %d, %v; want 50", got, err)
	}
	// A campaign that paused the account holds it too.
	if _, err := h.db.Exec(ctx, `UPDATE campaigns SET auto_paused_at = $2 WHERE id = $1`, h.campaign.ID, h.now.AddDate(0, 0, -1)); err != nil {
		t.Fatal(err)
	}
	if got, _ := p.dailyLimit(ctx, h.db, h.acc, nil, h.now.AddDate(0, 0, -46), h.now); got != 10 {
		t.Errorf("after an automatic pause: %d, want 10", got)
	}
}

// pausing is a sender that runs f before each send, counting from 1.
type pausing struct {
	mail.Fake
	n int
	f func(n int)
}

func (s *pausing) Send(ctx context.Context, m mail.Message) (string, error) {
	s.n++
	s.f(s.n)
	return s.Fake.Send(ctx, m)
}

func TestPipelineAccountPausedMidBatch(t *testing.T) {
	h := launchedHarness(t, 19)
	ctx := context.Background()
	p := h.pipeline(fastLimits)
	sender := &pausing{f: func(n int) {
		if n == 5 {
			if _, err := h.db.Exec(ctx, `UPDATE accounts SET sending_paused_at = now() WHERE id = $1`, h.acc); err != nil {
				t.Error(err)
			}
		}
	}}
	p.Mail = sender
	q := &recorded{}
	dispatch(t, p, q)
	batches := q.take()
	if len(batches) != 1 {
		t.Fatalf("%d batches", len(batches))
	}
	if err := p.HandleBatch(ctx, batches[0], 1); err != nil {
		t.Fatal(err)
	}
	// The fifth copy was already claimed when the pause came; nothing after.
	if n := len(sender.Sent()); n != 5 {
		t.Fatalf("%d sent after the pause, want 5", n)
	}
	var waiting int
	_ = h.db.QueryRow(ctx, `SELECT count(*) FROM campaign_recipients WHERE campaign_id = $1 AND status = 'queued' AND enqueued_at IS NULL`,
		h.campaign.ID).Scan(&waiting)
	if waiting != 15 {
		t.Errorf("%d rows handed back, want 15", waiting)
	}
	// While paused, nothing is batched; unpaused, the rest go.
	dispatch(t, p, q)
	if len(q.take()) != 0 {
		t.Error("batched a paused account")
	}
	if _, err := h.db.Exec(ctx, `UPDATE accounts SET sending_paused_at = NULL WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	dispatch(t, p, q)
	for _, b := range q.take() {
		if err := p.HandleBatch(ctx, b, 1); err != nil {
			t.Fatal(err)
		}
	}
	dispatch(t, p, q)
	if got := perAddress(sender.Sent()); len(got) != 20 || len(sender.Sent()) != 20 || h.status(t) != campaigns.StatusSent {
		t.Errorf("%d sent to %d addresses, status %s", len(sender.Sent()), len(got), h.status(t))
	}
}

// A copy claimed when its campaign was stopped, whose worker then died, is
// failed and its credit given back, though the dispatcher no longer visits
// the campaign (from the MG-34 review).
func TestFailStoppedClaims(t *testing.T) {
	h := launchedHarness(t, 4)
	ctx := context.Background()
	p := h.pipeline(fastLimits)
	q := &recorded{}
	dispatch(t, p, q)
	b := q.take()[0]
	// A worker claims the first copy and dies.
	if _, err := h.db.Exec(ctx, `UPDATE campaign_recipients SET status = 'sending', claimed_at = $2 WHERE id = $1`,
		b.IDs[0], h.now); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Stop(ctx, h.acc, h.campaign.ID); err != nil {
		t.Fatal(err)
	}
	before := h.balance(t)
	// Not yet stale: left alone.
	if n, err := FailStoppedClaims(ctx, h.db, h.now.Add(ClaimTimeout-time.Second)); err != nil || n != 0 {
		t.Fatalf("early: %d %v", n, err)
	}
	if n, err := FailStoppedClaims(ctx, h.db, h.now.Add(ClaimTimeout)); err != nil || n != 1 {
		t.Fatalf("failed %d, %v", n, err)
	}
	if c := h.counts(t); c["failed"] != 1 || c["sending"] != 0 || c["skipped"] != 4 {
		t.Errorf("counts %v", c)
	}
	if got := h.balance(t); got != before+1 {
		t.Errorf("balance %d, want %d", got, before+1)
	}
	// Once only.
	if n, _ := FailStoppedClaims(ctx, h.db, h.now.Add(ClaimTimeout)); n != 0 {
		t.Errorf("failed %d more", n)
	}
}

// A sending campaign's stale claims are the dispatcher's, not the sweep's.
func TestFailStoppedClaimsLeavesSending(t *testing.T) {
	h := launchedHarness(t, 1)
	ctx := context.Background()
	q := &recorded{}
	dispatch(t, h.pipeline(fastLimits), q)
	b := q.take()[0]
	if _, err := h.db.Exec(ctx, `UPDATE campaign_recipients SET status = 'sending', claimed_at = $2 WHERE id = $1`,
		b.IDs[0], h.now); err != nil {
		t.Fatal(err)
	}
	if n, err := FailStoppedClaims(ctx, h.db, h.now.Add(time.Hour)); err != nil || n != 0 {
		t.Errorf("failed %d, %v", n, err)
	}
}

func TestStopCampaignInFlight(t *testing.T) {
	h := launchedHarness(t, 19)
	ctx := context.Background()
	p := h.pipeline(fastLimits)
	before := h.balance(t)
	sender := &pausing{f: func(n int) {
		if n == 3 {
			if _, err := h.svc.Stop(ctx, h.acc, h.campaign.ID); err != nil {
				t.Error(err)
			}
		}
	}}
	p.Mail = sender
	q := &recorded{}
	dispatch(t, p, q)
	if err := p.HandleBatch(ctx, q.take()[0], 1); err != nil {
		t.Fatal(err)
	}
	if n := len(sender.Sent()); n != 3 {
		t.Errorf("%d sent, want 3", n)
	}
	c := h.counts(t)
	if c["sent"] != 3 || c["skipped"] != 17 || h.status(t) != campaigns.StatusCancelled {
		t.Errorf("%v, status %s", c, h.status(t))
	}
	if got := h.balance(t); got != before+17 {
		t.Errorf("balance %d, want %d: the 17 not sent come back", got, before+17)
	}
	var note string
	_ = h.db.QueryRow(ctx, `SELECT error FROM campaign_recipients WHERE campaign_id = $1 AND status = 'skipped' LIMIT 1`, h.campaign.ID).Scan(&note)
	if note != "stopped by mailgoose" {
		t.Errorf("note %q", note)
	}
	// Stopping again changes nothing; a draft can't be stopped.
	if _, err := h.svc.Stop(ctx, h.acc, h.campaign.ID); err != nil {
		t.Errorf("again: %v", err)
	}
	name := "Another"
	draft, err := h.svc.Campaigns.Create(ctx, h.acc, campaigns.Changes{Name: &name, EmailID: &h.email.ID, Recipients: &campaigns.Recipients{All: true}})
	if err != nil {
		t.Fatal(err)
	}
	var st *campaigns.StatusError
	if _, err := h.svc.Stop(ctx, h.acc, draft.ID); !errors.As(err, &st) || st.Status != campaigns.StatusDraft {
		t.Errorf("draft: %v", err)
	}
}
