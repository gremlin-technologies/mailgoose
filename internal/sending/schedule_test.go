package sending

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/campaigns"
)

// scheduledHarness is a harness whose campaign is launched for two days
// from now, to Sam and two more.
func scheduledHarness(t *testing.T) (*harness, time.Time) {
	t.Helper()
	ctx := context.Background()
	h := newHarness(t)
	h.buy(t, 10)
	if _, err := h.db.Exec(ctx, `
		INSERT INTO contacts (account_id, email, status, source) VALUES ($1, 'a@example.com', 'subscribed', 'api'), ($1, 'b@example.com', 'subscribed', 'api')`,
		h.acc); err != nil {
		t.Fatal(err)
	}
	at := h.now.Add(48 * time.Hour)
	c, err := h.svc.Campaigns.Update(ctx, h.acc, h.campaign.ID, campaigns.Changes{SendAt: &at})
	if err != nil {
		t.Fatal(err)
	}
	h.campaign = c
	if _, err := h.test(); err != nil {
		t.Fatal(err)
	}
	if _, err := h.launch(h.summary(t).Code); err != nil {
		t.Fatal(err)
	}
	h.fake.Reset()
	return h, at
}

func TestCancelScheduled(t *testing.T) {
	h, at := scheduledHarness(t)
	ctx := context.Background()
	if h.balance(t) != 7 {
		t.Fatalf("balance %d after launch", h.balance(t))
	}
	h.now = h.now.Add(time.Hour)
	c, err := h.svc.Cancel(ctx, h.acc, h.campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != campaigns.StatusCancelled || h.balance(t) != 10 {
		t.Errorf("status %s, balance %d", c.Status, h.balance(t))
	}
	if n := h.counts(t); n["skipped"] != 3 {
		t.Errorf("rows %v", n)
	}
	var done *time.Time
	_ = h.db.QueryRow(ctx, `SELECT done_at FROM send_queue WHERE campaign_id = $1`, h.campaign.ID).Scan(&done)
	if done == nil || !done.Equal(h.now) {
		t.Errorf("queue done %v", done)
	}
	// Again: nothing changes, and nothing more is given back.
	if c, err := h.svc.Cancel(ctx, h.acc, h.campaign.ID); err != nil || c.Status != campaigns.StatusCancelled || h.balance(t) != 10 {
		t.Errorf("again: %v %v, balance %d", c, err, h.balance(t))
	}
	// At its old time, nothing goes out.
	h.now = at
	if ids, err := StartDue(ctx, h.db, h.now); err != nil || len(ids) != 0 {
		t.Errorf("started %v %v", ids, err)
	}
	q := &recorded{}
	dispatch(t, h.pipeline(fastLimits), q)
	if len(q.take()) != 0 || len(h.fake.Sent()) != 0 {
		t.Error("a cancelled campaign was sent")
	}
	// A cancelled campaign can't be moved.
	var se *campaigns.StatusError
	if _, err := h.svc.Reschedule(ctx, h.acc, h.campaign.ID, at.Add(time.Hour)); !errors.As(err, &se) {
		t.Errorf("reschedule cancelled: %v", err)
	}
}

func TestCancelOnlyBeforeSending(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	var se *campaigns.StatusError
	if _, err := h.svc.Cancel(ctx, h.acc, h.campaign.ID); !errors.As(err, &se) || se.Status != campaigns.StatusDraft {
		t.Errorf("draft: %v", err)
	}
	if _, err := h.svc.Cancel(ctx, h.acc, "00000000-0000-0000-0000-000000000000"); !errors.Is(err, campaigns.ErrNotFound) {
		t.Errorf("missing: %v", err)
	}

	h, at := scheduledHarness(t)
	h.now = at
	ids, err := StartDue(ctx, h.db, h.now)
	if err != nil || len(ids) != 1 || ids[0] != h.campaign.ID {
		t.Fatalf("started %v %v", ids, err)
	}
	before := h.balance(t)
	if _, err := h.svc.Cancel(ctx, h.acc, h.campaign.ID); !errors.As(err, &se) || se.Status != campaigns.StatusSending {
		t.Errorf("sending: %v", err)
	}
	if _, err := h.svc.Reschedule(ctx, h.acc, h.campaign.ID, at.Add(time.Hour)); !errors.As(err, &se) {
		t.Errorf("reschedule sending: %v", err)
	}
	if h.balance(t) != before || h.status(t) != campaigns.StatusSending {
		t.Errorf("balance %d, status %s", h.balance(t), h.status(t))
	}
}

func TestReschedule(t *testing.T) {
	h, at := scheduledHarness(t)
	ctx := context.Background()
	later := at.Add(24 * time.Hour)
	c, err := h.svc.Reschedule(ctx, h.acc, h.campaign.ID, later.In(time.FixedZone("CST", -6*3600)))
	if err != nil {
		t.Fatal(err)
	}
	var runAt time.Time
	_ = h.db.QueryRow(ctx, `SELECT run_at FROM send_queue WHERE campaign_id = $1`, h.campaign.ID).Scan(&runAt)
	if c.Status != campaigns.StatusScheduled || !c.SendAt.Equal(later) || !runAt.Equal(later) {
		t.Errorf("moved to %v (%v), runs at %v", c.SendAt, c.SendAt.Location(), runAt)
	}
	var re *RescheduleError
	for _, bad := range []time.Time{h.now, h.now.Add(-time.Minute), h.now.AddDate(1, 0, 1)} {
		if _, err := h.svc.Reschedule(ctx, h.acc, h.campaign.ID, bad); !errors.As(err, &re) {
			t.Errorf("%v: %v", bad, err)
		}
	}
	// At the old time nothing starts; at the new one it does.
	h.now = at
	if ids, _ := StartDue(ctx, h.db, h.now); len(ids) != 0 {
		t.Errorf("started at the old time")
	}
	h.now = later
	if ids, _ := StartDue(ctx, h.db, h.now); len(ids) != 1 {
		t.Errorf("not started at the new time")
	}
}

func TestStartDueOnce(t *testing.T) {
	h, at := scheduledHarness(t)
	ctx := context.Background()
	if ids, err := StartDue(ctx, h.db, at.Add(-time.Second)); err != nil || len(ids) != 0 {
		t.Fatalf("early: %v %v", ids, err)
	}
	// Several workers at once: exactly one starts it.
	var mu sync.Mutex
	started := 0
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ids, err := StartDue(ctx, h.db, at)
			if err != nil {
				t.Error(err)
			}
			mu.Lock()
			started += len(ids)
			mu.Unlock()
		}()
	}
	wg.Wait()
	var changed time.Time
	_ = h.db.QueryRow(ctx, `SELECT status_changed_at FROM campaigns WHERE id = $1`, h.campaign.ID).Scan(&changed)
	if started != 1 || h.status(t) != campaigns.StatusSending || !changed.Equal(at) {
		t.Errorf("started %d times, status %s at %v", started, h.status(t), changed)
	}
	// The dispatcher then batches it.
	q := &recorded{}
	h.now = at
	dispatch(t, h.pipeline(fastLimits), q)
	if b := q.take(); len(b) != 1 || len(b[0].IDs) != 3 {
		t.Errorf("batches %+v", b)
	}
}

// A reschedule that commits while StartDue waits on the campaign wins: the
// campaign stays scheduled for its new time (from the MG-32 review).
func TestStartDueWaitsOutReschedule(t *testing.T) {
	h, at := scheduledHarness(t)
	ctx := context.Background()
	later := at.Add(24 * time.Hour)
	// A reschedule in flight: it holds the queue row and the campaign, the
	// order Reschedule takes them, and hasn't committed yet.
	tx, err := h.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var pid int
	if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE send_queue SET run_at = $2 WHERE campaign_id = $1`, h.campaign.ID, later); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE campaigns SET send_at = $2 WHERE id = $1`, h.campaign.ID, later); err != nil {
		t.Fatal(err)
	}
	// The scheduler comes round at the old time and waits on the campaign.
	type result struct {
		ids []string
		err error
	}
	done := make(chan result, 1)
	go func() {
		ids, err := StartDue(ctx, h.db, at)
		done <- result{ids, err}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting int
		if err := h.db.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid))`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("StartDue never waited on the reschedule")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	r := <-done
	if r.err != nil || len(r.ids) != 0 || h.status(t) != campaigns.StatusScheduled {
		t.Errorf("started %v (%v); status %s", r.ids, r.err, h.status(t))
	}
	if ids, _ := StartDue(ctx, h.db, later); len(ids) != 1 {
		t.Errorf("not started at the new time")
	}
}

func TestRunScheduler(t *testing.T) {
	h, _ := scheduledHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	// Due already, by the real clock.
	for _, q := range []string{
		`UPDATE send_queue SET run_at = now() - interval '1 second' WHERE campaign_id = $1`,
		`UPDATE campaigns SET send_at = now() - interval '1 second' WHERE id = $1`,
	} {
		if _, err := h.db.Exec(ctx, q, h.campaign.ID); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan struct{})
	go func() { defer close(done); RunScheduler(ctx, h.db, 10*time.Millisecond, quietLog()) }()
	deadline := time.Now().Add(5 * time.Second)
	for h.status(t) != campaigns.StatusSending && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if h.status(t) != campaigns.StatusSending {
		t.Errorf("status %s", h.status(t))
	}
}

func quietLog() *slog.Logger { return slog.New(slog.DiscardHandler) }
