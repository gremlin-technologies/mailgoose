package sending

import (
	"context"
	"math"
	"time"
)

// Send throughput (MG-91, step 1: measure). A worker takes one batch at a
// time and sends its rows one after another, so each copy waits for its
// claim, its pace slot, its SES call and its update. HandleBatch logs, at
// the end of every batch's send loop, what it sent and how long that took,
// and records each batch that sent a copy in send_batch_timings, so
// `mailgoose admin deliverability` can show the last day's copies a second
// against SEND_RATE_PER_SECOND. Whether to send a batch's rows with a small
// pool waits for the numbers from staging (docs/decisions.md).
//
// SES mailbox simulator copies count like any other: they're real SES
// calls, which is what's being measured.

// BatchTimingRetention is how long batch timings are kept; the worker
// prunes older ones as it records new ones.
const BatchTimingRetention = 7 * 24 * time.Hour

// ThroughputWindow is the window the throughput report covers.
const ThroughputWindow = DigestWindow

// batchRun is one batch's pass through its send loop.
type batchRun struct {
	// started is the pipeline's clock when the loop began, for the
	// recorded times; clock is the wall clock, for the (monotonic)
	// duration.
	started time.Time
	clock   time.Time
	sent    int
	skipped int
	failed  int
	// ended is why the loop stopped early: "halted" (paused or stopped),
	// "retry_later" (MG-87), or "" for the end of the batch or an error.
	ended string
}

func (p *Pipeline) startRun() *batchRun {
	return &batchRun{started: p.now(), clock: time.Now()}
}

func (r *batchRun) count(o copyOutcome) {
	switch o {
	case sentCopy:
		r.sent++
	case skippedCopy:
		r.skipped++
	}
}

// copiesPerSecond is n over secs, rounded to 2 places; nil for no time.
func copiesPerSecond(n int, secs float64) *float64 {
	if secs <= 0 {
		return nil
	}
	v := math.Round(float64(n)/secs*100) / 100
	return &v
}

// endRun logs the batch's run at INFO and, if it sent a copy, records it.
// Recording is best effort: a failure is logged and the batch's outcome
// stands. No addresses are logged.
func (p *Pipeline) endRun(ctx context.Context, b Batch, attempt int, r *batchRun, err error) {
	elapsed := time.Since(r.clock)
	secs := elapsed.Seconds()
	ended := r.ended
	switch {
	case ended != "":
	case err != nil:
		ended = "error"
	default:
		ended = "done"
	}
	rate := 0.0
	if v := copiesPerSecond(r.sent, secs); v != nil {
		rate = *v
	}
	p.log().InfoContext(ctx, "send batch finished",
		"campaign_id", b.CampaignID, "account_id", b.AccountID, "attempt", attempt, "rows", len(b.IDs),
		"sent", r.sent, "skipped", r.skipped, "failed", r.failed, "ended", ended,
		"seconds", math.Round(secs*1000)/1000, "copies_per_second", rate,
		"send_rate_per_second", p.Limits.PerSecond)
	if r.sent == 0 {
		return
	}
	finished := r.started.Add(elapsed)
	if _, err := p.DB.Exec(context.WithoutCancel(ctx), `
		WITH pruned AS (DELETE FROM send_batch_timings WHERE finished_at < $9)
		INSERT INTO send_batch_timings (account_id, campaign_id, started_at, finished_at, batch_size, sent, skipped, failed)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		b.AccountID, b.CampaignID, r.started, finished, len(b.IDs), r.sent, r.skipped, r.failed,
		finished.Add(-BatchTimingRetention)); err != nil {
		p.log().WarnContext(ctx, "couldn't record a send batch's timing", "campaign_id", b.CampaignID, "err", err)
	}
}

// Throughput is how fast campaign copies went out over a window, from the
// batches that sent at least one copy in it (by when they finished).
type Throughput struct {
	// Since is the window's start; it runs to now.
	Since   time.Time `json:"since"`
	Batches int       `json:"batches"`
	// Copies are the copies those batches sent, SES mailbox simulator
	// copies included.
	Copies int `json:"copies"`
	// BusySeconds is the time at least one batch was sending (the batches'
	// times merged where they overlap), and CopiesPerSecond the copies
	// over it: the platform's rate while it was sending, to set against
	// SendRatePerSecond. Time between batches isn't in it.
	BusySeconds     float64  `json:"busy_seconds"`
	CopiesPerSecond *float64 `json:"copies_per_second"`
	// BatchSeconds is the batches' times added up, and
	// PerBatchCopiesPerSecond the copies over it: how fast one batch
	// sends. With one worker the two match; batches that ran at once
	// share the rate, so each was slower than the platform.
	BatchSeconds            float64  `json:"batch_seconds"`
	PerBatchCopiesPerSecond *float64 `json:"per_batch_copies_per_second"`
	// SendRatePerSecond is the configured SEND_RATE_PER_SECOND.
	SendRatePerSecond int `json:"send_rate_per_second"`
}

// SendThroughput is the throughput of the ThroughputWindow up to now, with
// perSecond as the configured rate.
func SendThroughput(ctx context.Context, q querier, now time.Time, perSecond int) (Throughput, error) {
	t := Throughput{Since: now.Add(-ThroughputWindow), SendRatePerSecond: perSecond}
	// Busy time merges overlapping batches: a batch starts a new stretch
	// when it starts after every earlier one (by start) has finished.
	err := q.QueryRow(ctx, `
		WITH b AS (
			SELECT id, started_at, finished_at, sent FROM send_batch_timings WHERE finished_at > $1 AND finished_at <= $2
		), starts AS (
			SELECT id, started_at, finished_at,
				CASE WHEN started_at <= max(finished_at) OVER (ORDER BY started_at, finished_at, id
					ROWS BETWEEN UNBOUNDED PRECEDING AND 1 PRECEDING) THEN 0 ELSE 1 END AS starts
			FROM b
		), stretches AS (
			SELECT started_at, finished_at,
				sum(starts) OVER (ORDER BY started_at, finished_at, id ROWS UNBOUNDED PRECEDING) AS stretch
			FROM starts
		), busy AS (
			SELECT max(finished_at) - min(started_at) AS took FROM stretches GROUP BY stretch
		)
		SELECT (SELECT count(*) FROM b), (SELECT coalesce(sum(sent), 0) FROM b),
			(SELECT coalesce(extract(epoch FROM sum(finished_at - started_at)), 0) FROM b)::float8,
			(SELECT coalesce(extract(epoch FROM sum(took)), 0) FROM busy)::float8`, t.Since, now).
		Scan(&t.Batches, &t.Copies, &t.BatchSeconds, &t.BusySeconds)
	if err != nil {
		return t, err
	}
	t.BatchSeconds = math.Round(t.BatchSeconds*1000) / 1000
	t.BusySeconds = math.Round(t.BusySeconds*1000) / 1000
	t.CopiesPerSecond = copiesPerSecond(t.Copies, t.BusySeconds)
	t.PerBatchCopiesPerSecond = copiesPerSecond(t.Copies, t.BatchSeconds)
	return t, nil
}
