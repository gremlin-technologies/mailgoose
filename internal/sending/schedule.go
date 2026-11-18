package sending

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/campaigns"
	"github.com/gremlin-technologies/mailgoose/internal/credits"
)

// Scheduled campaigns (CAM-6): they start at their time, and until then
// can be cancelled, giving their credits back, or moved.

// RescheduleError is a send time that can't be used.
type RescheduleError struct{ Message string }

func (e *RescheduleError) Error() string { return e.Message }

// lockScheduled locks a launched campaign's queue row, then the campaign,
// in the order the dispatcher takes them, and returns the campaign.
func (s *Service) lockScheduled(ctx context.Context, tx pgx.Tx, accountID, campaignID string) (*campaigns.Campaign, error) {
	var one int
	err := tx.QueryRow(ctx, `SELECT 1 FROM send_queue WHERE account_id = $1 AND campaign_id = $2 FOR UPDATE`,
		accountID, campaignID).Scan(&one)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if err := tx.QueryRow(ctx, `SELECT 1 FROM campaigns WHERE account_id = $1 AND id = $2 FOR UPDATE`,
		accountID, campaignID).Scan(&one); errors.Is(err, pgx.ErrNoRows) {
		return nil, campaigns.ErrNotFound
	} else if err != nil {
		return nil, err
	}
	return s.Campaigns.Get(ctx, accountID, campaignID)
}

// Cancel stops a scheduled campaign before it starts: its recipients are
// skipped and their reserved credits given back. Cancelling a cancelled
// campaign changes nothing. One that has started sending can't be
// cancelled this way (a StatusError).
func (s *Service) Cancel(ctx context.Context, accountID, campaignID string) (*campaigns.Campaign, error) {
	return s.end(ctx, accountID, campaignID, "cancelled before sending", campaigns.StatusScheduled)
}

// Stop is the kill switch for one campaign (CMP-8): a scheduled, sending or
// paused campaign is cancelled where it stands. Nobody else gets it, the
// credits for who's left come back, and a batch already on its way sends
// nothing more (each send checks the campaign first). Copies being sent
// at that moment still go. Stopping a cancelled campaign changes nothing.
func (s *Service) Stop(ctx context.Context, accountID, campaignID string) (*campaigns.Campaign, error) {
	return s.end(ctx, accountID, campaignID, "stopped by mailgoose",
		campaigns.StatusScheduled, campaigns.StatusSending, campaigns.StatusPaused)
}

// end cancels a campaign in one of the allowed statuses.
func (s *Service) end(ctx context.Context, accountID, campaignID, note string, allowed ...string) (*campaigns.Campaign, error) {
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		c, err := s.lockScheduled(ctx, tx, accountID, campaignID)
		if err != nil {
			return err
		}
		if c.Status == campaigns.StatusCancelled {
			return nil
		}
		if !slices.Contains(allowed, c.Status) {
			return &campaigns.StatusError{Status: c.Status, To: campaigns.StatusCancelled}
		}
		now := s.now()
		tag, err := tx.Exec(ctx, `
			UPDATE campaign_recipients SET status = 'skipped', error = $2
			WHERE campaign_id = $1 AND status = 'queued'`, campaignID, note)
		if err != nil {
			return err
		}
		if n := tag.RowsAffected(); n > 0 {
			if _, err := credits.Release(ctx, tx, accountID, creditsRef(campaignID), n, now); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE campaigns SET status = 'cancelled', status_changed_at = $3, updated_at = $3
			WHERE account_id = $1 AND id = $2`, accountID, campaignID, now); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE send_queue SET done_at = $2 WHERE campaign_id = $1 AND done_at IS NULL`, campaignID, now)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.Campaigns.Get(ctx, accountID, campaignID)
}

// Reschedule moves a scheduled campaign's send time. The time must be in
// the future and at most a year ahead.
func (s *Service) Reschedule(ctx context.Context, accountID, campaignID string, at time.Time) (*campaigns.Campaign, error) {
	at = at.UTC()
	now := s.now()
	switch {
	case !at.After(now):
		return nil, &RescheduleError{Message: "the send time must be in the future"}
	case at.After(now.Add(campaigns.MaxScheduleAhead)):
		return nil, &RescheduleError{Message: "the send time can be at most a year ahead"}
	}
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		c, err := s.lockScheduled(ctx, tx, accountID, campaignID)
		if err != nil {
			return err
		}
		if c.Status != campaigns.StatusScheduled {
			return &campaigns.StatusError{Status: c.Status}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE campaigns SET send_at = $3, updated_at = $4 WHERE account_id = $1 AND id = $2`,
			accountID, campaignID, at, now); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE send_queue SET run_at = $2 WHERE campaign_id = $1 AND done_at IS NULL`, campaignID, at)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.Campaigns.Get(ctx, accountID, campaignID)
}

// StartDue moves every scheduled campaign whose time has come to sending,
// and returns their ids. Each is started once, however many workers run
// it at the same time: the update only takes campaigns still scheduled.
// It checks the campaign's own send_at too: the queue row isn't locked
// here, so if a reschedule commits while this waits on the campaign,
// Postgres rechecks only the campaign row, and send_at is what shows the
// move.
func StartDue(ctx context.Context, db *pgxpool.Pool, now time.Time) ([]string, error) {
	rows, err := db.Query(ctx, `
		UPDATE campaigns c SET status = 'sending', status_changed_at = $1, updated_at = $1
		FROM send_queue q
		WHERE q.campaign_id = c.id AND q.done_at IS NULL AND q.run_at <= $1
			AND c.status = 'scheduled' AND c.send_at <= $1
		RETURNING c.id`, now.UTC())
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// RunScheduler starts due scheduled campaigns every interval until ctx
// ends. The worker runs it; the dispatcher also starts a due campaign it
// comes to first, and either way it starts once.
func RunScheduler(ctx context.Context, db *pgxpool.Pool, interval time.Duration, log *slog.Logger) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		ids, err := StartDue(ctx, db, time.Now())
		switch {
		case err != nil && ctx.Err() == nil:
			log.ErrorContext(ctx, "start scheduled campaigns", "err", err)
		case len(ids) > 0:
			log.InfoContext(ctx, "scheduled campaigns started", "campaign_ids", ids)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
