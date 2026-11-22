// Package reports counts what happened to a campaign (REP-1, REP-2): who it
// went to, deliveries, bounces, complaints, unsubscribes, opens, clicks per
// link and credits used. The API, the CLI and the web app all read
// Service.Campaign, straight from the events as they're stored, so the
// numbers are as fresh as the events (REP-5).
//
// Raw events (opens, clicks and SES's deliveries, bounces and complaints)
// are kept 12 months. Prune runs in the worker: a campaign launched more
// than 12 months ago has its report saved to campaign_reports and its
// events deleted, and any other event older than 12 months is deleted.
package reports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/campaigns"
)

// Report is one campaign's numbers. Counts of people count each copy once,
// however many events it had.
type Report struct {
	CampaignID string     `json:"campaign_id"`
	Status     string     `json:"status"`
	LaunchedAt *time.Time `json:"launched_at"`
	Tracking   bool       `json:"tracking"`
	// Recipients is how many copies went out.
	Recipients int `json:"recipients"`
	// Delivered is copies SES says reached the receiving server.
	Delivered int `json:"delivered"`
	// Bounced is copies that hard bounced.
	Bounced      int `json:"bounced"`
	Complaints   int `json:"complaints"`
	Unsubscribes int `json:"unsubscribes"`
	// Opens and Clicks are nil when the campaign wasn't tracked.
	Opens  *Opens  `json:"opens"`
	Clicks *Clicks `json:"clicks"`
	// CreditsUsed is what the campaign has taken from the account's
	// credits; while it's sending, that includes credits held for the
	// copies still to go.
	CreditsUsed int64  `json:"credits_used"`
	Links       []Link `json:"links"`
	// Archived means the raw events are gone (12 months after launch) and
	// these are the numbers saved then.
	Archived bool      `json:"archived"`
	AsOf     time.Time `json:"as_of"`
}

// Opens are unique opens. They're always an estimate: mail apps that load
// images for privacy (Apple Mail) open every copy, and those that block
// images open none.
type Opens struct {
	Unique    int  `json:"unique"`
	Estimated bool `json:"estimated"`
}

// Clicks are people who clicked any link, and all clicks.
type Clicks struct {
	Unique int `json:"unique"`
	Total  int `json:"total"`
}

// Link is one link's clicks. Position counts the email's links from 1, in
// order. When the address differs between copies (filled in by merge tags),
// URL is only the site's address and Varies is true, so no one person's
// link is shown.
type Link struct {
	Position     int    `json:"position"`
	URL          string `json:"url"`
	Varies       bool   `json:"varies"`
	UniqueClicks int    `json:"unique_clicks"`
	TotalClicks  int    `json:"total_clicks"`
}

// Retention is how long raw events are kept.
const Retention = 12 // months

// Service reads reports.
type Service struct {
	DB *pgxpool.Pool
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Campaign is a campaign's report, or campaigns.ErrNotFound.
func (s *Service) Campaign(ctx context.Context, accountID, campaignID string) (*Report, error) {
	return s.campaign(ctx, s.DB, accountID, campaignID, true)
}

// countColumns are the report's counts for the campaign aliased c, joined
// to its saved report as s. The report and the campaign list's summaries
// both select them, so a list's numbers are the report's.
const countColumns = `c.status, c.launched_at, c.tracking,
			(SELECT count(*) FROM campaign_recipients r WHERE r.campaign_id = c.id AND r.status = 'sent'),
			(SELECT count(DISTINCT recipient_id) FROM send_events e
				WHERE e.campaign_id = c.id AND e.kind = 'campaign' AND e.type = 'delivery'),
			(SELECT count(DISTINCT recipient_id) FROM send_events e
				WHERE e.campaign_id = c.id AND e.kind = 'campaign' AND e.type = 'bounce' AND e.subtype LIKE 'permanent/%'),
			(SELECT count(DISTINCT recipient_id) FROM send_events e
				WHERE e.campaign_id = c.id AND e.kind = 'campaign' AND e.type = 'complaint'),
			(SELECT count(DISTINCT coalesce(u.contact_id, u.id)) FROM unsubscribes u WHERE u.campaign_id = c.id),
			(SELECT count(DISTINCT recipient_id) FROM engagement_events g WHERE g.campaign_id = c.id AND g.type = 'open'),
			(SELECT count(DISTINCT recipient_id) FROM engagement_events g WHERE g.campaign_id = c.id AND g.type = 'click'),
			(SELECT count(*) FROM engagement_events g WHERE g.campaign_id = c.id AND g.type = 'click'),
			(SELECT coalesce(-sum(t.amount), 0) FROM credit_transactions t
				WHERE t.account_id = c.account_id AND t.ref = 'campaign:' || c.id AND t.type IN ('reserve', 'release')),
			s.report, s.frozen_at`

// scanCounts reads countColumns into a report for campaignID, without its
// links. With archived, a campaign whose events are gone gives the numbers
// saved then.
func (s *Service) scanCounts(row pgx.Row, campaignID string, archived bool, extra ...any) (*Report, error) {
	r := &Report{CampaignID: campaignID, AsOf: s.now().UTC(), Links: []Link{}}
	var saved []byte
	var frozenAt *time.Time
	var opens, uniqueClicks, totalClicks int
	dest := append(extra, &r.Status, &r.LaunchedAt, &r.Tracking, &r.Recipients, &r.Delivered, &r.Bounced, &r.Complaints,
		&r.Unsubscribes, &opens, &uniqueClicks, &totalClicks, &r.CreditsUsed, &saved, &frozenAt)
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	if archived && saved != nil {
		var old Report
		if err := json.Unmarshal(saved, &old); err != nil {
			return nil, fmt.Errorf("read the saved report: %w", err)
		}
		old.Archived, old.AsOf = true, frozenAt.UTC()
		if old.Links == nil {
			old.Links = []Link{}
		}
		return &old, nil
	}
	if r.Tracking {
		r.Opens = &Opens{Unique: opens, Estimated: true}
		r.Clicks = &Clicks{Unique: uniqueClicks, Total: totalClicks}
	}
	return r, nil
}

func (s *Service) campaign(ctx context.Context, q querier, accountID, campaignID string, archived bool) (*Report, error) {
	r, err := s.scanCounts(q.QueryRow(ctx, `
		SELECT `+countColumns+`
		FROM campaigns c LEFT JOIN campaign_reports s ON s.campaign_id = c.id
		WHERE c.id = $1 AND c.account_id = $2`, campaignID, accountID), campaignID, archived)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, campaigns.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if r.Archived || !r.Tracking {
		return r, nil
	}
	rows, err := q.Query(ctx, `
		SELECT link, min(url), count(DISTINCT url) > 1, count(DISTINCT recipient_id), count(*)
		FROM engagement_events WHERE campaign_id = $1 AND type = 'click'
		GROUP BY link ORDER BY link`, campaignID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var l Link
		if err := rows.Scan(&l.Position, &l.URL, &l.Varies, &l.UniqueClicks, &l.TotalClicks); err != nil {
			return nil, err
		}
		if l.Varies {
			l.URL = site(l.URL)
		}
		r.Links = append(r.Links, l)
	}
	return r, rows.Err()
}

// site is a link's scheme and host, without anything that could be one
// person's.
func site(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// Pruned is what one Prune did.
type Pruned struct {
	Archived int   // campaigns whose report was saved
	Events   int64 // raw events deleted
}

// pruneBatch is how many rows one DELETE takes, so pruning never holds
// long locks.
const pruneBatch = 5000

// Prune saves the reports of campaigns launched more than Retention months
// ago and deletes their raw events, then deletes any other raw event older
// than Retention months (REP-5).
func (s *Service) Prune(ctx context.Context) (Pruned, error) {
	var p Pruned
	cutoff := s.now().AddDate(0, -Retention, 0)
	for {
		var accountID, campaignID string
		err := s.DB.QueryRow(ctx, `
			SELECT c.account_id, c.id FROM campaigns c
			WHERE c.launched_at < $1 AND c.status IN ('sent', 'cancelled', 'failed')
				AND NOT EXISTS (SELECT 1 FROM campaign_reports s WHERE s.campaign_id = c.id)
			ORDER BY c.launched_at LIMIT 1`, cutoff).Scan(&accountID, &campaignID)
		if errors.Is(err, pgx.ErrNoRows) {
			break
		}
		if err != nil {
			return p, err
		}
		n, archived, err := s.archive(ctx, accountID, campaignID)
		if err != nil {
			return p, fmt.Errorf("archive campaign %s: %w", campaignID, err)
		}
		if archived {
			p.Archived++
			p.Events += n
		}
	}
	// A campaign still sending or paused keeps all its events, so its live
	// report doesn't drift; it's archived once it finishes.
	for _, table := range []string{"engagement_events", "send_events"} {
		for {
			tag, err := s.DB.Exec(ctx, `DELETE FROM `+table+` WHERE id IN (
				SELECT e.id FROM `+table+` e WHERE e.occurred_at < $1
					AND NOT EXISTS (SELECT 1 FROM campaigns c
						WHERE c.id = e.campaign_id AND c.status IN ('sending', 'paused'))
				LIMIT $2)`, cutoff, pruneBatch)
			if err != nil {
				return p, fmt.Errorf("prune %s: %w", table, err)
			}
			p.Events += tag.RowsAffected()
			if tag.RowsAffected() < pruneBatch {
				break
			}
		}
	}
	return p, nil
}

// archive saves a campaign's report and deletes its raw events, in one
// transaction. If another pruner saved the report first, it does nothing and
// returns false.
func (s *Service) archive(ctx context.Context, accountID, campaignID string) (int64, bool, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	r, err := s.campaign(ctx, tx, accountID, campaignID, false)
	if err != nil {
		return 0, false, err
	}
	body, err := json.Marshal(r)
	if err != nil {
		return 0, false, err
	}
	// A second pruner waits here for the first to commit, then inserts
	// nothing.
	tag, err := tx.Exec(ctx, `INSERT INTO campaign_reports (campaign_id, account_id, report, frozen_at) VALUES ($1, $2, $3, $4)
		ON CONFLICT (campaign_id) DO NOTHING`,
		campaignID, accountID, body, s.now())
	if err != nil {
		return 0, false, err
	}
	if tag.RowsAffected() == 0 {
		return 0, false, nil
	}
	var n int64
	for _, table := range []string{"engagement_events", "send_events"} {
		tag, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE campaign_id = $1`, campaignID)
		if err != nil {
			return 0, false, err
		}
		n += tag.RowsAffected()
	}
	return n, true, tx.Commit(ctx)
}

// RunPruner prunes every interval until ctx ends.
func (s *Service) RunPruner(ctx context.Context, interval time.Duration, log *slog.Logger) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		p, err := s.Prune(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			log.ErrorContext(ctx, "prune old events", "err", err)
		case p.Archived > 0 || p.Events > 0:
			log.InfoContext(ctx, "old events pruned", "campaigns_archived", p.Archived, "events_deleted", p.Events)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
