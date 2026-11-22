package sending

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/mail"
)

// digestJob is a digest job for h's database, at 13:00 UTC, with its clock
// at *now.
func (h *harness) digestJob(now *time.Time) *DigestJob {
	return &DigestJob{
		DB:    h.db,
		Alert: &OpsAlert{Mail: &mail.Guard{Sender: h.fake, System: "mail.example"}, From: mail.Address{Email: "alerts@mail.example"}, To: "ops@example.com", Log: slog.New(slog.DiscardHandler)},
		Hour:  13,
		Now:   func() time.Time { return *now },
		Log:   slog.New(slog.DiscardHandler),
	}
}

// account adds an account named name, with an owner at email.
func (h *harness) digestAccount(t *testing.T, name, email string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := h.db.QueryRow(ctx, `INSERT INTO accounts (name) VALUES ($1) RETURNING id`, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(ctx, `INSERT INTO users (account_id, email) VALUES ($1, $2)`, id, email); err != nil {
		t.Fatal(err)
	}
	return id
}

// logAction adds an admin_actions row.
func (h *harness) logAction(t *testing.T, actor, action, accountID, reason string, at time.Time) {
	t.Helper()
	if _, err := h.db.Exec(context.Background(), `
		INSERT INTO admin_actions (actor, action, account_id, reason, details, created_at)
		VALUES ($1, $2, $3, $4, '{}', $5)`, actor, action, accountID, reason, at); err != nil {
		t.Fatal(err)
	}
}

func TestDigestContents(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 19, 13, 0, 0, 0, time.UTC)

	throttled := h.digestAccount(t, "Loud Lists", "loud@example.com")
	h.logAction(t, "automatic", "throttle", throttled, "Too many hard bounces", now.Add(-3*time.Hour))
	if _, err := h.db.Exec(ctx, `
		INSERT INTO account_throttles (account_id, reason, created_at, until) VALUES ($1, 'x', $2, $3)`,
		throttled, now.Add(-3*time.Hour), now.Add(7*24*time.Hour-3*time.Hour)); err != nil {
		t.Fatal(err)
	}

	paused := h.digestAccount(t, "Spam Barn", "barn@example.com")
	h.logAction(t, "automatic", "auto_pause", paused, "It was paused automatically: 0.5% marked it as spam.", now.Add(-2*time.Hour))
	if _, err := h.db.Exec(ctx, `UPDATE accounts SET sending_paused_at = $2, sending_paused_reason = 'x' WHERE id = $1`,
		paused, now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// Paused and lifted again within the day.
	lifted := h.digestAccount(t, "Mended Co", "mended@example.com")
	h.logAction(t, "admin@mailgoose.example", "pause", lifted, "Checking a complaint", now.Add(-20*time.Hour))

	suspended := h.digestAccount(t, "Phish Ltd", "phish@example.com")
	h.logAction(t, "admin@mailgoose.example", "suspend", suspended, "Phishing", now.Add(-time.Hour))
	if _, err := h.db.Exec(ctx, `
		UPDATE accounts SET suspended_at = $2, suspended_reason = 'Phishing', suspended_by = 'admin@mailgoose.example' WHERE id = $1`,
		suspended, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	// Older than a day: not listed.
	h.logAction(t, "automatic", "auto_pause", paused, "An old pause", now.Add(-25*time.Hour))

	var imp string
	if err := h.db.QueryRow(ctx, `
		INSERT INTO imports (account_id, file_name, permission_confirmed, email_column, csv, total_rows, review, hold_reason, status, created_at)
		VALUES ($1, 'bought.csv', true, 'email', 'email', 12000, 'held', 'shared_list', 'done', $2) RETURNING id`,
		h.acc, now.Add(-30*time.Hour)).Scan(&imp); err != nil {
		t.Fatal(err)
	}

	waiting := h.digestAccount(t, "New Shop", "new@mailinator.example")
	if _, err := h.db.Exec(ctx, `
		UPDATE accounts SET review = 'waiting', review_at = $2, review_flags = '{disposable_domain,ip_burst}' WHERE id = $1`,
		waiting, now.Add(-50*time.Hour)); err != nil {
		t.Fatal(err)
	}

	sent, err := h.digestJob(&now).RunOnce(ctx)
	if err != nil || !sent {
		t.Fatalf("sent %v, %v", sent, err)
	}
	if len(h.fake.Sent()) != 1 {
		t.Fatalf("%d emails", len(h.fake.Sent()))
	}
	last, _ := h.fake.Last()
	m := last.Message
	if m.To.Email != "ops@example.com" || m.Kind != mail.KindSystem ||
		m.Subject != "mailgoose: abuse digest for 2026-11-19 (6 items)" {
		t.Fatalf("message = %q to %q, %v", m.Subject, m.To.Email, m.Kind)
	}
	for _, want := range []string{
		"Abuse digest for the 24 hours to 2026-11-19 13:00 UTC.",
		"Throttled (early warning) in the last 24 hours (1)",
		"- Loud Lists (account " + throttled + "), 2026-11-19 10:00 UTC: throttled automatically. Too many hard bounces. Throttled until 2026-11-26 10:00 UTC.",
		"    mailgoose admin pause " + throttled + ` --reason "..."`,
		"Paused in the last 24 hours (2)",
		"- Mended Co (account " + lifted + "), 2026-11-18 17:00 UTC: paused by admin@mailgoose.example. Checking a complaint. The pause has been lifted.",
		"- Spam Barn (account " + paused + "), 2026-11-19 11:00 UTC: paused automatically. It was paused automatically: 0.5% marked it as spam. Still paused.",
		"    mailgoose admin unpause " + paused + ` --reason "..."`,
		"Suspended in the last 24 hours (1)",
		"- Phish Ltd (account " + suspended + "), 2026-11-19 12:00 UTC: suspended by admin@mailgoose.example. Phishing. Still suspended.",
		"    mailgoose admin unsuspend " + suspended + ` --reason "..."`,
		"Imports held for review (1)",
		"- Pat's Bakery (account " + h.acc + "), 2026-11-18 07:00 UTC: import " + imp + " (bought.csv, 12,000 rows) held: many of its addresses are in other accounts' imports.",
		"    mailgoose admin approve-import " + imp + ` --reason "..."`,
		"Sign-ups waiting for review (1)",
		"- New Shop (account " + waiting + "), 2026-11-17 11:00 UTC: waiting for review, owner new@mailinator.example, flagged: disposable_domain, ip_burst.",
		"    mailgoose admin approve-signup " + waiting,
		"    mailgoose admin reject-signup " + waiting + ` --reason "..."`,
		"- Last 24 hours: nothing sent.",
		"- Last 30 days: nothing sent.",
	} {
		if !strings.Contains(m.Text, want) {
			t.Errorf("missing %q in:\n%s", want, m.Text)
		}
	}
	if strings.Contains(m.Text, "Accounts over the early warning") {
		t.Errorf("listed accounts over the early warning when none are:\n%s", m.Text)
	}
	if strings.Contains(m.Text, "An old pause") {
		t.Errorf("listed a pause older than a day:\n%s", m.Text)
	}
	// The pause from 20 hours ago comes before the one from 2 hours ago.
	if strings.Index(m.Text, "Mended Co") > strings.Index(m.Text, "Spam Barn") {
		t.Errorf("not oldest first:\n%s", m.Text)
	}
	var items int
	var wasSent bool
	if err := h.db.QueryRow(ctx, `SELECT items, sent FROM ops_digests WHERE day = '2026-11-19'`).Scan(&items, &wasSent); err != nil ||
		items != 6 || !wasSent {
		t.Errorf("ops_digests = %d, %v, %v", items, wasSent, err)
	}
}

func TestDigestRates(t *testing.T) {
	h := launchedHarness(t, 999)
	ctx := context.Background()
	h.markSent(t, h.now.Add(-time.Hour))
	now := time.Date(2026, 11, 18, 13, 0, 0, 0, time.UTC) // h.now is 12:00

	// One complaint in 1,000 is over 0.05%, but the early warning needs at
	// least two: a quiet day.
	h.eventsFor(t, "complaint", "", 1, h.now.Add(-30*time.Minute))
	tx, err := h.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	d, err := BuildDigest(ctx, tx, now, Warning{})
	_ = tx.Rollback(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if d.Day != (Rates{Sent: 1000, Complaints: 1}) || d.Month != d.Day || d.AccountsOver != 0 || !d.Empty() {
		t.Fatalf("digest = %+v, %+v, empty %v", d.Day, d.Month, d.Empty())
	}

	// Two are over it: the rates alone send the digest.
	if _, err := h.db.Exec(ctx, `DELETE FROM send_events`); err != nil {
		t.Fatal(err)
	}
	h.eventsFor(t, "complaint", "", 2, h.now.Add(-30*time.Minute))
	h.eventsFor(t, "bounce", "permanent/general", 3, h.now.Add(-30*time.Minute))
	sent, err := h.digestJob(&now).RunOnce(ctx)
	if err != nil || !sent {
		t.Fatalf("sent %v, %v", sent, err)
	}
	last, _ := h.fake.Last()
	m := last.Message
	// The one account that sent is over the early warning too (MG-82).
	if m.Subject != "mailgoose: abuse digest for 2026-11-18, 1 account over the early warning, platform rates over the early warning" {
		t.Errorf("subject %q", m.Subject)
	}
	for _, want := range []string{
		"- Last 24 hours: 1,000 sent, 3 hard bounces (0.3%), 2 complaints (0.2%). OVER THE EARLY WARNING.",
		"- Last 30 days: 1,000 sent, 3 hard bounces (0.3%), 2 complaints (0.2%). OVER THE EARLY WARNING.",
		"Early warning at 0.05% complaints (and at least 2) or 2% hard bounces, on 500 or more copies.",
	} {
		if !strings.Contains(m.Text, want) {
			t.Errorf("missing %q in:\n%s", want, m.Text)
		}
	}
	if strings.Contains(m.Text, "in the last 24 hours (") || strings.Contains(m.Text, "for review (") {
		t.Errorf("listed an empty section:\n%s", m.Text)
	}

	// A day later the copies are outside the 24 hours but still in the 30
	// days.
	next := now.Add(24 * time.Hour)
	tx, err = h.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	d, err = BuildDigest(ctx, tx, next, Warning{})
	_ = tx.Rollback(ctx)
	if err != nil || d.Day != (Rates{}) || d.Month.Sent != 1000 || !d.RatesHigh() {
		t.Errorf("next day = %+v, %+v, %v", d.Day, d.Month, err)
	}
	// A configured threshold is used.
	tx, err = h.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	d, err = BuildDigest(ctx, tx, now, Warning{ComplaintsPer10k: 30, HardBouncesPer10k: 400})
	_ = tx.Rollback(ctx)
	if err != nil || d.RatesHigh() || d.AccountsOver != 0 || !d.Empty() {
		t.Errorf("with higher thresholds: high %v, accounts over %d, %v", d.RatesHigh(), d.AccountsOver, err)
	}
}

func TestDigestQuietDay(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 19, 13, 5, 0, 0, time.UTC)
	j := h.digestJob(&now)
	sent, err := j.RunOnce(ctx)
	if err != nil || sent || len(h.fake.Sent()) != 0 {
		t.Fatalf("sent %v (%d emails), %v", sent, len(h.fake.Sent()), err)
	}
	// The day is checked once: something later that day waits for
	// tomorrow's digest.
	h.logAction(t, "automatic", "auto_pause", h.acc, "Paused", now.Add(time.Minute))
	now = now.Add(time.Hour)
	if sent, err := j.RunOnce(ctx); err != nil || sent || len(h.fake.Sent()) != 0 {
		t.Fatalf("later: sent %v, %v", sent, err)
	}
	var items int
	var wasSent bool
	if err := h.db.QueryRow(ctx, `SELECT items, sent FROM ops_digests WHERE day = '2026-11-19'`).Scan(&items, &wasSent); err != nil ||
		items != 0 || wasSent {
		t.Errorf("ops_digests = %d, %v, %v", items, wasSent, err)
	}
	now = now.Add(23 * time.Hour)
	if sent, err := j.RunOnce(ctx); err != nil || !sent || len(h.fake.Sent()) != 1 {
		t.Fatalf("next day: sent %v, %v", sent, err)
	}
}

func TestDigestHour(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.logAction(t, "automatic", "auto_pause", h.acc, "Paused", time.Date(2026, 11, 19, 1, 0, 0, 0, time.UTC))
	now := time.Date(2026, 11, 19, 12, 59, 59, 0, time.UTC)
	j := h.digestJob(&now)
	if sent, err := j.RunOnce(ctx); err != nil || sent {
		t.Fatalf("before the hour: sent %v, %v", sent, err)
	}
	var n int
	if err := h.db.QueryRow(ctx, `SELECT count(*) FROM ops_digests`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("claimed %d before the hour, %v", n, err)
	}
	// The hour is UTC, whatever the clock's zone.
	now = time.Date(2026, 11, 19, 8, 0, 0, 0, time.FixedZone("EST", -5*60*60))
	if sent, err := j.RunOnce(ctx); err != nil || !sent || len(h.fake.Sent()) != 1 {
		t.Fatalf("at the hour: sent %v, %v", sent, err)
	}
	// Later the same day: nothing more.
	now = time.Date(2026, 11, 19, 23, 59, 0, 0, time.UTC)
	if sent, err := j.RunOnce(ctx); err != nil || sent || len(h.fake.Sent()) != 1 {
		t.Fatalf("later: sent %v, %v", sent, err)
	}
	// A worker started late in the day still sends that day's.
	now = time.Date(2026, 11, 20, 22, 0, 0, 0, time.UTC)
	h.logAction(t, "automatic", "auto_pause", h.acc, "Paused again", now.Add(-time.Hour))
	if sent, err := j.RunOnce(ctx); err != nil || !sent || len(h.fake.Sent()) != 2 {
		t.Fatalf("late start: sent %v, %v", sent, err)
	}
	// Hour 0 sends just after midnight.
	j.Hour = 0
	now = time.Date(2026, 11, 21, 0, 1, 0, 0, time.UTC)
	h.logAction(t, "automatic", "auto_pause", h.acc, "Paused a third time", now.Add(-time.Hour))
	if sent, err := j.RunOnce(ctx); err != nil || !sent {
		t.Fatalf("hour 0: sent %v, %v", sent, err)
	}
}

func TestDigestOnceAcrossTasks(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 19, 14, 0, 0, 0, time.UTC)
	h.logAction(t, "automatic", "auto_pause", h.acc, "Paused", now.Add(-time.Hour))
	// Five worker tasks, each with its own job, look at once.
	var wg sync.WaitGroup
	sends := make(chan bool, 5)
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sent, err := h.digestJob(&now).RunOnce(ctx)
			if err != nil {
				t.Error(err)
			}
			sends <- sent
		}()
	}
	wg.Wait()
	close(sends)
	n := 0
	for s := range sends {
		if s {
			n++
		}
	}
	if n != 1 || len(h.fake.Sent()) != 1 {
		t.Fatalf("%d sent, %d emails", n, len(h.fake.Sent()))
	}
	// A restarted task finds the day claimed.
	if sent, err := h.digestJob(&now).RunOnce(ctx); err != nil || sent || len(h.fake.Sent()) != 1 {
		t.Fatalf("after a restart: sent %v, %v", sent, err)
	}
}

func TestDigestNeedsAnAddress(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 19, 14, 0, 0, 0, time.UTC)
	h.logAction(t, "automatic", "auto_pause", h.acc, "Paused", now.Add(-time.Hour))
	j := h.digestJob(&now)
	j.Alert.To = ""
	if sent, err := j.RunOnce(ctx); err != nil || sent || len(h.fake.Sent()) != 0 {
		t.Fatalf("sent %v, %v", sent, err)
	}
	var n int
	if err := h.db.QueryRow(ctx, `SELECT count(*) FROM ops_digests`).Scan(&n); err != nil || n != 0 {
		t.Errorf("claimed the day with no address: %d, %v", n, err)
	}
}

func TestDigestRun(t *testing.T) {
	h := newHarness(t)
	now := time.Date(2026, 11, 19, 14, 0, 0, 0, time.UTC)
	h.logAction(t, "automatic", "auto_pause", h.acc, "Paused", now.Add(-time.Hour))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.digestJob(&now).Run(ctx, time.Millisecond); close(done) }()
	deadline := time.Now().Add(10 * time.Second)
	for len(h.fake.Sent()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
	if len(h.fake.Sent()) != 1 {
		t.Fatalf("%d emails", len(h.fake.Sent()))
	}
}

func TestDigestListsMailchimpImports(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 19, 13, 0, 0, 0, time.UTC)
	mcImport := func(review, hold string, total int, file, signals string, confirmed time.Time) string {
		t.Helper()
		var id string
		if err := h.db.QueryRow(ctx, `
			INSERT INTO imports (account_id, file_name, permission_confirmed, email_column, csv, total_rows, review, hold_reason, status, created_at)
			VALUES ($1, $2, true, 'email', 'email', $3, $4, nullif($5, ''), 'done', $6) RETURNING id`,
			h.acc, file, total, review, hold, confirmed).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if _, err := h.db.Exec(ctx, `
			INSERT INTO mailchimp_bundles (account_id, file_name, bytes, expires_at, confirmed_at, import_id, signals)
			VALUES ($1, $2, 100, $3::timestamptz + interval '7 days', $3, $4, $5)`, h.acc, file, confirmed, id, signals); err != nil {
			t.Fatal(err)
		}
		return id
	}
	held := mcImport("held", "large_first_import", 3000, "export.zip", `{"source": "account_export",
		"audiences": [{"name": "Newsletter", "members": 3000, "by_status": {"subscribed": 2500, "unsubscribed": 300, "cleaned": 150, "pending": 50}}],
		"with_optin_time": 2000, "newest_optin": "2026-11-01T10:00:00Z"}`, now.Add(-2*time.Hour))
	fetched := mcImport("none", "", 40, "mailchimp-account.zip", `{"source": "connected_account",
		"audiences": [{"name": "Wholesale", "members": 40, "by_status": {"subscribed": 40}}],
		"with_optin_time": 40, "newest_optin": "2026-10-12T08:00:00Z"}`, now.Add(-5*time.Hour))
	old := mcImport("none", "", 10, "old.zip", `{"source": "audience_csv", "audiences": []}`, now.Add(-30*time.Hour))
	// An upload with no contacts makes no import, and isn't listed.
	if _, err := h.db.Exec(ctx, `
		INSERT INTO mailchimp_bundles (account_id, file_name, bytes, expires_at, confirmed_at)
		VALUES ($1, 'templates.zip', 100, $2, $3)`, h.acc, now.Add(7*24*time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}

	d, err := BuildDigest(ctx, h.db, now, Warning{})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.MailchimpImports) != 2 || len(d.HeldImports) != 1 || d.Items() != 3 {
		t.Fatalf("mailchimp %d, held %d, items %d", len(d.MailchimpImports), len(d.HeldImports), d.Items())
	}
	text := d.Text()
	for _, want := range []string{
		"Mailchimp imports in the last 24 hours (2)",
		"- Pat's Bakery (account " + h.acc + "), 2026-11-19 08:00 UTC: import " + fetched + " (40 rows), not held.\n" +
			"  From a connected Mailchimp account: Wholesale (40).\n" +
			"  Members: 40 subscribed.\n" +
			"  Cleaned 0.0%, unsubscribed 0.0%; 100.0% of subscribed have a Mailchimp opt-in time, the newest 2026-10-12.\n" +
			"    mailgoose admin account " + h.acc + "\n",
		"- Pat's Bakery (account " + h.acc + "), 2026-11-19 11:00 UTC: import " + held + " (3,000 rows) held: a large first import.\n" +
			"  From file export.zip: Newsletter (3,000).\n" +
			"  Members: 2,500 subscribed, 300 unsubscribed, 150 cleaned, 50 pending.\n" +
			"  Cleaned 5.0%, unsubscribed 10.0%; 80.0% of subscribed have a Mailchimp opt-in time, the newest 2026-11-01.\n" +
			"    mailgoose admin account " + h.acc + "\n" +
			"    mailgoose admin approve-import " + held + ` --reason "..."`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, old) || strings.Contains(text, "templates.zip") {
		t.Errorf("listed an old import or one with no contacts:\n%s", text)
	}
	// The digest goes on a day with only a Mailchimp import that isn't held.
	if _, err := h.db.Exec(ctx, `DELETE FROM imports WHERE id = $1`, held); err != nil {
		t.Fatal(err)
	}
	if d, err := BuildDigest(ctx, h.db, now, Warning{}); err != nil || d.Empty() || d.Items() != 1 {
		t.Errorf("one unheld import: items %d, empty %v, %v", d.Items(), d.Empty(), err)
	}
}
