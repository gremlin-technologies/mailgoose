package sending

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gremlin-technologies/mailgoose/internal/mail"
)

// Account health (CMP-6) and send limits (CMP-7).
//
// After a campaign has gone to HealthMinRecipients or more, its account is
// paused if more than 0.1% of them complained (and at least MinComplaints
// did) or more than 3% hard bounced. The check runs on every bounce and
// complaint SES reports, so a campaign that goes bad is stopped while it's
// still sending, and one whose complaints come in days later is still
// caught.
//
// The thresholds sit at AWS's own: it reviews the whole SES account, which
// every customer shares, at 0.1% complaints or 5% bounces. A customer over
// 0.1% is pulling the account towards that review, so it's stopped there
// (MG-79).
//
// The same check runs on the account's last 30 days (MG-88), so an account
// that sends many campaigns under HealthMinRecipients each, none of which
// is judged alone, is still paused once its copies over the 30 days reach
// HealthMinRecipients and are over either threshold. After an admin lifts a
// pause, only copies sent since then count, so the same evidence can't
// pause it again.
//
// An account's daily limit grows with clean history: the new-account
// limit for its first week, then double that each week (1,000, 2,000,
// 4,000, 8,000 with the defaults) up to the usual limit. An account isn't
// clean while its last 30 days' campaigns are over either threshold, or one
// of them paused it; it's then held at the new-account limit.
const (
	// HealthMinRecipients is the smallest campaign (copies sent) the
	// thresholds apply to.
	HealthMinRecipients = 500
	// MinComplaints is the fewest complaints a campaign's or account's
	// complaint rate is judged on. At 0.1%, one complaint on any campaign
	// under 1,000 copies would be over the line; one is chance, two is a
	// pattern. A new account's slice (MG-62) is judged without it.
	MinComplaints = 2
	// ComplaintsPer10k and HardBouncesPer10k are the thresholds: 0.1% and
	// 3%, in hundredths of a percent.
	ComplaintsPer10k  = 10
	HardBouncesPer10k = 300
	// HealthWindow is how far back an account's history counts for its
	// limit.
	HealthWindow = 30 * 24 * time.Hour
)

// A tester account (MG-86) sends at most a few hundred copies, under
// HealthMinRecipients, so it has its own, stricter pause: a campaign of
// TesterMinCopies or more copies with more than 2% hard bounces (and at
// least TesterMinHardBounces of them), or with TesterComplaints complaints
// at any size, pauses it. Two complaints on its largest possible campaign,
// 500 copies, are 0.4%, four times the usual limit.
const (
	TesterMinCopies         = 20
	TesterHardBouncesPer10k = 200
	TesterMinHardBounces    = 3
	TesterComplaints        = 2
)

// TesterUnhealthy says whether the rates are over a tester account's
// pause thresholds.
func (r Rates) TesterUnhealthy() bool {
	return r.Complaints >= TesterComplaints ||
		r.Sent >= TesterMinCopies && r.HardBounces >= TesterMinHardBounces && over(r.HardBounces, r.Sent, TesterHardBouncesPer10k)
}

// over says whether n of sent is over a threshold in hundredths of a
// percent.
func over(n, sent, per10k int) bool { return n*10000 > sent*per10k }

// complaintsOver says whether r's complaints are over a threshold in
// hundredths of a percent, and at least min of them.
func (r Rates) complaintsOver(per10k, min int) bool {
	return r.Complaints >= min && over(r.Complaints, r.Sent, per10k)
}

// LimitSchedule is an account's daily limit by age (CMP-7), before its own
// limit: newDaily in its first week and while it isn't clean, then doubling
// each week up to daily.
func LimitSchedule(newDaily, daily int, age time.Duration, clean bool) int {
	week := 7 * 24 * time.Hour
	if age < week || !clean {
		return newDaily
	}
	limit := newDaily
	for w := age / week; w > 0 && limit < daily; w-- {
		limit *= 2
	}
	return max(min(limit, daily), newDaily)
}

// querier is a pool or a transaction.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Rates are a campaign's or account's copies sent, and the recipients who
// hard bounced or complained.
type Rates struct {
	Sent        int `json:"sent"`
	HardBounces int `json:"hard_bounces"`
	Complaints  int `json:"complaints"`
}

// Unhealthy says whether the rates are over a threshold, on enough copies
// and complaints.
func (r Rates) Unhealthy() bool {
	return r.Sent >= HealthMinRecipients && r.overPauseFrom(MinComplaints)
}

// overPause says whether the rates are over a pause threshold, on any
// number of copies and complaints, as a slice is judged.
func (r Rates) overPause() bool { return r.overPauseFrom(1) }

func (r Rates) overPauseFrom(minComplaints int) bool {
	return r.complaintsOver(ComplaintsPer10k, minComplaints) || over(r.HardBounces, r.Sent, HardBouncesPer10k)
}

// accountClean says whether an account's last 30 days let its limit grow.
func accountClean(ctx context.Context, q querier, accountID string, now time.Time) (bool, error) {
	since := now.Add(-HealthWindow)
	r, err := accountRates(ctx, q, accountID, since, false)
	if err != nil {
		return false, err
	}
	var paused bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM campaigns WHERE account_id = $1 AND auto_paused_at > $2)`,
		accountID, since).Scan(&paused); err != nil {
		return false, err
	}
	return !paused && !r.Unhealthy(), nil
}

// accountRates counts an account's campaign copies sent after since, and
// the recipients who hard bounced or complained after since, leaving out
// SES mailbox simulator addresses (MG-86). With sentSince, a bounce or
// complaint counts only on a copy sent after since too, so late events on
// older copies aren't judged against newer ones.
func accountRates(ctx context.Context, q querier, accountID string, since time.Time, sentSince bool) (Rates, error) {
	var r Rates
	err := q.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM campaign_recipients WHERE account_id = $1 AND sent_at > $2 AND NOT simulated),
			(SELECT count(DISTINCT recipient_id) FROM send_events
				WHERE account_id = $1 AND occurred_at > $2 AND kind = 'campaign' AND NOT simulated
					AND type = 'bounce' AND subtype LIKE 'permanent/%'
					AND (NOT $3::boolean OR recipient_id IN (
						SELECT id FROM campaign_recipients WHERE account_id = $1 AND sent_at > $2))),
			(SELECT count(DISTINCT recipient_id) FROM send_events
				WHERE account_id = $1 AND occurred_at > $2 AND kind = 'campaign' AND NOT simulated AND type = 'complaint'
					AND (NOT $3::boolean OR recipient_id IN (
						SELECT id FROM campaign_recipients WHERE account_id = $1 AND sent_at > $2)))`,
		accountID, since, sentSince).Scan(&r.Sent, &r.HardBounces, &r.Complaints)
	return r, err
}

// dailyLimit is an account's daily limit: see Limits.ForAccount.
func (p *Pipeline) dailyLimit(ctx context.Context, q querier, accountID string, own *int, since, now time.Time) (int, error) {
	l, err := p.Limits.ForAccount(ctx, q, accountID, own, since, now)
	return l.Daily, err
}

// AutoPause is an account paused by a campaign's bounces or complaints, or
// by its own over the last 30 days (MG-88).
type AutoPause struct {
	AccountID   string
	AccountName string
	// CampaignID and CampaignName are the campaign that paused it; for an
	// account-level pause, the campaign whose bounce or complaint was the
	// last straw.
	CampaignID   string
	CampaignName string
	// Rates are the campaign's, or for an account-level pause the
	// account's since Since.
	Rates Rates
	// Account is set when the account's own rates paused it (MG-88), not
	// one campaign's.
	Account bool
	// Since is where an account-level pause's rates start: 30 days back, or
	// the account's last unpause by an admin or automatic pause for a
	// campaign, if later. Zero otherwise.
	Since time.Time
	// After is what Since is: "unpause" or "auto_pause", or "" for 30
	// days back.
	After string
	// Reason is what the account's owner is told.
	Reason string
	At     time.Time
	// Suspension is set when this pause suspended the account too: its
	// second automatic sending pause in 30 days (MG-65; the past-due pause
	// doesn't count, MG-104).
	Suspension *Suspension
}

// CampaignRates counts a campaign's copies sent, hard bounces and
// complaints, leaving out SES mailbox simulator addresses (MG-86).
func CampaignRates(ctx context.Context, q querier, campaignID string) (Rates, error) {
	var r Rates
	err := q.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM campaign_recipients WHERE campaign_id = $1 AND status = 'sent' AND NOT simulated),
			(SELECT count(DISTINCT recipient_id) FROM send_events
				WHERE campaign_id = $1 AND NOT simulated AND type = 'bounce' AND subtype LIKE 'permanent/%'),
			(SELECT count(DISTINCT recipient_id) FROM send_events WHERE campaign_id = $1 AND NOT simulated AND type = 'complaint')`,
		campaignID).Scan(&r.Sent, &r.HardBounces, &r.Complaints)
	return r, err
}

// Health is what a campaign's rates led to: a pause, a throttle (MG-61),
// or neither (both nil).
type Health struct {
	Pause    *AutoPause
	Throttle *Throttle
}

// CheckCampaign pauses the campaign's account if its rates are over a
// threshold (CMP-6), or throttles it if they're over an early-warning one
// (MG-61), inside tx. A campaign pauses its account once: after someone
// lifts the pause, more bounces on the same campaign don't pause it again.
// An account paused already (by an admin) keeps that pause and its reason;
// the campaign is still marked and the pause still reported. A campaign
// throttles its account once too, and may pause it later if it gets worse.
//
// When the campaign doesn't pause it, the account's own last 30 days are
// judged by the same thresholds (MG-88): see checkAccount.
func CheckCampaign(ctx context.Context, tx pgx.Tx, campaignID string, now time.Time, warn Warning) (Health, error) {
	h, err := checkCampaign(ctx, tx, campaignID, now, warn)
	if err != nil || h.Pause != nil {
		return h, err
	}
	ap, err := checkAccount(ctx, tx, campaignID, now)
	if err != nil {
		return Health{}, err
	}
	h.Pause = ap
	return h, nil
}

// checkCampaign is CheckCampaign's check of the campaign alone.
func checkCampaign(ctx context.Context, tx pgx.Tx, campaignID string, now time.Time, warn Warning) (Health, error) {
	r, err := CampaignRates(ctx, tx, campaignID)
	if err != nil {
		return Health{}, err
	}
	var isTester bool
	if err := tx.QueryRow(ctx, `
		SELECT a.tester_started_at IS NOT NULL FROM campaigns c JOIN accounts a ON a.id = c.account_id WHERE c.id = $1`,
		campaignID).Scan(&isTester); err != nil {
		return Health{}, err
	}
	switch {
	case isTester && r.TesterUnhealthy():
		return autoPause(ctx, tx, campaignID, r, now, testerPauseReason)
	case r.Unhealthy():
		return autoPause(ctx, tx, campaignID, r, now, pauseReason)
	case r.OverWarning(warn):
		return throttle(ctx, tx, campaignID, r, now, warn)
	}
	return Health{}, nil
}

// checkAccount pauses the campaign's account, inside tx, if the account's
// copies over the last HealthWindow are Unhealthy: HealthMinRecipients or
// more, over 0.1% complaints (at least MinComplaints) or 3% hard bounces
// (MG-88). That catches an account whose campaigns are each too small to
// be judged alone. It returns nil when it doesn't pause.
//
// An account paused already, for any reason, keeps its pause and reason,
// and nothing is logged, so a run of events can't stack up automatic
// pauses towards a suspension. After a pause is lifted by an admin, or
// after any automatic pause for a campaign, only copies sent since then,
// and bounces and complaints on them, count: the account needs new
// evidence to be paused again, not the 30 days that were looked at already.
// An automatic unpause (a plan payment going through) doesn't count, since
// nobody looked at the account's sending.
func checkAccount(ctx context.Context, tx pgx.Tx, campaignID string, now time.Time) (*AutoPause, error) {
	var accountID, campaignName string
	if err := tx.QueryRow(ctx, `SELECT account_id, name FROM campaigns WHERE id = $1`, campaignID).
		Scan(&accountID, &campaignName); err != nil {
		return nil, err
	}
	// Judged once without a lock, so most events, on healthy accounts,
	// take none. One that would pause locks the account row and judges
	// again, so two events at once can't both pause it and log two pauses:
	// the reads after the lock are statements of their own, so they see
	// what a transaction it waited for committed (see throttle).
	ap, err := judgeAccount(ctx, tx, accountID, now, false)
	if ap == nil || err != nil {
		return nil, err
	}
	if ap, err = judgeAccount(ctx, tx, accountID, now, true); ap == nil || err != nil {
		return nil, err
	}
	ap.CampaignID, ap.CampaignName = campaignID, campaignName
	ap.Reason = accountPauseReason(ap)
	return ap, pauseAccount(ctx, tx, ap)
}

// judgeAccount is checkAccount's judgement of the account: the pause to
// make, without its campaign or reason, or nil. lock locks the account row
// first.
func judgeAccount(ctx context.Context, tx pgx.Tx, accountID string, now time.Time, lock bool) (*AutoPause, error) {
	ap := &AutoPause{AccountID: accountID, Account: true, At: now, Since: now.Add(-HealthWindow)}
	q := `SELECT sending_paused_at IS NOT NULL FROM accounts WHERE id = $1`
	if lock {
		q += ` FOR UPDATE`
	}
	var paused bool
	if err := tx.QueryRow(ctx, q, accountID).Scan(&paused); err != nil || paused {
		return nil, err
	}
	var last *time.Time
	var after string
	err := tx.QueryRow(ctx, `
		SELECT created_at, action FROM admin_actions
		WHERE account_id = $1 AND created_at > $2 AND created_at <= $3
			AND (action = 'unpause' AND actor <> 'automatic' OR action = 'auto_pause' AND target_id IS NOT NULL)
		ORDER BY created_at DESC, id DESC LIMIT 1`, accountID, ap.Since, now).Scan(&last, &after)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if last != nil {
		ap.Since, ap.After = *last, after
	}
	if ap.Rates, err = accountRates(ctx, tx, accountID, ap.Since, last != nil); err != nil || !ap.Rates.Unhealthy() {
		return nil, err
	}
	return ap, nil
}

// autoPause marks the campaign as having paused its account, once, and
// pauses the account with its rates r, inside tx.
func autoPause(ctx context.Context, tx pgx.Tx, campaignID string, r Rates, now time.Time, why func(*AutoPause) string) (Health, error) {
	ap := &AutoPause{CampaignID: campaignID, Rates: r, At: now}
	err := tx.QueryRow(ctx, `
		UPDATE campaigns SET auto_paused_at = $2 WHERE id = $1 AND auto_paused_at IS NULL
		RETURNING account_id, name`, campaignID, now).Scan(&ap.AccountID, &ap.CampaignName)
	if errors.Is(err, pgx.ErrNoRows) {
		return Health{}, nil
	}
	if err != nil {
		return Health{}, err
	}
	ap.Reason = why(ap)
	return Health{Pause: ap}, pauseAccount(ctx, tx, ap)
}

// pauseAccount pauses ap's account for its campaign and logs it, inside tx.
func pauseAccount(ctx context.Context, tx pgx.Tx, ap *AutoPause) error {
	if err := tx.QueryRow(ctx, `
		UPDATE accounts SET
			sending_paused_reason = CASE WHEN sending_paused_at IS NULL THEN $3 ELSE sending_paused_reason END,
			sending_paused_at = coalesce(sending_paused_at, $2)
		WHERE id = $1 RETURNING name`, ap.AccountID, ap.At, ap.Reason).Scan(&ap.AccountName); err != nil {
		return err
	}
	var details any = ap.Rates
	if ap.Account {
		details = map[string]any{"sent": ap.Rates.Sent, "hard_bounces": ap.Rates.HardBounces,
			"complaints": ap.Rates.Complaints, "scope": "account", "since": ap.Since}
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO admin_actions (actor, action, account_id, target_id, reason, details, created_at)
		VALUES ('automatic', 'auto_pause', $1, $2, $3, $4, $5)`,
		ap.AccountID, ap.CampaignID, ap.Reason, details, ap.At)
	if err != nil {
		return err
	}
	ap.Suspension, err = suspendOnRepeat(ctx, tx, ap, ap.At)
	return err
}

func percent(n, of int) string {
	return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.2f", float64(n)*100/float64(of)), "0"), ".") + "%"
}

func pauseReason(ap *AutoPause) string {
	r := ap.Rates
	// A single complaint over the rate pauses a slice but not a full
	// campaign, so it's named only when the bounces didn't pause it.
	if r.complaintsOver(ComplaintsPer10k, MinComplaints) || !over(r.HardBounces, r.Sent, HardBouncesPer10k) {
		return fmt.Sprintf("It was paused automatically: %s of the people sent %q marked it as spam, over the %s limit.",
			percent(r.Complaints, r.Sent), ap.CampaignName, per10k(ComplaintsPer10k))
	}
	return fmt.Sprintf("It was paused automatically: %s of the addresses %q went to don't exist (hard bounces), over the %s limit.",
		percent(r.HardBounces, r.Sent), ap.CampaignName, per10k(HardBouncesPer10k))
}

// accountPauseReason is pauseReason for an account-level pause (MG-88).
func accountPauseReason(ap *AutoPause) string {
	r := ap.Rates
	window := "over the last 30 days"
	switch ap.After {
	case "unpause":
		window = "since sending was last unpaused (" + ap.Since.UTC().Format("Jan 2, 2006") + ")"
	case "auto_pause":
		window = "since it was last paused automatically (" + ap.Since.UTC().Format("Jan 2, 2006") + ")"
	}
	if r.complaintsOver(ComplaintsPer10k, MinComplaints) || !over(r.HardBounces, r.Sent, HardBouncesPer10k) {
		return fmt.Sprintf("It was paused automatically: %s, %s of the people sent this account's campaigns marked them as spam "+
			"(%s of %s), over the %s limit for an account.",
			window, percent(r.Complaints, r.Sent), thousands(r.Complaints), thousands(r.Sent), per10k(ComplaintsPer10k))
	}
	return fmt.Sprintf("It was paused automatically: %s, %s of the addresses this account's campaigns went to don't exist "+
		"(hard bounces, %s of %s), over the %s limit for an account.",
		window, percent(r.HardBounces, r.Sent), thousands(r.HardBounces), thousands(r.Sent), per10k(HardBouncesPer10k))
}

// testerPauseReason is pauseReason for a tester account's stricter limits.
func testerPauseReason(ap *AutoPause) string {
	r := ap.Rates
	if r.Complaints >= TesterComplaints {
		return fmt.Sprintf("It was paused automatically: %d of the people sent %q marked it as spam. "+
			"Tester accounts are paused at %d complaints on a campaign.", r.Complaints, ap.CampaignName, TesterComplaints)
	}
	return fmt.Sprintf("It was paused automatically: %s of the addresses %q went to don't exist (hard bounces), "+
		"over the %s limit for tester accounts.", percent(r.HardBounces, r.Sent), ap.CampaignName, per10k(TesterHardBouncesPer10k))
}

// OpsAlert tells mailgoose's operators about automatic pauses (CMP-6): an
// error in the log always, and an email when To is set. It also sends the
// owner's notice of a suspension (MG-65).
type OpsAlert struct {
	Mail mail.Sender
	From mail.Address
	// To is the ops address; empty sends no email.
	To string
	// Abuse is where a suspended account's owner appeals, by replying to
	// the notice; empty means abuse@ the From address's domain.
	Abuse string
	// AppURL is where people sign in, for the suspension notice.
	AppURL string
	Log    *slog.Logger
}

// AutoPaused reports a pause. It never fails: an alert that can't be sent
// is logged.
func (a *OpsAlert) AutoPaused(ctx context.Context, ap AutoPause) {
	log := a.Log
	if log == nil {
		log = slog.Default()
	}
	log.ErrorContext(ctx, "account paused automatically", "account_id", ap.AccountID, "campaign_id", ap.CampaignID,
		"account_level", ap.Account, "sent", ap.Rates.Sent, "hard_bounces", ap.Rates.HardBounces, "complaints", ap.Rates.Complaints)
	if a.To == "" || a.Mail == nil {
		if ap.Suspension != nil {
			a.Suspended(ctx, *ap.Suspension)
		}
		return
	}
	what := fmt.Sprintf("Campaign %q (%s)", ap.CampaignName, ap.CampaignID)
	if ap.Account {
		what = "The account's campaigns since " + ap.Since.UTC().Format("2006-01-02 15:04 UTC")
		switch ap.After {
		case "unpause":
			what += " (when it was last unpaused)"
		case "auto_pause":
			what += " (when it was last paused automatically)"
		default:
			what += " (30 days)"
		}
		what += fmt.Sprintf(", the latest event on %q (%s)", ap.CampaignName, ap.CampaignID)
	}
	text := fmt.Sprintf(`Sending is paused for %s (account %s).

%s: %d sent, %d hard bounces (%s), %d complaints (%s).

%s

Look into it with:
  mailgoose admin account %s
Lift the pause with:
  mailgoose admin unpause %s --reason "..."
`, ap.AccountName, ap.AccountID, what, ap.Rates.Sent,
		ap.Rates.HardBounces, percent(ap.Rates.HardBounces, ap.Rates.Sent),
		ap.Rates.Complaints, percent(ap.Rates.Complaints, ap.Rates.Sent), ap.Reason, ap.AccountID, ap.AccountID)
	_, err := a.Mail.Send(context.WithoutCancel(ctx), mail.Message{
		From:    a.From,
		To:      mail.Address{Email: a.To},
		Subject: "mailgoose: " + ap.AccountName + " paused automatically",
		Text:    text,
		HTML:    "<!doctype html><html><body><pre>" + html.EscapeString(text) + "</pre></body></html>",
		Kind:    mail.KindSystem,
	})
	if err != nil {
		log.ErrorContext(ctx, "send the pause alert", "err", err, "account_id", ap.AccountID)
	}
	if ap.Suspension != nil {
		a.Suspended(ctx, *ap.Suspension)
	}
}

// Notify emails the operators a note about something to sort out by hand.
// The caller logs it; Notify only sends, and logs a failure to send.
func (a *OpsAlert) Notify(ctx context.Context, subject, text string) {
	if a.To == "" || a.Mail == nil {
		return
	}
	_, err := a.Mail.Send(context.WithoutCancel(ctx), mail.Message{
		From:    a.From,
		To:      mail.Address{Email: a.To},
		Subject: "mailgoose: " + subject,
		Text:    text,
		HTML:    "<!doctype html><html><body><pre>" + html.EscapeString(text) + "</pre></body></html>",
		Kind:    mail.KindSystem,
	})
	if err != nil {
		log := a.Log
		if log == nil {
			log = slog.Default()
		}
		log.ErrorContext(ctx, "send an ops alert", "err", err, "subject", subject)
	}
}
