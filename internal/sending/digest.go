package sending

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The daily abuse digest (MG-67). Once a day, at DigestJob.Hour UTC, the
// ops address gets one email listing what happened to accounts in the last
// 24 hours and what still waits for a person:
//
//   - throttles, pauses (automatic or by an admin) and suspensions (the
//     same), from admin_actions;
//   - imports held for review, and sign-ups waiting for review, whenever
//     they were made;
//   - the platform's complaint and hard-bounce rates over 24 hours and 30
//     days.
//
// Each item names the account and the `mailgoose admin` command to act on
// it. Nothing goes on a quiet day: no items, and rates under the early
// warning thresholds (on enough copies to judge, as for one account).
//
// A row in ops_digests claims each UTC day, so the digest goes at most once
// a day across restarts and worker tasks. The claim is committed before the
// email goes: a crash in between loses that day's digest rather than
// sending it twice.

// DigestWindow is how far back the digest looks for throttles, pauses and
// suspensions, and for the short platform rates.
const DigestWindow = 24 * time.Hour

// DefaultDigestHour is the hour, UTC, the digest goes when none is set:
// 8 AM in US Eastern winter time.
const DefaultDigestHour = 13

// DigestItem is one thing in the digest, about one account.
type DigestItem struct {
	AccountID   string
	AccountName string
	// At is when it happened, or since when it has waited.
	At time.Time
	// What is the line about it: who did it, why, and how it stands.
	What string
	// Commands are the `mailgoose admin` commands to act on it.
	Commands []string
}

// Digest is a day's digest.
type Digest struct {
	// At is when it was built; it covers the DigestWindow before.
	At          time.Time
	Throttles   []DigestItem
	Pauses      []DigestItem
	Suspensions []DigestItem
	HeldImports []DigestItem
	Signups     []DigestItem
	// Day and Month are the platform's rates over DigestWindow and
	// HealthWindow.
	Day, Month Rates
	Warning    Warning
}

// Items is how many items the digest lists, rates aside.
func (d Digest) Items() int {
	return len(d.Throttles) + len(d.Pauses) + len(d.Suspensions) + len(d.HeldImports) + len(d.Signups)
}

// RatesHigh says whether the platform's rates over either window are over
// an early-warning threshold, on enough copies to judge.
func (d Digest) RatesHigh() bool {
	return d.Day.OverWarning(d.Warning) || d.Month.OverWarning(d.Warning)
}

// Empty says whether the digest has nothing to say, so isn't sent.
func (d Digest) Empty() bool { return d.Items() == 0 && !d.RatesHigh() }

// Subject is the digest email's subject, after "mailgoose: ".
func (d Digest) Subject() string {
	s := fmt.Sprintf("abuse digest for %s", d.At.UTC().Format(time.DateOnly))
	switch n := d.Items(); {
	case n == 1:
		s += " (1 item)"
	case n > 1:
		s += fmt.Sprintf(" (%d items)", n)
	}
	if d.RatesHigh() {
		s += ", platform rates over the early warning"
	}
	return s
}

// Text is the digest email's body.
func (d Digest) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Abuse digest for the 24 hours to %s.\n", d.At.UTC().Format("2006-01-02 15:04 UTC"))
	for _, s := range []struct {
		title string
		items []DigestItem
	}{
		{"Throttled (early warning) in the last 24 hours", d.Throttles},
		{"Paused in the last 24 hours", d.Pauses},
		{"Suspended in the last 24 hours", d.Suspensions},
		{"Imports held for review", d.HeldImports},
		{"Sign-ups waiting for review", d.Signups},
	} {
		if len(s.items) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n%s (%d)\n", s.title, len(s.items))
		for _, it := range s.items {
			fmt.Fprintf(&b, "\n- %s (account %s), %s: %s\n", it.AccountName, it.AccountID,
				it.At.UTC().Format("2006-01-02 15:04 UTC"), it.What)
			for _, c := range it.Commands {
				fmt.Fprintf(&b, "    %s\n", c)
			}
		}
	}
	b.WriteString("\nPlatform rates (campaign email)\n\n")
	fmt.Fprintf(&b, "- Last 24 hours: %s\n", rateLine(d.Day, d.Warning))
	fmt.Fprintf(&b, "- Last 30 days: %s\n", rateLine(d.Month, d.Warning))
	w := d.Warning.orDefault()
	fmt.Fprintf(&b, "\nEarly warning at %s complaints (and at least %d) or %s hard bounces, on %d or more copies.\n",
		percent(w.ComplaintsPer10k, 10000), MinComplaints, percent(w.HardBouncesPer10k, 10000), HealthMinRecipients)
	return b.String()
}

func rateLine(r Rates, w Warning) string {
	if r.Sent == 0 {
		return "nothing sent."
	}
	s := fmt.Sprintf("%s sent, %s hard bounces (%s), %s complaints (%s).", thousands(r.Sent),
		thousands(r.HardBounces), percent(r.HardBounces, r.Sent), thousands(r.Complaints), percent(r.Complaints, r.Sent))
	if r.OverWarning(w) {
		s += " OVER THE EARLY WARNING."
	}
	return s
}

// holdReasons say why an import is held, by imports.hold_reason.
var holdReasons = map[string]string{
	"large_first_import": "a large first import",
	"over_contact_limit": "over the plan's contact limit",
	"shared_list":        "many of its addresses are in other accounts' imports",
	"risky_addresses":    "too many role or disposable addresses",
	"invalid_addresses":  "too many invalid rows",
	"":                   "no reason recorded",
}

// digestQuerier is a pool or a transaction.
type digestQuerier interface {
	querier
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// BuildDigest builds the digest for the DigestWindow up to now.
func BuildDigest(ctx context.Context, q digestQuerier, now time.Time, w Warning) (Digest, error) {
	d := Digest{At: now, Warning: w}
	since := now.Add(-DigestWindow)

	rows, err := q.Query(ctx, `
		SELECT x.action, x.actor, x.account_id::text, coalesce(a.name, ''), x.reason, x.created_at,
			coalesce(a.sending_paused_at IS NOT NULL, false), coalesce(a.suspended_at IS NOT NULL, false),
			(SELECT max(t.until) FROM account_throttles t WHERE t.account_id = x.account_id)
		FROM admin_actions x LEFT JOIN accounts a ON a.id = x.account_id
		WHERE x.created_at > $1 AND x.created_at <= $2 AND x.account_id IS NOT NULL
			AND x.action IN ('throttle', 'auto_pause', 'pause', 'auto_suspend', 'suspend')
		ORDER BY x.created_at, x.id`, since, now)
	if err != nil {
		return d, err
	}
	for rows.Next() {
		var action, actor, id, name, reason string
		var at time.Time
		var paused, suspended bool
		var until *time.Time
		if err := rows.Scan(&action, &actor, &id, &name, &reason, &at, &paused, &suspended, &until); err != nil {
			rows.Close()
			return d, err
		}
		by := "by " + actor
		if actor == "automatic" {
			by = "automatically"
		}
		it := DigestItem{AccountID: id, AccountName: orDeleted(name), At: at}
		switch action {
		case "throttle":
			state := "The throttle has lifted."
			if until != nil && until.After(now) {
				state = "Throttled until " + until.UTC().Format("2006-01-02 15:04 UTC") + "."
			}
			it.What = fmt.Sprintf("throttled %s. %s %s", by, sentence(reason), state)
			it.Commands = []string{
				"mailgoose admin account " + id,
				"mailgoose admin pause " + id + ` --reason "..."`,
			}
			d.Throttles = append(d.Throttles, it)
		case "auto_pause", "pause":
			if paused {
				it.What = fmt.Sprintf("paused %s. %s Still paused.", by, sentence(reason))
				it.Commands = []string{
					"mailgoose admin account " + id,
					"mailgoose admin unpause " + id + ` --reason "..."`,
					"mailgoose admin suspend " + id + ` --reason "..."`,
				}
			} else {
				it.What = fmt.Sprintf("paused %s. %s The pause has been lifted.", by, sentence(reason))
				it.Commands = []string{"mailgoose admin account " + id}
			}
			d.Pauses = append(d.Pauses, it)
		default:
			if suspended {
				it.What = fmt.Sprintf("suspended %s. %s Still suspended.", by, sentence(reason))
				it.Commands = []string{
					"mailgoose admin account " + id,
					"mailgoose admin unsuspend " + id + ` --reason "..."`,
				}
			} else {
				it.What = fmt.Sprintf("suspended %s. %s It has been unsuspended.", by, sentence(reason))
				it.Commands = []string{"mailgoose admin account " + id}
			}
			d.Suspensions = append(d.Suspensions, it)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return d, err
	}

	rows, err = q.Query(ctx, `
		SELECT i.id::text, i.account_id::text, a.name, coalesce(i.file_name, ''), i.total_rows,
			coalesce(i.hold_reason, ''), i.created_at
		FROM imports i JOIN accounts a ON a.id = i.account_id
		WHERE i.review = 'held'
		ORDER BY i.created_at, i.id`)
	if err != nil {
		return d, err
	}
	for rows.Next() {
		var importID, id, name, file, hold string
		var total int
		var at time.Time
		if err := rows.Scan(&importID, &id, &name, &file, &total, &hold, &at); err != nil {
			rows.Close()
			return d, err
		}
		why, ok := holdReasons[hold]
		if !ok {
			why = hold
		}
		if file == "" {
			file = "no file name"
		}
		d.HeldImports = append(d.HeldImports, DigestItem{
			AccountID: id, AccountName: orDeleted(name), At: at,
			What: fmt.Sprintf("import %s (%s, %s rows) held: %s.", importID, file, thousands(total), why),
			Commands: []string{
				"mailgoose admin account " + id,
				"mailgoose admin approve-import " + importID + ` --reason "..."`,
			},
		})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return d, err
	}

	rows, err = q.Query(ctx, `
		SELECT a.id::text, a.name, a.review_flags, a.review_at,
			(SELECT u.email FROM users u WHERE u.account_id = a.id ORDER BY u.created_at, u.id LIMIT 1)
		FROM accounts a
		WHERE a.review = 'waiting'
		ORDER BY a.review_at, a.id`)
	if err != nil {
		return d, err
	}
	for rows.Next() {
		var id, name string
		var flags []string
		var at time.Time
		var owner *string
		if err := rows.Scan(&id, &name, &flags, &at, &owner); err != nil {
			rows.Close()
			return d, err
		}
		what := "waiting for review"
		if owner != nil {
			what += ", owner " + *owner
		}
		if len(flags) > 0 {
			what += ", flagged: " + strings.Join(flags, ", ") + "."
		} else {
			what += ", no flags."
		}
		d.Signups = append(d.Signups, DigestItem{
			AccountID: id, AccountName: orDeleted(name), At: at, What: what,
			Commands: []string{
				"mailgoose admin approve-signup " + id,
				"mailgoose admin reject-signup " + id + ` --reason "..."`,
			},
		})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return d, err
	}

	if d.Day, err = platformRates(ctx, q, since, now); err != nil {
		return d, err
	}
	d.Month, err = platformRates(ctx, q, now.Add(-HealthWindow), now)
	return d, err
}

// platformRates counts every account's campaign copies sent after since,
// and the recipients who hard bounced or complained, as for one account.
func platformRates(ctx context.Context, q querier, since, now time.Time) (Rates, error) {
	var r Rates
	err := q.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM campaign_recipients WHERE sent_at > $1 AND sent_at <= $2),
			(SELECT count(DISTINCT recipient_id) FROM send_events
				WHERE occurred_at > $1 AND occurred_at <= $2 AND kind = 'campaign' AND type = 'bounce' AND subtype LIKE 'permanent/%'),
			(SELECT count(DISTINCT recipient_id) FROM send_events
				WHERE occurred_at > $1 AND occurred_at <= $2 AND kind = 'campaign' AND type = 'complaint')`,
		since, now).Scan(&r.Sent, &r.HardBounces, &r.Complaints)
	return r, err
}

func orDeleted(name string) string {
	if name == "" {
		return "(deleted account)"
	}
	return name
}

// sentence ends s with a full stop.
func sentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasSuffix(s, ".") || strings.HasSuffix(s, "!") || strings.HasSuffix(s, "?") {
		return s
	}
	return s + "."
}

// DigestJob sends the daily abuse digest.
type DigestJob struct {
	DB *pgxpool.Pool
	// Alert sends it; with no To, nothing is built or sent.
	Alert *OpsAlert
	// Hour is when it goes, UTC, 0 to 23.
	Hour    int
	Warning Warning
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	Log *slog.Logger
}

// DigestEvery is how often the job looks whether the day's digest is due.
const DigestEvery = 5 * time.Minute

// Run sends the digest once a day until ctx ends, looking every interval.
func (j *DigestJob) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if _, err := j.RunOnce(ctx); err != nil && ctx.Err() == nil {
			j.log().ErrorContext(ctx, "send the abuse digest", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// RunOnce sends today's digest if it's due, not yet claimed and not empty.
// It reports whether it sent one.
func (j *DigestJob) RunOnce(ctx context.Context) (bool, error) {
	if j.Alert == nil || j.Alert.To == "" {
		return false, nil
	}
	now := time.Now()
	if j.Now != nil {
		now = j.Now()
	}
	now = now.UTC()
	if now.Hour() < j.Hour {
		return false, nil
	}
	day := now.Format(time.DateOnly)
	tx, err := j.DB.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	// Another task claiming the same day waits here until this one
	// commits, then finds the row and does nothing.
	var claimed string
	err = tx.QueryRow(ctx, `
		INSERT INTO ops_digests (day, checked_at, items, sent) VALUES ($1, $2, 0, false)
		ON CONFLICT (day) DO NOTHING RETURNING day::text`, day, now).Scan(&claimed)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	d, err := BuildDigest(ctx, tx, now, j.Warning)
	if err != nil {
		return false, err
	}
	send := !d.Empty()
	if _, err := tx.Exec(ctx, `UPDATE ops_digests SET items = $2, sent = $3 WHERE day = $1`, day, d.Items(), send); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	if !send {
		j.log().InfoContext(ctx, "abuse digest: a quiet day, nothing sent", "day", day)
		return false, nil
	}
	j.log().InfoContext(ctx, "sending the abuse digest", "day", day, "items", d.Items(), "rates_high", d.RatesHigh())
	j.Alert.Notify(ctx, d.Subject(), d.Text())
	return true, nil
}

func (j *DigestJob) log() *slog.Logger {
	if j.Log != nil {
		return j.Log
	}
	return slog.Default()
}
