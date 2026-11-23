package sending

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gremlin-technologies/mailgoose/internal/mail"
	"github.com/gremlin-technologies/mailgoose/internal/tester"
)

// Early-warning throttles (MG-61).
//
// A campaign of HealthMinRecipients or more whose complaints or hard
// bounces pass the early-warning thresholds (0.05% and 2% by default, under
// the 0.1% and 3% that pause; complaints counted from MinComplaints)
// throttles its account: its daily limit drops
// to the new-account limit for ThrottleFor, whatever its ramp had reached,
// and the owner is told. Each such campaign throttles once, and another
// one meanwhile starts the seven days again, so the limit lifts after seven
// days with none. The ramp then starts over from the new-account limit.
// The third throttle within ThrottleWindow pauses the account instead.
const (
	// ThrottleFor is how long a throttle lasts.
	ThrottleFor = 7 * 24 * time.Hour
	// ThrottleWindow is how far back throttles count towards a pause.
	ThrottleWindow = 30 * 24 * time.Hour
	// ThrottlesBeforePause is how many throttles in ThrottleWindow an
	// account may have; one more pauses it.
	ThrottlesBeforePause = 2
)

// Warning is the early-warning thresholds, in hundredths of a percent.
type Warning struct {
	ComplaintsPer10k  int
	HardBouncesPer10k int
}

// DefaultWarning is 0.05% complaints and 2% hard bounces.
var DefaultWarning = Warning{ComplaintsPer10k: 5, HardBouncesPer10k: 200}

// orDefault is w, or DefaultWarning for an unset one.
func (w Warning) orDefault() Warning {
	if w.ComplaintsPer10k <= 0 || w.HardBouncesPer10k <= 0 {
		return DefaultWarning
	}
	return w
}

// OverWarning says whether the rates are over an early-warning threshold,
// on enough copies and complaints.
func (r Rates) OverWarning(w Warning) bool {
	return r.Sent >= HealthMinRecipients && r.overWarningFrom(w, MinComplaints)
}

// overWarning says whether the rates are over an early-warning threshold,
// on any number of copies and complaints, as a slice is judged.
func (r Rates) overWarning(w Warning) bool { return r.overWarningFrom(w, 1) }

func (r Rates) overWarningFrom(w Warning, minComplaints int) bool {
	w = w.orDefault()
	return r.complaintsOver(w.ComplaintsPer10k, minComplaints) || over(r.HardBounces, r.Sent, w.HardBouncesPer10k)
}

// Throttle is an account throttled by a campaign's bounces or complaints.
type Throttle struct {
	AccountID   string
	AccountName string
	// OwnerEmail is who's told; empty if the account has no user.
	OwnerEmail   string
	CampaignID   string
	CampaignName string
	Rates        Rates
	Warning      Warning
	// Reason is what the account's owner is told.
	Reason string
	At     time.Time
	Until  time.Time
}

// per10k shows hundredths of a percent as a percentage, such as "0.1%".
func per10k(n int) string { return percent(n, 10000) }

func throttleReason(t *Throttle) string {
	r, w := t.Rates, t.Warning
	// As in pauseReason: a single complaint is named only when the bounces
	// weren't over.
	if r.complaintsOver(w.ComplaintsPer10k, MinComplaints) || !over(r.HardBounces, r.Sent, w.HardBouncesPer10k) {
		return fmt.Sprintf("Lowered automatically: %s of the people sent %q marked it as spam, over the %s early-warning limit.",
			percent(r.Complaints, r.Sent), t.CampaignName, per10k(w.ComplaintsPer10k))
	}
	return fmt.Sprintf("Lowered automatically: %s of the addresses %q went to don't exist (hard bounces), over the %s early-warning limit.",
		percent(r.HardBounces, r.Sent), t.CampaignName, per10k(w.HardBouncesPer10k))
}

// throttle throttles the campaign's account, or pauses it on its third
// throttle in ThrottleWindow, inside tx. A campaign that throttled or paused
// its account already does nothing.
func throttle(ctx context.Context, tx pgx.Tx, campaignID string, r Rates, now time.Time, warn Warning) (Health, error) {
	t := &Throttle{CampaignID: campaignID, Rates: r, Warning: warn.orDefault(), At: now, Until: now.Add(ThrottleFor)}
	err := tx.QueryRow(ctx, `
		UPDATE campaigns SET throttled_at = $2 WHERE id = $1 AND throttled_at IS NULL AND auto_paused_at IS NULL
		RETURNING account_id, name`, campaignID, now).Scan(&t.AccountID, &t.CampaignName)
	if errors.Is(err, pgx.ErrNoRows) {
		return Health{}, nil
	}
	if err != nil {
		return Health{}, err
	}
	// The account row is locked so two campaigns' events counting its
	// throttles at once can't both miss the pause. The count is a statement
	// of its own, after the lock: under READ COMMITTED a statement's snapshot
	// is taken before it waits for a lock, so a count in the locking
	// statement wouldn't see a throttle committed while it waited.
	if err := tx.QueryRow(ctx, `
		SELECT a.name, coalesce((SELECT u.email FROM users u WHERE u.account_id = a.id ORDER BY u.created_at LIMIT 1), '')
		FROM accounts a WHERE a.id = $1 FOR UPDATE OF a`, t.AccountID).
		Scan(&t.AccountName, &t.OwnerEmail); err != nil {
		return Health{}, err
	}
	var earlier int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM account_throttles WHERE account_id = $1 AND created_at > $2`,
		t.AccountID, now.Add(-ThrottleWindow)).Scan(&earlier); err != nil {
		return Health{}, err
	}
	if earlier >= ThrottlesBeforePause {
		ap := &AutoPause{AccountID: t.AccountID, CampaignID: campaignID, CampaignName: t.CampaignName, Rates: r, At: now}
		ap.Reason = fmt.Sprintf("It was paused automatically: %q went over the early-warning limits for spam complaints (%s) "+
			"or hard bounces (%s), the third campaign to do so in 30 days.",
			t.CampaignName, per10k(t.Warning.ComplaintsPer10k), per10k(t.Warning.HardBouncesPer10k))
		if _, err := tx.Exec(ctx, `UPDATE campaigns SET auto_paused_at = $2 WHERE id = $1`, campaignID, now); err != nil {
			return Health{}, err
		}
		return Health{Pause: ap}, pauseAccount(ctx, tx, ap)
	}
	t.Reason = throttleReason(t)
	if _, err := tx.Exec(ctx, `
		INSERT INTO account_throttles (account_id, campaign_id, reason, rates, created_at, until)
		VALUES ($1, $2, $3, $4, $5, $6)`, t.AccountID, campaignID, t.Reason, r, now, t.Until); err != nil {
		return Health{}, err
	}
	details := map[string]any{"sent": r.Sent, "hard_bounces": r.HardBounces, "complaints": r.Complaints, "until": t.Until}
	if _, err := tx.Exec(ctx, `
		INSERT INTO admin_actions (actor, action, account_id, target_id, reason, details, created_at)
		VALUES ('automatic', 'throttle', $1, $2, $3, $4, $5)`, t.AccountID, campaignID, t.Reason, details, now); err != nil {
		return Health{}, err
	}
	return Health{Throttle: t}, nil
}

// SendLimit is an account's daily limit now, and why it's under the usual
// one.
type SendLimit struct {
	// Daily is how many campaign recipients it may be sent to in 24 hours.
	Daily int `json:"daily"`
	// Usual is the limit for an established account in good standing.
	Usual int `json:"usual"`
	// Reason says why Daily is under Usual, or why it's set by hand; nil
	// when it's the usual limit.
	Reason *string `json:"reason"`
	// LoweredUntil is when a throttle lifts; nil when not throttled.
	LoweredUntil *time.Time `json:"lowered_until"`
	// NeedsDomain is true when Daily is held at the new-account limit
	// because the account has no verified domain of its own (MG-98):
	// adding one is how it sends more.
	NeedsDomain bool `json:"needs_domain"`
}

// ForAccount is an account's daily limit (CMP-7, MG-61). own is its own
// limit, set by staff; since is when it started sending.
//
// A throttled account has the new-account limit (or its own, if lower)
// until the throttle lifts. Otherwise an own limit stands. Otherwise the
// limit grows with clean history from since, or from when the last throttle
// lifted: the new-account limit for the first week, then doubling each
// week up to the usual limit, held at the new-account limit while the last
// 30 days aren't clean.
//
// With shared sending on (MG-98), an account with no verified domain of its
// own stays at the new-account limit, NeedsDomain set, since everything it
// sends goes from mailgoose's shared domain. Once it verifies one, the
// limit grows as above from when it verified its first still-verified
// domain, if that's later, so a new domain starts at the new-account limit
// too.
func (l Limits) ForAccount(ctx context.Context, q querier, accountID string, own *int, since, now time.Time) (SendLimit, error) {
	out, err := l.forAccount(ctx, q, accountID, own, since, now)
	if err != nil || l.FreeDaily <= 0 || out.Daily <= l.FreeDaily && out.Usual <= l.FreeDaily {
		return out, err
	}
	var plan string
	if err := q.QueryRow(ctx, `SELECT plan FROM accounts WHERE id = $1`, accountID).Scan(&plan); err != nil {
		return out, err
	}
	if plan != "free" {
		return out, nil
	}
	// The free plan's cap comes after every other rule (MG-120): it lowers
	// the limit and the usual one, and says why only when nothing else has.
	out.Daily = min(out.Daily, l.FreeDaily)
	out.Usual = min(out.Usual, l.FreeDaily)
	if out.Reason == nil {
		r := fmt.Sprintf("The free plan sends at most %s a day. The paid plan raises it, up to %s a day.",
			thousands(l.FreeDaily), thousands(l.Daily))
		out.Reason = &r
	}
	return out, nil
}

func (l Limits) forAccount(ctx context.Context, q querier, accountID string, own *int, since, now time.Time) (SendLimit, error) {
	out := SendLimit{Usual: l.Daily}
	reason := func(s string) { out.Reason = &s }
	var until *time.Time
	var why string
	err := q.QueryRow(ctx, `SELECT until, reason FROM account_throttles WHERE account_id = $1 ORDER BY until DESC LIMIT 1`,
		accountID).Scan(&until, &why)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	if until != nil && until.After(now) {
		out.Daily = l.NewDaily
		if own != nil {
			out.Daily = min(*own, l.NewDaily)
		}
		u := until.UTC()
		out.LoweredUntil = &u
		reason(fmt.Sprintf("%s It's %s a day until %s, then grows again each week of clean sending.",
			why, thousands(out.Daily), u.Format("Jan 2, 2006 15:04 UTC")))
		return out, nil
	}
	if own != nil {
		out.Daily = *own
		reason("Set for this account by mailgoose.")
		return out, nil
	}
	fromDomain := false
	if l.SharedDomain {
		var first *time.Time
		if err := q.QueryRow(ctx, `SELECT min(coalesce(verified_at, created_at)) FROM domains WHERE account_id = $1 AND status = 'verified'`,
			accountID).Scan(&first); err != nil {
			return out, err
		}
		if first == nil {
			out.Daily = min(l.NewDaily, l.Daily)
			out.NeedsDomain = true
			reason(fmt.Sprintf("Accounts without a verified domain of their own send at most %s a day, from mailgoose's shared domain. "+
				"Add your own domain to send more: the limit then doubles each week of clean sending, up to %s a day.",
				thousands(out.Daily), thousands(l.Daily)))
			return out, nil
		}
		if first.After(since) {
			since, fromDomain = *first, true
		}
	}
	lifted := until != nil && until.After(since)
	if lifted {
		since = *until
	}
	age := now.Sub(since)
	clean := true
	if age >= NewAccountDays*24*time.Hour {
		if clean, err = accountClean(ctx, q, accountID, now); err != nil {
			return out, err
		}
	}
	out.Daily = l.NewDaily
	if age >= NewAccountDays*24*time.Hour {
		out.Daily = LimitSchedule(l.NewDaily, l.Daily, age, clean)
	}
	switch {
	case out.Daily >= l.Daily:
		out.Reason = nil
	case !clean:
		reason(fmt.Sprintf("Held at %s a day: a campaign in the last 30 days went over the spam-complaint (%s) or hard-bounce (%s) limit.",
			thousands(out.Daily), per10k(ComplaintsPer10k), per10k(HardBouncesPer10k)))
	case lifted:
		reason(fmt.Sprintf("Growing again after a lowered limit: it doubles each week of clean sending, up to %s a day.", thousands(l.Daily)))
	case fromDomain:
		reason(fmt.Sprintf("Growing from when your domain was verified: it doubles each week of clean sending, up to %s a day.", thousands(l.Daily)))
	default:
		reason(fmt.Sprintf("New accounts start at %s a day; it doubles each week of clean sending, up to %s a day.",
			thousands(l.NewDaily), thousands(l.Daily)))
	}
	return out, nil
}

// NeedsDomain reports whether an account stays at the new-account limit
// because it has no verified domain of its own while shared sending is on
// (MG-98).
func (l Limits) NeedsDomain(ctx context.Context, q querier, accountID string) (bool, error) {
	if !l.SharedDomain {
		return false, nil
	}
	var has bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM domains WHERE account_id = $1 AND status = 'verified')`, accountID).Scan(&has)
	return !has, err
}

// ownLimitSQL is an account's own daily limit, from a row of accounts a: the
// limit staff set, held to tester.Daily for a tester account (MG-86).
var ownLimitSQL = fmt.Sprintf(`CASE WHEN a.tester_started_at IS NOT NULL
	THEN least(coalesce(a.daily_send_limit, %[1]d), %[1]d) ELSE a.daily_send_limit END`, tester.Daily)

// Account is ForAccount for an account read from the database.
func (l Limits) Account(ctx context.Context, q querier, accountID string, now time.Time) (SendLimit, error) {
	var own *int
	var since time.Time
	var isTester bool
	if err := q.QueryRow(ctx, `SELECT `+ownLimitSQL+`, coalesce(a.activated_at, a.created_at), a.tester_started_at IS NOT NULL
		FROM accounts a WHERE a.id = $1`,
		accountID).Scan(&own, &since, &isTester); err != nil {
		return SendLimit{Usual: l.Daily}, err
	}
	out, err := l.ForAccount(ctx, q, accountID, own, since, now)
	if err == nil && isTester && out.LoweredUntil == nil && out.Daily == tester.Daily {
		r := fmt.Sprintf("Tester accounts send to at most %d people a day.", tester.Daily)
		out.Reason = &r
	}
	return out, err
}

// thousands writes n with thousands separators: 10,000.
func thousands(n int) string {
	s := strconv.Itoa(n)
	if n < 0 {
		return "-" + thousands(-n)
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// Notices tells account owners about changes to their sending (MG-61), and
// mailgoose's operators through Ops.
type Notices struct {
	Mail mail.Sender
	From mail.Address
	// NewDaily is the limit a throttled account drops to.
	NewDaily int
	Ops      *OpsAlert
	Log      *slog.Logger
}

func (n *Notices) log() *slog.Logger {
	if n.Log == nil {
		return slog.Default()
	}
	return n.Log
}

// Throttled tells the owner and the operators about a throttle. It never
// fails: an email that can't be sent is logged.
func (n *Notices) Throttled(ctx context.Context, t Throttle) {
	n.log().WarnContext(ctx, "account throttled automatically", "account_id", t.AccountID, "campaign_id", t.CampaignID,
		"sent", t.Rates.Sent, "hard_bounces", t.Rates.HardBounces, "complaints", t.Rates.Complaints, "until", t.Until)
	if n.Ops != nil {
		n.Ops.Throttled(ctx, t, n.NewDaily)
	}
	if t.OwnerEmail == "" || n.Mail == nil {
		return
	}
	w := t.Warning.orDefault()
	text := fmt.Sprintf(`Your daily sending limit is lowered to %s for 7 days.

Your campaign %q went to %s people. %s of them marked it as spam (%d people), and %s of the addresses don't exist (%d hard bounces). Our early-warning limits are %s for spam complaints and %s for hard bounces.

Lists with people who didn't ask for your email, or old addresses, are the usual cause. While the limit is lower:
- Send only to people who signed up for your email or bought from you.
- Remove addresses you haven't mailed in a long time.
- Make sure people know who you are from the From name and subject.

The lower limit lifts on its own on %s if no campaign goes over these limits meanwhile, and then grows again each week of clean sending. A third lowered limit within 30 days pauses sending.

Questions? Reply to this email.
`, thousands(n.NewDaily), t.CampaignName, thousands(t.Rates.Sent),
		percent(t.Rates.Complaints, t.Rates.Sent), t.Rates.Complaints,
		percent(t.Rates.HardBounces, t.Rates.Sent), t.Rates.HardBounces,
		per10k(w.ComplaintsPer10k), per10k(w.HardBouncesPer10k), t.Until.UTC().Format("Jan 2, 2006 at 15:04 UTC"))
	_, err := n.Mail.Send(context.WithoutCancel(ctx), mail.Message{
		From:      n.From,
		To:        mail.Address{Email: t.OwnerEmail},
		Subject:   "Your mailgoose sending limit is lowered for 7 days",
		Text:      text,
		HTML:      "<!doctype html><html><body><pre style=\"white-space:pre-wrap;font-family:inherit\">" + html.EscapeString(text) + "</pre></body></html>",
		Kind:      mail.KindSystem,
		AccountID: t.AccountID,
	})
	if err != nil {
		n.log().ErrorContext(ctx, "send the throttle notice", "err", err, "account_id", t.AccountID)
	}
}

// Throttled tells the operators about a throttle, as AutoPaused does a
// pause.
func (a *OpsAlert) Throttled(ctx context.Context, t Throttle, newDaily int) {
	text := fmt.Sprintf(`THROTTLE (early warning): %s (account %s) is limited to %s a day until %s.

Campaign %q (%s): %d sent, %d hard bounces (%s), %d complaints (%s).

%s

Look into it with:
  mailgoose admin account %s
Pause it with:
  mailgoose admin pause %s --reason "..."
`, t.AccountName, t.AccountID, thousands(newDaily), t.Until.UTC().Format(time.RFC3339),
		t.CampaignName, t.CampaignID, t.Rates.Sent,
		t.Rates.HardBounces, percent(t.Rates.HardBounces, t.Rates.Sent),
		t.Rates.Complaints, percent(t.Rates.Complaints, t.Rates.Sent), t.Reason, t.AccountID, t.AccountID)
	a.Notify(ctx, t.AccountName+" throttled automatically (early warning)", text)
}
