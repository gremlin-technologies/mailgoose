package sending

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/mail"
)

// slowSender takes delay over every copy, as an SES round trip does, and
// calls then (when set) after the nth copy it sent, counting from 1.
type slowSender struct {
	mail.Fake
	delay time.Duration
	n     int
	then  func(n int) error
}

func (s *slowSender) Send(ctx context.Context, m mail.Message) (string, error) {
	time.Sleep(s.delay)
	s.n++
	if s.then != nil {
		if err := s.then(s.n); err != nil {
			return "", err
		}
	}
	return s.Fake.Send(ctx, m)
}

// batchLogs are the "send batch finished" lines of a JSON log.
func batchLogs(t *testing.T, logs string) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(strings.NewReader(logs))
	for sc.Scan() {
		var line map[string]any
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatalf("%v: %s", err, sc.Text())
		}
		if line["msg"] == "send batch finished" {
			out = append(out, line)
		}
	}
	return out
}

// timings are the campaign's recorded batch timings, oldest first.
type timing struct {
	started, finished           time.Time
	size, sent, skipped, failed int
	accountID, campaignID       string
}

func (h *harness) timings(t *testing.T) []timing {
	t.Helper()
	rows, err := h.db.Query(context.Background(), `
		SELECT started_at, finished_at, batch_size, sent, skipped, failed, account_id::text, campaign_id::text
		FROM send_batch_timings ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []timing
	for rows.Next() {
		var x timing
		if err := rows.Scan(&x.started, &x.finished, &x.size, &x.sent, &x.skipped, &x.failed, &x.accountID, &x.campaignID); err != nil {
			t.Fatal(err)
		}
		out = append(out, x)
	}
	return out
}

// TestPipelineMeasuresBatchThroughput logs each batch's copies sent,
// skipped and failed, its seconds and its copies a second at INFO, with no
// addresses; records the batches that sent a copy, pruning old ones; and
// SendThroughput reads them back (MG-91).
func TestPipelineMeasuresBatchThroughput(t *testing.T) {
	h := launchedHarness(t, 5)
	ctx := context.Background()
	p := h.pipeline(fastLimits)
	var logs strings.Builder
	p.Log = slog.New(slog.NewJSONHandler(&logs, nil))
	const delay = 30 * time.Millisecond
	p.Mail = &slowSender{delay: delay}
	q := &recorded{}
	dispatch(t, p, q)
	batches := q.take()
	if len(batches) != 1 || len(batches[0].IDs) != 6 {
		t.Fatalf("batches %+v", batches)
	}
	if _, err := h.db.Exec(ctx, `UPDATE contacts SET status = 'unsubscribed' WHERE account_id = $1 AND email = 'reader1@example.com'`,
		h.acc); err != nil {
		t.Fatal(err)
	}
	// A timing older than BatchTimingRetention is pruned when a new one is
	// recorded.
	if _, err := h.db.Exec(ctx, `
		INSERT INTO send_batch_timings (account_id, campaign_id, started_at, finished_at, batch_size, sent, skipped, failed)
		VALUES ($1, $2, $3, $3, 1, 1, 0, 0)`, h.acc, h.campaign.ID, h.now.Add(-BatchTimingRetention-time.Hour)); err != nil {
		t.Fatal(err)
	}

	if err := p.HandleBatch(ctx, batches[0], 1); err != nil {
		t.Fatal(err)
	}
	lines := batchLogs(t, logs.String())
	if len(lines) != 1 {
		t.Fatalf("%d batch lines:\n%s", len(lines), logs.String())
	}
	l := lines[0]
	secs, _ := l["seconds"].(float64)
	rate, _ := l["copies_per_second"].(float64)
	if l["level"] != "INFO" || l["campaign_id"] != h.campaign.ID || l["account_id"] != h.acc || l["sent"] != 5.0 ||
		l["skipped"] != 1.0 || l["failed"] != 0.0 || l["rows"] != 6.0 || l["attempt"] != 1.0 || l["ended"] != "done" ||
		l["send_rate_per_second"] != float64(fastLimits.PerSecond) {
		t.Errorf("line %v", l)
	}
	if secs < (5*delay).Seconds() || rate <= 0 || rate > 1/delay.Seconds() || math.Abs(rate*secs-5) > 0.05 {
		t.Errorf("%v seconds, %v a second", secs, rate)
	}
	if strings.Contains(logs.String(), "@") {
		t.Errorf("an address is logged:\n%s", logs.String())
	}
	got := h.timings(t)
	if len(got) != 1 {
		t.Fatalf("timings %+v", got)
	}
	g := got[0]
	took := g.finished.Sub(g.started)
	if !g.started.Equal(h.now) || took < 5*delay || math.Abs(took.Seconds()-secs) > 0.002 || g.size != 6 || g.sent != 5 ||
		g.skipped != 1 || g.failed != 0 || g.accountID != h.acc || g.campaignID != h.campaign.ID {
		t.Errorf("timing %+v (took %v)", g, took)
	}

	// A redelivery sends nothing: it's logged, but not recorded.
	logs.Reset()
	if err := p.HandleBatch(ctx, batches[0], 2); err != nil {
		t.Fatal(err)
	}
	if lines := batchLogs(t, logs.String()); len(lines) != 1 || lines[0]["sent"] != 0.0 || lines[0]["copies_per_second"] != 0.0 ||
		lines[0]["attempt"] != 2.0 {
		t.Errorf("redelivery lines %v", lines)
	}
	if n := len(h.timings(t)); n != 1 {
		t.Errorf("%d timings after a redelivery", n)
	}

	tp, err := SendThroughput(ctx, h.db, h.now.Add(time.Minute), 10)
	if err != nil {
		t.Fatal(err)
	}
	if tp.Batches != 1 || tp.Copies != 5 || math.Abs(tp.BusySeconds-took.Seconds()) > 0.002 || tp.BatchSeconds != tp.BusySeconds ||
		tp.CopiesPerSecond == nil || math.Abs(*tp.CopiesPerSecond-5/tp.BusySeconds) > 0.01 ||
		tp.PerBatchCopiesPerSecond == nil || *tp.PerBatchCopiesPerSecond != *tp.CopiesPerSecond ||
		tp.SendRatePerSecond != 10 || !tp.Since.Equal(h.now.Add(time.Minute-ThroughputWindow)) {
		t.Errorf("throughput %+v", tp)
	}
}

// A batch stopped partway, by a pause or by SES asking to retry later,
// logs and records what it sent before it stopped, and why it stopped
// (MG-91).
func TestPipelineMeasuresStoppedBatch(t *testing.T) {
	for _, tc := range []struct {
		name, ended string
		then        func(h *harness) func(n int) error
	}{
		{"paused", "halted", func(h *harness) func(int) error {
			return func(n int) error {
				if n == 2 {
					_, err := h.db.Exec(context.Background(), `UPDATE campaigns SET status = 'paused' WHERE id = $1`, h.campaign.ID)
					return err
				}
				return nil
			}
		}},
		{"retry later", "retry_later", func(*harness) func(int) error {
			return func(n int) error {
				if n == 3 {
					return mail.ErrRetryLater
				}
				return nil
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := launchedHarness(t, 5)
			ctx := context.Background()
			p := h.pipeline(fastLimits)
			var logs strings.Builder
			p.Log = slog.New(slog.NewJSONHandler(&logs, nil))
			p.Mail = &slowSender{then: tc.then(h)}
			q := &recorded{}
			dispatch(t, p, q)
			if err := p.HandleBatch(ctx, q.take()[0], 1); err != nil {
				t.Fatal(err)
			}
			lines := batchLogs(t, logs.String())
			if len(lines) != 1 || lines[0]["sent"] != 2.0 || lines[0]["ended"] != tc.ended {
				t.Errorf("lines %v", lines)
			}
			if got := h.timings(t); len(got) != 1 || got[0].sent != 2 {
				t.Errorf("timings %+v", got)
			}
			if c := h.counts(t); c["sent"] != 2 || c["queued"] != 4 {
				t.Errorf("counts %v", c)
			}
		})
	}
}

// SendThroughput merges batches that overlapped into the time the
// platform was busy, adds their times up apart, and counts only the
// batches that finished in the last 24 hours (MG-91).
func TestSendThroughput(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	now := h.now
	add := func(start, end time.Duration, sent int) {
		t.Helper()
		if _, err := h.db.Exec(ctx, `
			INSERT INTO send_batch_timings (account_id, campaign_id, started_at, finished_at, batch_size, sent, skipped, failed)
			VALUES ($1, $2, $3, $4, 100, $5, 0, 0)`, h.acc, h.campaign.ID, now.Add(start), now.Add(end), sent); err != nil {
			t.Fatal(err)
		}
	}

	tp, err := SendThroughput(ctx, h.db, now, 10)
	if err != nil {
		t.Fatal(err)
	}
	if tp.Batches != 0 || tp.Copies != 0 || tp.BusySeconds != 0 || tp.BatchSeconds != 0 || tp.CopiesPerSecond != nil ||
		tp.PerBatchCopiesPerSecond != nil || tp.SendRatePerSecond != 10 {
		t.Errorf("nothing sent: %+v", tp)
	}

	// Two overlapping batches, and one inside the first: 15 busy seconds.
	add(-10*time.Minute, -10*time.Minute+10*time.Second, 100)
	add(-10*time.Minute+5*time.Second, -10*time.Minute+15*time.Second, 50)
	add(-10*time.Minute+2*time.Second, -10*time.Minute+8*time.Second, 10)
	// Two at the very same times: 5 busy seconds.
	add(-5*time.Minute, -5*time.Minute+5*time.Second, 50)
	add(-5*time.Minute, -5*time.Minute+5*time.Second, 50)
	// Finished before the window, and after now: not counted.
	add(-25*time.Hour, -ThroughputWindow-time.Second, 500)
	add(time.Second, 2*time.Second, 500)

	tp, err = SendThroughput(ctx, h.db, now, 10)
	if err != nil {
		t.Fatal(err)
	}
	if tp.Batches != 5 || tp.Copies != 260 || tp.BusySeconds != 20 || tp.BatchSeconds != 36 ||
		tp.CopiesPerSecond == nil || *tp.CopiesPerSecond != 13 ||
		tp.PerBatchCopiesPerSecond == nil || *tp.PerBatchCopiesPerSecond != 7.22 {
		t.Errorf("throughput %+v", tp)
	}
}
