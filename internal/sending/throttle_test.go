package sending

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gremlin-technologies/mailgoose/internal/mail"
)

func TestOverWarning(t *testing.T) {
	cases := []struct {
		r    Rates
		w    Warning
		want bool
	}{
		{Rates{Sent: 499, Complaints: 100}, Warning{}, false}, // too small to judge
		{Rates{Sent: 1000, Complaints: 1}, Warning{}, false},  // 0.1% is the limit, not over it
		{Rates{Sent: 1000, Complaints: 2}, Warning{}, true},
		{Rates{Sent: 1000, HardBounces: 20}, Warning{}, false},
		{Rates{Sent: 1000, HardBounces: 21}, Warning{}, true},
		{Rates{Sent: 1000, Complaints: 2}, Warning{ComplaintsPer10k: 20, HardBouncesPer10k: 200}, false},
		{Rates{Sent: 1000, HardBounces: 21}, Warning{ComplaintsPer10k: 10, HardBouncesPer10k: 300}, false},
	}
	for _, c := range cases {
		if got := c.r.OverWarning(c.w); got != c.want {
			t.Errorf("%+v %+v: %v, want %v", c.r, c.w, got, c.want)
		}
	}
}

func TestThousands(t *testing.T) {
	for n, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 10000: "10,000", 1234567: "1,234,567", -2500: "-2,500"} {
		if got := thousands(n); got != want {
			t.Errorf("%d: %q, want %q", n, got, want)
		}
	}
}

// throttles is the account's throttle rows' reasons, oldest first.
func (h *harness) throttles(t *testing.T) []string {
	t.Helper()
	rows, err := h.db.Query(context.Background(), `SELECT reason FROM account_throttles WHERE account_id = $1 ORDER BY id`, h.acc)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func TestThrottleOnComplaints(t *testing.T) {
	h := launchedHarness(t, 999)
	ctx := context.Background()
	h.markSent(t, h.now)
	h.eventsFor(t, "complaint", "abuse", 1, h.now)
	if got := h.checkHealth(t); got.Pause != nil || got.Throttle != nil {
		t.Fatalf("acted at 0.1%%: %+v", got)
	}
	if _, err := h.db.Exec(ctx, `DELETE FROM send_events`); err != nil {
		t.Fatal(err)
	}
	h.eventsFor(t, "complaint", "abuse", 2, h.now)
	got := h.checkHealth(t)
	th := got.Throttle
	if got.Pause != nil || th == nil {
		t.Fatalf("at 0.2%%: %+v", got)
	}
	if th.AccountID != h.acc || th.AccountName != "Pat's Bakery" || th.OwnerEmail == "" || th.CampaignName != "Bread week" ||
		th.Rates != (Rates{Sent: 1000, Complaints: 2}) || !th.Until.Equal(h.now.Add(7*24*time.Hour)) {
		t.Errorf("throttle = %+v", th)
	}
	if !strings.Contains(th.Reason, `0.2% of the people sent "Bread week" marked it as spam, over the 0.1% early-warning limit`) {
		t.Errorf("reason %q", th.Reason)
	}
	if paused, _ := h.paused(t); paused {
		t.Error("a throttle paused the account")
	}
	if got := h.throttles(t); len(got) != 1 {
		t.Errorf("throttles %v", got)
	}
	var actor, action, target string
	var details []byte
	if err := h.db.QueryRow(ctx, `SELECT actor, action, target_id, details FROM admin_actions WHERE account_id = $1`, h.acc).
		Scan(&actor, &action, &target, &details); err != nil {
		t.Fatal(err)
	}
	var d struct {
		Complaints int       `json:"complaints"`
		Until      time.Time `json:"until"`
	}
	_ = json.Unmarshal(details, &d)
	if actor != "automatic" || action != "throttle" || target != h.campaign.ID || d.Complaints != 2 || d.Until.IsZero() {
		t.Errorf("logged %s %s %s %s", actor, action, target, details)
	}
	// One throttle per campaign.
	h.eventsFor(t, "bounce", "permanent/General", 30, h.now.Add(time.Second))
	if got := h.checkHealth(t); got.Throttle != nil || got.Pause != nil {
		t.Errorf("acted twice: %+v", got)
	}
	// It can still pause it if it gets past the pause thresholds.
	h.eventsFor(t, "complaint", "other", 4, h.now.Add(2*time.Second))
	if got := h.checkHealth(t); got.Pause == nil {
		t.Error("a throttled campaign over 0.3% didn't pause")
	}
}

func TestThrottleOnHardBounces(t *testing.T) {
	h := launchedHarness(t, 999)
	h.markSent(t, h.now)
	h.eventsFor(t, "bounce", "permanent/General", 20, h.now)
	if got := h.checkHealth(t); got.Throttle != nil {
		t.Fatalf("throttled at 2%%: %+v", got.Throttle)
	}
	if _, err := h.db.Exec(context.Background(), `DELETE FROM send_events`); err != nil {
		t.Fatal(err)
	}
	h.eventsFor(t, "bounce", "permanent/General", 21, h.now)
	th := h.checkHealth(t).Throttle
	if th == nil || !strings.Contains(th.Reason, `2.1% of the addresses "Bread week" went to don't exist (hard bounces), over the 2% early-warning limit`) {
		t.Fatalf("throttle = %+v", th)
	}
}

func TestNoThrottleUnder500(t *testing.T) {
	h := launchedHarness(t, 498)
	h.markSent(t, h.now)
	h.eventsFor(t, "bounce", "permanent/General", 20, h.now)
	h.eventsFor(t, "complaint", "", 2, h.now)
	if got := h.checkHealth(t); got.Throttle != nil || got.Pause != nil {
		t.Errorf("acted on 499 copies: %+v", got)
	}
}

func TestThrottleThresholdsConfigured(t *testing.T) {
	h := launchedHarness(t, 999)
	h.markSent(t, h.now)
	h.eventsFor(t, "complaint", "", 2, h.now)
	ctx := context.Background()
	var got Health
	err := pgx.BeginFunc(ctx, h.db, func(tx pgx.Tx) error {
		var err error
		got, err = CheckCampaign(ctx, tx, h.campaign.ID, h.now, Warning{ComplaintsPer10k: 25, HardBouncesPer10k: 400})
		return err
	})
	if err != nil || got.Throttle != nil {
		t.Errorf("throttled under a 0.25%% threshold: %+v %v", got, err)
	}
}

func TestThirdThrottlePauses(t *testing.T) {
	h := launchedHarness(t, 999)
	ctx := context.Background()
	h.markSent(t, h.now)
	// Two throttles in the last 30 days, one older.
	for _, ago := range []int{40, 20, 5} {
		at := h.now.AddDate(0, 0, -ago)
		if _, err := h.db.Exec(ctx, `
			INSERT INTO account_throttles (account_id, reason, created_at, until) VALUES ($1, 'earlier', $2, $3)`,
			h.acc, at, at.Add(ThrottleFor)); err != nil {
			t.Fatal(err)
		}
	}
	h.eventsFor(t, "complaint", "abuse", 2, h.now)
	got := h.checkHealth(t)
	if got.Throttle != nil || got.Pause == nil {
		t.Fatalf("third throttle: %+v", got)
	}
	ap := got.Pause
	if ap.AccountName != "Pat's Bakery" || ap.CampaignName != "Bread week" {
		t.Errorf("pause = %+v", ap)
	}
	paused, reason := h.paused(t)
	if !paused || !strings.Contains(reason, `It was paused automatically: "Bread week" went over the early-warning limits`) ||
		!strings.Contains(reason, "the third campaign to do so in 30 days") {
		t.Errorf("paused %v, reason %q", paused, reason)
	}
	if n := len(h.throttles(t)); n != 3 {
		t.Errorf("%d throttle rows, want the 3 earlier ones only", n)
	}
	var action string
	if err := h.db.QueryRow(ctx, `SELECT action FROM admin_actions WHERE account_id = $1`, h.acc).Scan(&action); err != nil || action != "auto_pause" {
		t.Errorf("logged %q, %v", action, err)
	}
	var autoPaused *time.Time
	_ = h.db.QueryRow(ctx, `SELECT auto_paused_at FROM campaigns WHERE id = $1`, h.campaign.ID).Scan(&autoPaused)
	if autoPaused == nil {
		t.Error("campaign not marked as having paused the account")
	}
}

// TestThirdThrottleSeesConcurrentThrottle checks the count of earlier
// throttles sees one another transaction commits while this one waits for
// the account's lock.
func TestThirdThrottleSeesConcurrentThrottle(t *testing.T) {
	h := launchedHarness(t, 999)
	ctx := context.Background()
	h.markSent(t, h.now)
	at := h.now.AddDate(0, 0, -20)
	if _, err := h.db.Exec(ctx, `
		INSERT INTO account_throttles (account_id, reason, created_at, until) VALUES ($1, 'earlier', $2, $3)`,
		h.acc, at, at.Add(ThrottleFor)); err != nil {
		t.Fatal(err)
	}
	h.eventsFor(t, "complaint", "abuse", 2, h.now)

	// The first transaction locks the account and adds a second throttle,
	// committing only once the health check is waiting on that lock.
	first, err := h.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Rollback(ctx) }()
	if _, err := first.Exec(ctx, `SELECT id FROM accounts WHERE id = $1 FOR UPDATE`, h.acc); err != nil {
		t.Fatal(err)
	}
	at = h.now.AddDate(0, 0, -1)
	if _, err := first.Exec(ctx, `
		INSERT INTO account_throttles (account_id, reason, created_at, until) VALUES ($1, 'concurrent', $2, $3)`,
		h.acc, at, at.Add(ThrottleFor)); err != nil {
		t.Fatal(err)
	}
	done := make(chan Health, 1)
	go func() {
		var out Health
		err := pgx.BeginFunc(ctx, h.db, func(tx pgx.Tx) error {
			var err error
			out, err = CheckCampaign(ctx, tx, h.campaign.ID, h.now, Warning{})
			return err
		})
		if err != nil {
			t.Error(err)
		}
		done <- out
	}()
	// Wait until the check is blocked on the account's lock.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting bool
		if err := h.db.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM pg_stat_activity
				WHERE wait_event_type = 'Lock' AND query LIKE '%FROM accounts a WHERE a.id = $1 FOR UPDATE OF a%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the health check never waited for the account's lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := first.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.Throttle != nil || got.Pause == nil {
		t.Fatalf("third throttle after a concurrent one: %+v", got)
	}
	if paused, _ := h.paused(t); !paused {
		t.Error("account not paused")
	}
}

func TestThrottledLimit(t *testing.T) {
	h := launchedHarness(t, 999)
	ctx := context.Background()
	l := Limits{PerSecond: 100, Daily: 10000, NewDaily: 1000}
	since := h.now.AddDate(0, -3, 0)
	at := func(now time.Time, own *int) SendLimit {
		t.Helper()
		got, err := l.ForAccount(ctx, h.db, h.acc, own, since, now)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	// Three clean months: the usual limit, with no reason.
	if got := at(h.now, nil); got.Daily != 10000 || got.Reason != nil || got.LoweredUntil != nil || got.Usual != 10000 {
		t.Fatalf("before: %+v", got)
	}
	h.markSent(t, h.now)
	h.eventsFor(t, "complaint", "abuse", 2, h.now)
	th := h.checkHealth(t).Throttle
	if th == nil {
		t.Fatal("not throttled")
	}
	got := at(h.now.Add(time.Hour), nil)
	if got.Daily != 1000 || got.LoweredUntil == nil || !got.LoweredUntil.Equal(th.Until) || got.Reason == nil ||
		!strings.Contains(*got.Reason, "early-warning limit") || !strings.Contains(*got.Reason, "1,000 a day until") {
		t.Fatalf("throttled: %+v %s", got, deref(got.Reason))
	}
	// The dispatcher uses the same limit.
	p := h.pipeline(l)
	if n, err := p.dailyLimit(ctx, h.db, h.acc, nil, since, h.now.Add(time.Hour)); err != nil || n != 1000 {
		t.Errorf("dispatcher limit %d, %v", n, err)
	}
	// A throttle wins over a staff-set limit, unless that's lower.
	own, low := 50000, 300
	if got := at(h.now.Add(time.Hour), &own); got.Daily != 1000 {
		t.Errorf("own 50,000: %d", got.Daily)
	}
	if got := at(h.now.Add(time.Hour), &low); got.Daily != 300 {
		t.Errorf("own 300: %d", got.Daily)
	}
	// Seven days later it lifts and the ramp starts over: the new-account
	// limit for a week, then doubling.
	lift := th.Until
	got = at(lift.Add(time.Hour), nil)
	if got.Daily != 1000 || got.LoweredUntil != nil || got.Reason == nil || !strings.Contains(*got.Reason, "Growing again") {
		t.Errorf("just lifted: %+v %s", got, deref(got.Reason))
	}
	if got := at(lift.Add(8*24*time.Hour), nil); got.Daily != 2000 {
		t.Errorf("a week after: %d", got.Daily)
	}
	if got := at(lift.Add(29*24*time.Hour), nil); got.Daily != 10000 || got.Reason != nil {
		t.Errorf("four weeks after: %+v", got)
	}
	// A second over-the-line campaign during the throttle starts the seven
	// days again.
	if _, err := h.db.Exec(ctx, `
		INSERT INTO account_throttles (account_id, reason, created_at, until) VALUES ($1, 'again', $2, $3)`,
		h.acc, h.now.AddDate(0, 0, 3), h.now.AddDate(0, 0, 10)); err != nil {
		t.Fatal(err)
	}
	if got := at(lift.Add(time.Hour), nil); got.Daily != 1000 || got.LoweredUntil == nil {
		t.Errorf("throttled again: %+v", got)
	}
	// Account reads the account's own row.
	if got, err := l.Account(ctx, h.db, h.acc, h.now.Add(time.Hour)); err != nil || got.Daily != 1000 {
		t.Errorf("Account: %+v %v", got, err)
	}
}

func TestNewAccountLimitReason(t *testing.T) {
	h := launchedHarness(t, 1)
	l := Limits{PerSecond: 100, Daily: 10000, NewDaily: 1000}
	got, err := l.ForAccount(context.Background(), h.db, h.acc, nil, h.now.AddDate(0, 0, -2), h.now)
	if err != nil || got.Daily != 1000 || got.Reason == nil || !strings.Contains(*got.Reason, "New accounts start at 1,000 a day") {
		t.Errorf("%+v %v %s", got, err, deref(got.Reason))
	}
	own := 25000
	got, _ = l.ForAccount(context.Background(), h.db, h.acc, &own, h.now.AddDate(0, 0, -2), h.now)
	if got.Daily != 25000 || got.Reason == nil || *got.Reason != "Set for this account by mailgoose." {
		t.Errorf("own: %+v", got)
	}
}

func TestThrottleNotices(t *testing.T) {
	fake := &mail.Fake{}
	quiet := slog.New(slog.DiscardHandler)
	until := time.Date(2026, 11, 26, 16, 5, 0, 0, time.UTC)
	th := Throttle{AccountID: "acc-1", AccountName: "Pat's Bakery", OwnerEmail: "pat@example.com", CampaignID: "c-1",
		CampaignName: "Bread week", Rates: Rates{Sent: 1000, Complaints: 2, HardBounces: 5}, Warning: DefaultWarning,
		Reason: "Lowered automatically.", Until: until}
	n := &Notices{Mail: fake, From: mail.Address{Name: "mailgoose", Email: "notices@mail.localhost"}, NewDaily: 1000, Log: quiet,
		Ops: &OpsAlert{Mail: fake, From: mail.Address{Email: "alerts@mail.localhost"}, To: "ops@example.com", Log: quiet}}
	n.Throttled(context.Background(), th)
	sent := fake.Sent()
	if len(sent) != 2 {
		t.Fatalf("%d emails", len(sent))
	}
	var owner, ops mail.Message
	for _, s := range sent {
		if s.Message.To.Email == "pat@example.com" {
			owner = s.Message
		} else {
			ops = s.Message
		}
	}
	for _, want := range []string{`"Bread week"`, "0.2% of them marked it as spam (2 people)", "0.5% of the addresses don't exist (5 hard bounces)",
		"lowered to 1,000", "Nov 26, 2026 at 16:05 UTC", "0.1% for spam complaints and 2% for hard bounces"} {
		if !strings.Contains(owner.Text, want) {
			t.Errorf("owner email lacks %q:\n%s", want, owner.Text)
		}
	}
	if owner.Kind != mail.KindSystem || owner.AccountID != "acc-1" || owner.From.Email != "notices@mail.localhost" {
		t.Errorf("owner email = %+v", owner)
	}
	if ops.To.Email != "ops@example.com" || !strings.Contains(ops.Subject, "Pat's Bakery throttled automatically (early warning)") ||
		!strings.HasPrefix(ops.Text, "THROTTLE (early warning)") || !strings.Contains(ops.Text, "mailgoose admin account acc-1") ||
		!strings.Contains(ops.Text, "2 complaints (0.2%)") {
		t.Errorf("ops alert = %+v", ops)
	}
	// No owner address and no ops address: nothing sent, nothing fails.
	fake.Reset()
	(&Notices{Mail: fake, NewDaily: 1000, Log: quiet, Ops: &OpsAlert{Mail: fake, Log: quiet}}).
		Throttled(context.Background(), Throttle{AccountID: "acc-2", Rates: Rates{Sent: 600}})
	if len(fake.Sent()) != 0 {
		t.Errorf("sent %d", len(fake.Sent()))
	}
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
