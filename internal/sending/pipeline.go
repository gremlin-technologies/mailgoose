package sending

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/campaigns"
	"github.com/gremlin-technologies/mailgoose/internal/contacts"
	"github.com/gremlin-technologies/mailgoose/internal/credits"
	"github.com/gremlin-technologies/mailgoose/internal/mail"
	"github.com/gremlin-technologies/mailgoose/internal/render"
	"github.com/gremlin-technologies/mailgoose/internal/tracking"
	"github.com/gremlin-technologies/mailgoose/internal/unsubscribe"
)

// The send pipeline (CAM-7, CAM-8, CMP-7).
//
// Launch writes the campaign's recipients (campaign_recipients) and a
// send_queue row. The dispatcher, on a timer, takes due rows: it starts
// scheduled campaigns, puts the recipients in batches on the send queue
// (SQS in AWS, in memory locally) within each account's daily limit, and
// finishes campaigns whose recipients are all done. Workers take batches
// and send each recipient's copy at the configured rate.
//
// Each recipient row is its send record. A worker claims the row (status
// sending) in its own transaction before sending and marks it sent after,
// so a batch delivered twice, or picked up after a worker died, never sends
// a row twice. A claim that never finished (the worker died between the
// two) can't be told apart from a sent copy that wasn't recorded, so it's
// not sent again: after ClaimTimeout the dispatcher marks it failed and
// gives its credit back.

// Pipeline settings.
const (
	// MaxBatch is the most recipients in one batch.
	MaxBatch = 100
	// MaxAttempts is how many times a batch is tried before its rows are
	// marked failed. It matches the send queue's redrive policy
	// (infra/modules/queue, max_receive_count), so the last try's message
	// goes to the dead-letter queue.
	MaxAttempts = 5
	// ClaimTimeout is how long a claimed row may stay unsent before it's
	// taken as interrupted.
	ClaimTimeout = 10 * time.Minute
	// RequeueAfter is how long an enqueued row may wait for its batch
	// before the dispatcher puts it in a new batch, in case the first was
	// lost. A row sent by both batches is still sent once.
	RequeueAfter = time.Hour
	// NewAccountDays is how long an account counts as new for its daily
	// limit (CMP-7).
	NewAccountDays = 7
	// maxEnqueuePerTick caps one campaign's rows enqueued per tick, so a
	// big campaign doesn't hold one transaction for long.
	maxEnqueuePerTick = 2000
)

// Batch is one message on the send queue: recipient rows of one campaign.
type Batch struct {
	AccountID  string  `json:"account_id"`
	CampaignID string  `json:"campaign_id"`
	IDs        []int64 `json:"ids"`
}

// Queue carries batches from the dispatcher to the workers.
type Queue interface {
	Enqueue(ctx context.Context, batches []Batch) error
}

// Limits are the send limits (CAM-7, CMP-7).
type Limits struct {
	// PerSecond is the most campaign emails sent a second, across every
	// worker and campaign together (one send_pace row keeps it).
	PerSecond int
	// Daily is an account's daily limit once it's past its first week, and
	// NewDaily its limit in that week. An account's own daily_send_limit
	// overrides both.
	Daily, NewDaily int
}

// Pipeline runs the dispatcher and the workers' batches.
type Pipeline struct {
	DB          *pgxpool.Pool
	Mail        mail.Sender
	Unsubscribe *unsubscribe.Service
	// BaseURL is the app's public URL, for unsubscribe links.
	BaseURL string
	Limits  Limits
	Log     *slog.Logger
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

func (p *Pipeline) now() time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
}

func (p *Pipeline) log() *slog.Logger {
	if p.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return p.Log
}

// BatchSize is how many recipients go in a batch: about ten seconds of
// sending at the configured rate when one batch runs at a time. The rate
// is shared, so with several batches running each takes longer; the worker
// extends a batch's visibility on the queue while it runs.
func (p *Pipeline) BatchSize() int {
	return min(MaxBatch, max(10, p.Limits.PerSecond*10))
}

// RunDispatcher dispatches every interval until ctx ends.
func (p *Pipeline) RunDispatcher(ctx context.Context, q Queue, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := p.Dispatch(ctx, q); err != nil && ctx.Err() == nil {
			p.log().ErrorContext(ctx, "dispatch", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Dispatch makes one pass over the due send_queue rows: each campaign is
// started if it's scheduled, gets its next batches, or is finished.
// Several dispatchers can run at once; each row is worked by one at a time.
func (p *Pipeline) Dispatch(ctx context.Context, q Queue) error {
	var after int64
	for ctx.Err() == nil {
		next, err := p.dispatchOne(ctx, q, after)
		if err != nil {
			return err
		}
		if next == 0 {
			return nil
		}
		after = next
	}
	return ctx.Err()
}

// dispatchOne works the first due send_queue row after the id after, and
// returns its id, or 0 when there's none.
func (p *Pipeline) dispatchOne(ctx context.Context, q Queue, after int64) (int64, error) {
	tx, err := p.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	now := p.now()
	var rowID int64
	var accountID, campaignID, status string
	var paused bool
	var limit *int
	var since time.Time
	err = tx.QueryRow(ctx, `
		SELECT q.id, q.account_id, q.campaign_id, c.status, a.sending_paused_at IS NOT NULL, a.daily_send_limit,
			coalesce(a.activated_at, a.created_at)
		FROM send_queue q JOIN campaigns c ON c.id = q.campaign_id JOIN accounts a ON a.id = q.account_id
		WHERE q.done_at IS NULL AND q.run_at <= $1 AND q.id > $2
		ORDER BY q.id LIMIT 1
		FOR UPDATE OF q SKIP LOCKED`, now, after).
		Scan(&rowID, &accountID, &campaignID, &status, &paused, &limit, &since)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}

	switch status {
	case campaigns.StatusScheduled:
		// Due: start it, unless it was cancelled or moved meanwhile.
		tag, err := tx.Exec(ctx, `
			UPDATE campaigns SET status = 'sending', status_changed_at = $2, updated_at = $2 WHERE id = $1 AND status = 'scheduled'`,
			campaignID, now)
		if err != nil {
			return 0, err
		}
		if tag.RowsAffected() == 0 {
			return rowID, tx.Commit(ctx)
		}
	case campaigns.StatusSending:
	case campaigns.StatusPaused:
		// Held until it's resumed or stopped (CMP-8).
		return rowID, tx.Commit(ctx)
	default:
		// Cancelled or failed: nothing more to send. Cancelling gives the
		// credits back (MG-32).
		if _, err := tx.Exec(ctx, `UPDATE send_queue SET done_at = $2 WHERE id = $1`, rowID, now); err != nil {
			return 0, err
		}
		return rowID, tx.Commit(ctx)
	}

	if err := p.failStaleClaims(ctx, tx, accountID, campaignID, now); err != nil {
		return 0, err
	}
	var batches []Batch
	if !paused {
		daily, err := p.dailyLimit(ctx, tx, accountID, limit, since, now)
		if err != nil {
			return 0, err
		}
		if batches, err = p.nextBatches(ctx, tx, accountID, campaignID, daily, now); err != nil {
			return 0, err
		}
	}
	if len(batches) == 0 {
		if err := p.finishIfDone(ctx, tx, rowID, accountID, campaignID, now); err != nil {
			return 0, err
		}
		return rowID, tx.Commit(ctx)
	}
	// The rows are marked enqueued in this transaction and the batches
	// sent before it commits: if the commit fails, the next pass sends
	// them again, and the claims keep that from sending anyone twice.
	if q != nil {
		if err := q.Enqueue(ctx, batches); err != nil {
			return 0, fmt.Errorf("enqueue: %w", err)
		}
	}
	return rowID, tx.Commit(ctx)
}

// nextBatches marks the campaign's next rows enqueued, as many as the
// account's daily limit allows, and returns them in batches. Rows enqueued
// long ago whose batch never came are taken again.
func (p *Pipeline) nextBatches(ctx context.Context, tx pgx.Tx, accountID, campaignID string, limit int, now time.Time) ([]Batch, error) {
	var used int
	if err := tx.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM campaign_recipients WHERE account_id = $1 AND sent_at > $2)
			+ (SELECT count(*) FROM campaign_recipients WHERE account_id = $1
				AND status IN ('queued', 'sending') AND enqueued_at IS NOT NULL AND enqueued_at > $3)`,
		accountID, now.Add(-24*time.Hour), now.Add(-RequeueAfter)).Scan(&used); err != nil {
		return nil, err
	}
	room := min(limit-used, maxEnqueuePerTick)
	if room <= 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `
		UPDATE campaign_recipients SET enqueued_at = $3
		WHERE id IN (
			SELECT id FROM campaign_recipients
			WHERE campaign_id = $1 AND status = 'queued' AND (enqueued_at IS NULL OR enqueued_at <= $4)
			ORDER BY id LIMIT $2 FOR UPDATE SKIP LOCKED)
		RETURNING id`, campaignID, room, now, now.Add(-RequeueAfter))
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, err
	}
	slices.Sort(ids)
	size := p.BatchSize()
	var out []Batch
	for len(ids) > 0 {
		n := min(size, len(ids))
		out = append(out, Batch{AccountID: accountID, CampaignID: campaignID, IDs: ids[:n:n]})
		ids = ids[n:]
	}
	return out, nil
}

// failStaleClaims marks rows claimed longer than ClaimTimeout ago failed
// and gives their credits back.
func (p *Pipeline) failStaleClaims(ctx context.Context, tx pgx.Tx, accountID, campaignID string, now time.Time) error {
	tag, err := tx.Exec(ctx, `
		UPDATE campaign_recipients SET status = 'failed',
			error = 'interrupted while sending; not sent again in case it went out'
		WHERE campaign_id = $1 AND status = 'sending' AND claimed_at <= $2`, campaignID, now.Add(-ClaimTimeout))
	if err != nil {
		return err
	}
	if n := tag.RowsAffected(); n > 0 {
		if _, err := credits.Release(ctx, tx, accountID, creditsRef(campaignID), n, now); err != nil {
			return err
		}
	}
	return nil
}

// FailStoppedClaims does what failStaleClaims does for campaigns the
// dispatcher no longer visits: ones stopped or cancelled while a worker
// held a copy, when that worker then died. Their stale claims are marked
// failed and the credits given back. It returns how many rows it failed.
// Claimed rows always came in a batch, so enqueued_at is set, which lets
// the in-flight index find them.
func FailStoppedClaims(ctx context.Context, db *pgxpool.Pool, now time.Time) (int64, error) {
	rows, err := db.Query(ctx, `
		SELECT DISTINCT r.account_id, r.campaign_id FROM campaign_recipients r
		WHERE r.status = 'sending' AND r.enqueued_at IS NOT NULL AND r.claimed_at <= $1
			AND NOT EXISTS (SELECT 1 FROM send_queue q WHERE q.campaign_id = r.campaign_id AND q.done_at IS NULL)`, now.Add(-ClaimTimeout))
	if err != nil {
		return 0, err
	}
	type stale struct{ account, campaign string }
	var found []stale
	for rows.Next() {
		var st stale
		if err := rows.Scan(&st.account, &st.campaign); err != nil {
			rows.Close()
			return 0, err
		}
		found = append(found, st)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	var total int64
	for _, st := range found {
		err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
			// The status check makes each row fail once, however many
			// workers sweep at the same time.
			tag, err := tx.Exec(ctx, `
				UPDATE campaign_recipients SET status = 'failed',
					error = 'interrupted while sending; not sent again in case it went out'
				WHERE campaign_id = $1 AND status = 'sending' AND claimed_at <= $2
					AND NOT EXISTS (SELECT 1 FROM send_queue q WHERE q.campaign_id = $1 AND q.done_at IS NULL)`,
				st.campaign, now.Add(-ClaimTimeout))
			if err != nil {
				return err
			}
			n := tag.RowsAffected()
			if n > 0 {
				if _, err := credits.Release(ctx, tx, st.account, creditsRef(st.campaign), n, now); err != nil {
					return err
				}
			}
			total += n
			return nil
		})
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// finishIfDone ends a campaign whose rows are all done: sent, or failed
// when none went out.
func (p *Pipeline) finishIfDone(ctx context.Context, tx pgx.Tx, rowID int64, accountID, campaignID string, now time.Time) error {
	var open, sent int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE status IN ('queued', 'sending')), count(*) FILTER (WHERE status = 'sent')
		FROM campaign_recipients WHERE campaign_id = $1`, campaignID).Scan(&open, &sent); err != nil {
		return err
	}
	if open > 0 {
		return nil
	}
	status := campaigns.StatusSent
	if sent == 0 {
		var failed int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM campaign_recipients WHERE campaign_id = $1 AND status = 'failed'`,
			campaignID).Scan(&failed); err != nil {
			return err
		}
		if failed > 0 {
			status = campaigns.StatusFailed
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE campaigns SET status = $3, status_changed_at = $4, updated_at = $4
		WHERE account_id = $1 AND id = $2 AND status = 'sending'`, accountID, campaignID, status, now); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE send_queue SET done_at = $2 WHERE id = $1`, rowID, now)
	return err
}

// sendable is what a batch needs of its campaign.
type sendable struct {
	status string
	paused bool
	copy   launchCopy
	signer *unsubscribe.Signer
	// links makes tracked addresses; nil when tracking is off.
	links *tracking.Links
}

func (p *Pipeline) loadCampaign(ctx context.Context, b Batch) (*sendable, error) {
	var s sendable
	var raw []byte
	err := p.DB.QueryRow(ctx, `
		SELECT c.status, a.sending_paused_at IS NOT NULL, c.launch
		FROM campaigns c JOIN accounts a ON a.id = c.account_id
		WHERE c.id = $1 AND c.account_id = $2`, b.CampaignID, b.AccountID).Scan(&s.status, &s.paused, &raw)
	if err != nil {
		return nil, err
	}
	if s.status != campaigns.StatusSending || s.paused {
		return &s, nil
	}
	if err := json.Unmarshal(raw, &s.copy); err != nil {
		return nil, fmt.Errorf("read the launched email: %w", err)
	}
	if s.copy.Template == nil {
		return nil, errors.New("the launched email has no template")
	}
	if s.signer, err = p.Unsubscribe.Signer(ctx, b.AccountID); err != nil {
		return nil, err
	}
	if s.copy.Tracking {
		if s.links, err = tracking.NewLinks(s.signer, p.BaseURL, b.CampaignID); err != nil {
			return nil, err
		}
	}
	return &s, nil
}

// HandleBatch sends a batch. attempt counts from 1. It returns an error to
// have the batch tried again; on the last attempt, rows that fail to send
// are marked failed with their credits given back, and the error is still
// returned so the message goes to the dead-letter queue.
func (p *Pipeline) HandleBatch(ctx context.Context, b Batch, attempt int) error {
	s, err := p.loadCampaign(ctx, b)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // The campaign is gone.
	}
	if err != nil {
		return err
	}
	if s.status != campaigns.StatusSending || s.paused {
		return p.handBack(ctx, b)
	}
	var lastErr error
	for _, id := range b.IDs {
		err := p.sendOne(ctx, b, s, id)
		var se *sendFailed
		switch {
		case err == nil:
		case errors.Is(err, errHalted):
			// Paused or stopped partway through the batch.
			return p.handBack(ctx, b)
		case errors.As(err, &se) && attempt >= MaxAttempts:
			if err := p.markFailed(ctx, b, id, se.err); err != nil {
				return err
			}
			lastErr = err
		default:
			return err
		}
	}
	if lastErr != nil {
		return fmt.Errorf("some copies failed after %d attempts: %w", attempt, lastErr)
	}
	return nil
}

// errHalted is a copy not sent because its campaign or account stopped
// sending since the batch began.
var errHalted = errors.New("sending halted")

// handBack returns a batch's unsent rows when its campaign or account is
// paused or stopped: they wait, and the dispatcher batches them again once
// the campaign is sending. A stopped campaign's rows are already skipped.
func (p *Pipeline) handBack(ctx context.Context, b Batch) error {
	_, err := p.DB.Exec(ctx, `
		UPDATE campaign_recipients SET enqueued_at = NULL WHERE id = ANY($1) AND campaign_id = $2 AND status = 'queued'`,
		b.IDs, b.CampaignID)
	return err
}

// sendFailed is a copy the mail provider refused: its claim was taken back,
// so it can be tried again.
type sendFailed struct{ err error }

func (e *sendFailed) Error() string { return "send: " + e.err.Error() }
func (e *sendFailed) Unwrap() error { return e.err }

// sendOne sends one row's copy, if it's still waiting.
func (p *Pipeline) sendOne(ctx context.Context, b Batch, s *sendable, id int64) error {
	r, ok, err := p.claim(ctx, b, id)
	if err != nil || !ok {
		return err
	}
	// Only a copy that will really go out waits for a send slot, so
	// skipped rows and rows already sent (a redelivery) don't use one.
	if err := p.paceWait(ctx); err != nil {
		// Nothing was sent: the claim goes back for a later attempt.
		if _, rerr := p.DB.Exec(context.WithoutCancel(ctx), `
			UPDATE campaign_recipients SET status = 'queued', claimed_at = NULL WHERE id = $1 AND status = 'sending'`, id); rerr != nil {
			return errors.Join(err, rerr)
		}
		return err
	}
	url, err := s.signer.URL(strings.TrimRight(p.BaseURL, "/"), r.contactID, b.CampaignID)
	if err != nil {
		return err
	}
	m := s.copy.Template.Personalize(render.Recipient{Email: r.email, FirstName: r.first, LastName: r.last, UnsubscribeURL: url})
	if s.links != nil {
		unsub := strings.TrimRight(p.BaseURL, "/") + unsubscribe.PathPrefix
		m.HTML = render.Track(m.HTML, s.links.For(id), func(href string) bool {
			return href == url || strings.HasPrefix(href, unsub)
		})
	}
	msg := mail.Message{
		From:       s.copy.From,
		To:         mail.Address{Email: r.email},
		ReplyTo:    s.copy.ReplyTo,
		Subject:    m.Subject,
		HTML:       m.HTML,
		Text:       m.Text,
		Kind:       mail.KindCampaign,
		AccountID:  b.AccountID,
		CampaignID: b.CampaignID,
	}
	for _, h := range m.Headers {
		msg.Headers = append(msg.Headers, mail.Header{Name: h.Name, Value: h.Value})
	}
	messageID, sendErr := p.Mail.Send(ctx, msg)
	if sendErr != nil {
		if ctx.Err() != nil {
			// Shutting down mid-send: it may have gone out, so the claim
			// stays and the row ends failed rather than sent twice.
			return ctx.Err()
		}
		// Refused: take the claim back so a later attempt can send it.
		_, err := p.DB.Exec(context.WithoutCancel(ctx), `
			UPDATE campaign_recipients SET status = 'queued', claimed_at = NULL, error = $2 WHERE id = $1 AND status = 'sending'`,
			id, truncate(sendErr.Error(), 500))
		if err != nil {
			return err
		}
		return &sendFailed{err: sendErr}
	}
	_, err = p.DB.Exec(context.WithoutCancel(ctx), `
		UPDATE campaign_recipients SET status = 'sent', sent_at = $2, message_id = $3, error = NULL
		WHERE id = $1 AND status = 'sending'`, id, p.now(), messageID)
	if err != nil {
		// The copy went out but isn't recorded: the row will end failed
		// and refunded. The message id lets it be matched up by hand.
		p.log().ErrorContext(ctx, "a sent copy wasn't recorded", "campaign_id", b.CampaignID,
			"recipient_row", id, "message_id", messageID, "err", err)
	}
	return err
}

type claimed struct {
	contactID, email, first, last string
}

// claim takes a row for sending. A row that's no longer queued isn't
// claimed (ok false). One whose contact is gone or no longer subscribed is
// skipped and its credit given back.
func (p *Pipeline) claim(ctx context.Context, b Batch, id int64) (claimed, bool, error) {
	var r claimed
	tx, err := p.DB.Begin(ctx)
	if err != nil {
		return r, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	now := p.now()
	var contactID *string
	// Only while the campaign is sending and its account isn't paused, so
	// a pause or a stop takes effect at the next copy (CMP-8).
	err = tx.QueryRow(ctx, `
		UPDATE campaign_recipients SET status = 'sending', claimed_at = $3
		WHERE id = $1 AND campaign_id = $2 AND status = 'queued'
			AND EXISTS (SELECT 1 FROM campaigns c JOIN accounts a ON a.id = c.account_id
				WHERE c.id = $2 AND c.status = 'sending' AND a.sending_paused_at IS NULL)
		RETURNING contact_id`, id, b.CampaignID, now).Scan(&contactID)
	if errors.Is(err, pgx.ErrNoRows) {
		var live bool
		if err := tx.QueryRow(ctx, `
			SELECT c.status = 'sending' AND a.sending_paused_at IS NULL
			FROM campaigns c JOIN accounts a ON a.id = c.account_id WHERE c.id = $1`, b.CampaignID).Scan(&live); err != nil &&
			!errors.Is(err, pgx.ErrNoRows) {
			return r, false, err
		}
		if !live {
			return r, false, errHalted
		}
		return r, false, nil
	}
	if err != nil {
		return r, false, err
	}
	var status string
	var first, last *string
	if contactID != nil {
		err = tx.QueryRow(ctx, `SELECT email, status, first_name, last_name FROM contacts WHERE id = $1`, *contactID).
			Scan(&r.email, &status, &first, &last)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return r, false, err
		}
	}
	if contactID == nil || !contacts.CanReceive(status) {
		if _, err := tx.Exec(ctx, `UPDATE campaign_recipients SET status = 'skipped' WHERE id = $1`, id); err != nil {
			return r, false, err
		}
		if _, err := credits.Release(ctx, tx, b.AccountID, creditsRef(b.CampaignID), 1, now); err != nil {
			return r, false, err
		}
		return r, false, tx.Commit(ctx)
	}
	r.contactID, r.first, r.last = *contactID, trimmed(first), trimmed(last)
	return r, true, tx.Commit(ctx)
}

// markFailed ends a row that failed on the last attempt and gives its
// credit back.
func (p *Pipeline) markFailed(ctx context.Context, b Batch, id int64, cause error) error {
	tx, err := p.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `UPDATE campaign_recipients SET status = 'failed', error = $2 WHERE id = $1 AND status = 'queued'`,
		id, truncate(cause.Error(), 500))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		if _, err := credits.Release(ctx, tx, b.AccountID, creditsRef(b.CampaignID), 1, p.now()); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// paceWait waits for the next send slot. Slots are spaced 1/PerSecond
// apart across every worker, by the send_pace row and the database's clock.
func (p *Pipeline) paceWait(ctx context.Context) error {
	interval := time.Second / time.Duration(max(1, p.Limits.PerSecond))
	var secs float64
	if err := p.DB.QueryRow(ctx, `
		UPDATE send_pace SET next_at = greatest(next_at, clock_timestamp()) + $1::interval WHERE id = 1
		RETURNING extract(epoch FROM next_at - $1::interval - clock_timestamp())::float8`, interval).Scan(&secs); err != nil {
		return fmt.Errorf("take a send slot: %w", err)
	}
	wait := time.Duration(secs * float64(time.Second))
	if wait <= 0 {
		return nil
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
