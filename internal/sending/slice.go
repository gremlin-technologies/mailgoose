package sending

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gremlin-technologies/mailgoose/internal/credits"
)

// Sliced first sends (MG-62). A new account's campaign goes first to a
// slice of its recipients, picked at random at launch. Once the slice is
// out, the rest waits (an hour by default) for bounces and complaints to
// come in, then the slice is checked:
//
//   - over the pause thresholds (0.3% complaints, 5% hard bounces), the
//     account is paused as for any campaign and the rest never goes: its
//     credits come back and the campaign ends as cancelled;
//   - over the early-warning thresholds only (MG-61), the account is
//     throttled and the rest held until the throttle lifts, then checked
//     again against the pause thresholds;
//   - otherwise the rest goes out as usual.
//
// The thresholds apply to the slice whatever its size: the 500-copy floor
// of the usual check would let every slice through.
//
// An account is new for its first 30 days or its first 3 launched
// campaigns, whichever ends later.
const (
	// SliceAccountDays and SliceCampaigns are how long an account's
	// campaigns go out in slices.
	SliceAccountDays = 30
	SliceCampaigns   = 3
)

// Slice states, in campaigns.slice_state.
const (
	SliceSending  = "sending_slice"
	SliceHold     = "slice_hold"
	SliceHeld     = "held"
	SliceReleased = "released"
	SliceStopped  = "stopped"
)

// Slicing is how a new account's campaigns are sliced: Percent of the
// recipients, at least Min, then a wait of Wait.
type Slicing struct {
	Percent int
	Min     int
	Wait    time.Duration
}

// DefaultSlicing is 10%, at least 200, then an hour.
var DefaultSlicing = Slicing{Percent: 10, Min: 200, Wait: time.Hour}

func (s Slicing) orDefault() Slicing {
	d := DefaultSlicing
	if s.Percent > 0 {
		d.Percent = s.Percent
	}
	if s.Min > 0 {
		d.Min = s.Min
	}
	if s.Wait > 0 {
		d.Wait = s.Wait
	}
	return d
}

// Size is the slice for a campaign of n recipients: Percent of them,
// rounded up, and at least Min. It's 0, for no slice, when that would be
// the whole list: lists of Min or fewer go out whole.
func (s Slicing) Size(n int) int {
	s = s.orDefault()
	size := max(s.Min, (n*s.Percent+99)/100)
	if size >= n {
		return 0
	}
	return size
}

// sliceLaunch picks the slice of a campaign being launched to n
// recipients, inside the launch's transaction, when its account is new.
// It returns the slice's size, 0 for none.
func (s *Service) sliceLaunch(ctx context.Context, tx pgx.Tx, accountID, campaignID string, n int, now time.Time) (int, error) {
	size := s.Slicing.Size(n)
	if size == 0 {
		return 0, nil
	}
	var since time.Time
	var launched int
	if err := tx.QueryRow(ctx, `
		SELECT coalesce(a.activated_at, a.created_at),
			(SELECT count(*) FROM campaigns c WHERE c.account_id = a.id AND c.launched_at IS NOT NULL AND c.id <> $2)
		FROM accounts a WHERE a.id = $1`, accountID, campaignID).Scan(&since, &launched); err != nil {
		return 0, err
	}
	if now.Sub(since) >= SliceAccountDays*24*time.Hour && launched >= SliceCampaigns {
		return 0, nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE campaign_recipients SET in_slice = true
		WHERE id IN (SELECT id FROM campaign_recipients WHERE campaign_id = $1 ORDER BY random() LIMIT $2)`,
		campaignID, size); err != nil {
		return 0, err
	}
	_, err := tx.Exec(ctx, `UPDATE campaigns SET slice_size = $2, slice_state = $3 WHERE id = $1`,
		campaignID, size, SliceSending)
	return size, err
}

// SliceRates counts the copies of a campaign's slice sent, hard bounced
// and complained about.
func SliceRates(ctx context.Context, q querier, campaignID string) (Rates, error) {
	var r Rates
	err := q.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE r.status = 'sent'),
			count(*) FILTER (WHERE EXISTS (SELECT 1 FROM send_events e WHERE e.campaign_id = r.campaign_id
				AND e.recipient_id = r.id AND e.type = 'bounce' AND e.subtype LIKE 'permanent/%')),
			count(*) FILTER (WHERE EXISTS (SELECT 1 FROM send_events e WHERE e.campaign_id = r.campaign_id
				AND e.recipient_id = r.id AND e.type = 'complaint'))
		FROM campaign_recipients r WHERE r.campaign_id = $1 AND r.in_slice`, campaignID).
		Scan(&r.Sent, &r.HardBounces, &r.Complaints)
	return r, err
}

// sliceSent moves a campaign whose slice has all gone out (sent, failed or
// skipped) to slice_hold, and its queue row to the release time. It
// reports whether it did.
func (p *Pipeline) sliceSent(ctx context.Context, tx pgx.Tx, rowID int64, campaignID string, now time.Time) (bool, error) {
	var open int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM campaign_recipients WHERE campaign_id = $1 AND in_slice AND status IN ('queued', 'sending')`,
		campaignID).Scan(&open); err != nil {
		return false, err
	}
	if open > 0 {
		return false, nil
	}
	return true, p.holdSlice(ctx, tx, rowID, campaignID, SliceHold, now.Add(p.Slicing.orDefault().Wait))
}

// holdSlice puts a campaign in a waiting slice state until at.
func (p *Pipeline) holdSlice(ctx context.Context, tx pgx.Tx, rowID int64, campaignID, state string, at time.Time) error {
	if _, err := tx.Exec(ctx, `UPDATE campaigns SET slice_state = $2, slice_release_at = $3 WHERE id = $1`,
		campaignID, state, at); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE send_queue SET run_at = $2 WHERE id = $1`, rowID, at)
	return err
}

// releaseSlice checks a held campaign's slice when its wait is over, and
// releases the rest, holds it or stops it. It reports whether the rest was
// released, and what the slice led to, for the alerts.
func (p *Pipeline) releaseSlice(ctx context.Context, tx pgx.Tx, rowID int64, accountID, campaignID, state string, now time.Time) (bool, Health, error) {
	r, err := SliceRates(ctx, tx, campaignID)
	if err != nil {
		return false, Health{}, err
	}
	var paused bool
	if err := tx.QueryRow(ctx, `SELECT auto_paused_at IS NOT NULL FROM campaigns WHERE id = $1`, campaignID).Scan(&paused); err != nil {
		return false, Health{}, err
	}
	switch {
	case paused || r.overPause():
		h, err := autoPause(ctx, tx, campaignID, r, now)
		if err != nil {
			return false, Health{}, err
		}
		return false, h, p.stopSlice(ctx, tx, rowID, accountID, campaignID, r, now)
	case state == SliceHold && r.overWarning(p.Warning):
		h, err := throttle(ctx, tx, campaignID, r, now, p.Warning)
		if err != nil {
			return false, Health{}, err
		}
		if h.Pause != nil {
			return false, h, p.stopSlice(ctx, tx, rowID, accountID, campaignID, r, now)
		}
		// Held until the account's throttle lifts: this one's, or one
		// already running when the campaign was throttled before.
		until := now.Add(ThrottleFor)
		var latest *time.Time
		if err := tx.QueryRow(ctx, `SELECT max(until) FROM account_throttles WHERE account_id = $1`, accountID).
			Scan(&latest); err != nil {
			return false, Health{}, err
		}
		if latest != nil && latest.After(now) {
			until = *latest
		}
		return false, h, p.holdSlice(ctx, tx, rowID, campaignID, SliceHeld, until)
	}
	_, err = tx.Exec(ctx, `UPDATE campaigns SET slice_state = $2, slice_release_at = NULL WHERE id = $1`, campaignID, SliceReleased)
	return err == nil, Health{}, err
}

// stopSlice ends a campaign whose slice went over the pause thresholds:
// the rest is skipped, its credits come back, and the campaign is
// cancelled.
func (p *Pipeline) stopSlice(ctx context.Context, tx pgx.Tx, rowID int64, accountID, campaignID string, r Rates, now time.Time) error {
	tag, err := tx.Exec(ctx, `
		UPDATE campaign_recipients SET status = 'skipped', error = $2
		WHERE campaign_id = $1 AND status = 'queued'`, campaignID,
		fmt.Sprintf("not sent: the first %d copies went over the bounce or complaint limits", r.Sent))
	if err != nil {
		return err
	}
	if n := tag.RowsAffected(); n > 0 {
		if _, err := credits.Release(ctx, tx, accountID, creditsRef(campaignID), n, now); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE campaigns SET status = 'cancelled', status_changed_at = $2, updated_at = $2,
			slice_state = $3, slice_release_at = NULL
		WHERE id = $1`, campaignID, now, SliceStopped); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE send_queue SET done_at = $2 WHERE id = $1`, rowID, now)
	return err
}

// reportHealth tells p.Health what a slice led to, after its transaction.
func (p *Pipeline) reportHealth(ctx context.Context, h Health) {
	if p.Health != nil && (h.Pause != nil || h.Throttle != nil) {
		p.Health(ctx, h)
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
