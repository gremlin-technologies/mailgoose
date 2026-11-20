package sending

import (
	"cmp"
	"context"
	"math"
	"slices"
	"time"
)

// The per-customer deliverability report (MG-82). Each account that sent
// campaign email in the last 7 days, or had a hard bounce or complaint
// reported in them, with its copies sent, hard bounce rate and complaint
// rate over the last 24 hours and 7 days, counted exactly as the digest
// counts the platform's rates (sentIn, hardBouncedIn, complainedIn), worst
// first. `mailgoose admin deliverability` lists it, and the daily digest
// lists the accounts over the early warning over 7 days.

// DeliverabilityWindow is the long window of the report; the short one is
// DigestWindow.
const DeliverabilityWindow = 7 * 24 * time.Hour

// How a window's campaign copies, hard bounces and complaints are counted,
// for the platform (platformRates) and for each account
// (DeliverabilityReport) alike. $1 and $2 bound the window.
const (
	sentIn        = `FROM campaign_recipients WHERE sent_at > $1 AND sent_at <= $2`
	hardBouncedIn = `FROM send_events
				WHERE occurred_at > $1 AND occurred_at <= $2 AND kind = 'campaign' AND type = 'bounce' AND subtype LIKE 'permanent/%'`
	complainedIn = `FROM send_events
				WHERE occurred_at > $1 AND occurred_at <= $2 AND kind = 'campaign' AND type = 'complaint'`
)

// WindowRates are an account's rates over one window, with the rates as
// percentages, rounded to 4 places; nil when nothing was sent in it.
type WindowRates struct {
	Rates
	HardBouncePercent *float64 `json:"hard_bounce_percent"`
	ComplaintPercent  *float64 `json:"complaint_percent"`
}

func windowRates(r Rates) WindowRates {
	w := WindowRates{Rates: r}
	if r.Sent > 0 {
		pct := func(n int) *float64 {
			p := math.Round(float64(n)*100/float64(r.Sent)*10000) / 10000
			return &p
		}
		w.HardBouncePercent, w.ComplaintPercent = pct(r.HardBounces), pct(r.Complaints)
	}
	return w
}

// AccountDeliverability is one account's line in the report.
type AccountDeliverability struct {
	AccountID   string  `json:"account_id"`
	AccountName string  `json:"account_name"`
	OwnerEmail  *string `json:"owner_email"`
	// SendingMode is test or full (MG-80).
	SendingMode     string     `json:"sending_mode"`
	SendingPausedAt *time.Time `json:"sending_paused_at"`
	SuspendedAt     *time.Time `json:"suspended_at"`
	// ThrottledUntil is when its early-warning throttle lifts; nil when it
	// isn't throttled now.
	ThrottledUntil *time.Time  `json:"throttled_until"`
	Day            WindowRates `json:"last_24_hours"`
	Week           WindowRates `json:"last_7_days"`
	// OverWarning says whether its last 7 days are over the early warning,
	// on enough copies and complaints, as the digest judges them.
	OverWarning bool `json:"over_warning"`
}

// State says how the account stands, for people: "paused, throttled until
// ..., test mode", or "sending normally".
func (a AccountDeliverability) State() []string {
	var s []string
	if a.SuspendedAt != nil {
		s = append(s, "suspended")
	}
	if a.SendingPausedAt != nil {
		s = append(s, "paused")
	}
	if a.ThrottledUntil != nil {
		s = append(s, "throttled until "+a.ThrottledUntil.UTC().Format("2006-01-02 15:04 UTC"))
	}
	if a.SendingMode == "test" {
		s = append(s, "test mode")
	}
	return s
}

// severity is how far rates are over the early warning: the larger of the
// complaint rate and the hard-bounce rate, each as a multiple of its
// threshold. Nothing sent is 0.
func severity(r Rates, w Warning) float64 {
	if r.Sent == 0 {
		return 0
	}
	w = w.orDefault()
	c := float64(r.Complaints) * 10000 / (float64(r.Sent) * float64(w.ComplaintsPer10k))
	b := float64(r.HardBounces) * 10000 / (float64(r.Sent) * float64(w.HardBouncesPer10k))
	return max(c, b)
}

// SortWorstFirst orders the report worst first: accounts over the early
// warning over 7 days first, then by how far over the threshold their
// 7-day rates are, then their 24-hour rates, then the most copies sent.
func SortWorstFirst(rows []AccountDeliverability, w Warning) {
	slices.SortStableFunc(rows, func(a, b AccountDeliverability) int {
		if a.OverWarning != b.OverWarning {
			if a.OverWarning {
				return -1
			}
			return 1
		}
		return cmp.Or(
			cmp.Compare(severity(b.Week.Rates, w), severity(a.Week.Rates, w)),
			cmp.Compare(severity(b.Day.Rates, w), severity(a.Day.Rates, w)),
			cmp.Compare(b.Week.Sent, a.Week.Sent),
			cmp.Compare(a.AccountName, b.AccountName),
			cmp.Compare(a.AccountID, b.AccountID),
		)
	})
}

// DeliverabilityReport is every account that sent campaign email in the
// DeliverabilityWindow up to now, or had a hard bounce or complaint
// reported in it, worst first (SortWorstFirst), judged by w.
func DeliverabilityReport(ctx context.Context, q digestQuerier, now time.Time, w Warning) ([]AccountDeliverability, error) {
	day, week := now.Add(-DigestWindow), now.Add(-DeliverabilityWindow)
	// Each count is the platform's, by account; the 7-day window is the
	// query's, and the 24 hours a filter on it ($3).
	rows, err := q.Query(ctx, `
		WITH sent AS (
			SELECT account_id, count(*) FILTER (WHERE sent_at > $3) AS day, count(*) AS week
			`+sentIn+` GROUP BY account_id
		), bounced AS (
			SELECT account_id, count(DISTINCT recipient_id) FILTER (WHERE occurred_at > $3) AS day,
				count(DISTINCT recipient_id) AS week
			`+hardBouncedIn+` AND account_id IS NOT NULL GROUP BY account_id
		), complained AS (
			SELECT account_id, count(DISTINCT recipient_id) FILTER (WHERE occurred_at > $3) AS day,
				count(DISTINCT recipient_id) AS week
			`+complainedIn+` AND account_id IS NOT NULL GROUP BY account_id
		), active AS (
			SELECT account_id FROM sent UNION SELECT account_id FROM bounced UNION SELECT account_id FROM complained
		)
		SELECT a.id::text, a.name,
			(SELECT u.email FROM users u WHERE u.account_id = a.id ORDER BY u.created_at, u.id LIMIT 1),
			a.sending_mode, a.sending_paused_at, a.suspended_at,
			(SELECT max(t.until) FROM account_throttles t WHERE t.account_id = a.id),
			coalesce(s.day, 0), coalesce(b.day, 0), coalesce(c.day, 0),
			coalesce(s.week, 0), coalesce(b.week, 0), coalesce(c.week, 0)
		FROM active x JOIN accounts a ON a.id = x.account_id
			LEFT JOIN sent s ON s.account_id = a.id
			LEFT JOIN bounced b ON b.account_id = a.id
			LEFT JOIN complained c ON c.account_id = a.id`, week, now, day)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AccountDeliverability{}
	for rows.Next() {
		var a AccountDeliverability
		var d, wk Rates
		if err := rows.Scan(&a.AccountID, &a.AccountName, &a.OwnerEmail, &a.SendingMode, &a.SendingPausedAt,
			&a.SuspendedAt, &a.ThrottledUntil, &d.Sent, &d.HardBounces, &d.Complaints,
			&wk.Sent, &wk.HardBounces, &wk.Complaints); err != nil {
			return nil, err
		}
		if a.ThrottledUntil != nil && !a.ThrottledUntil.After(now) {
			a.ThrottledUntil = nil
		}
		a.Day, a.Week = windowRates(d), windowRates(wk)
		a.OverWarning = wk.OverWarning(w)
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	SortWorstFirst(out, w)
	return out, nil
}
