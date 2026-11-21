package sending

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/campaigns"
	"github.com/gremlin-technologies/mailgoose/internal/mail"
)

// The send queue cap (MG-89). The dispatcher runs at one email a second,
// so its batches are 10 and a campaign may have 100 rows waiting; the
// worker sends without waiting.
var slowDispatch = Limits{PerSecond: 1, Daily: 10000, NewDaily: 10000}

// waiting counts the campaign's rows on the send queue: enqueued and not
// yet claimed, within RequeueAfter.
func (h *harness) waiting(t *testing.T) int {
	t.Helper()
	var n int
	if err := h.db.QueryRow(context.Background(), `
		SELECT count(*) FROM campaign_recipients
		WHERE campaign_id = $1 AND status = 'queued' AND enqueued_at > $2`,
		h.campaign.ID, h.now.Add(-RequeueAfter)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func idsOf(batches []Batch) []int64 {
	var out []int64
	for _, b := range batches {
		out = append(out, b.IDs...)
	}
	return out
}

func handle(t *testing.T, p *Pipeline, batches ...Batch) {
	t.Helper()
	for _, b := range batches {
		if err := p.HandleBatch(context.Background(), b, 1); err != nil {
			t.Fatal(err)
		}
	}
}

// A big campaign has at most MaxWaitingBatches batches waiting, topped up
// in whole batches as the worker sends them, until every copy is out once.
func TestPipelineQueueCap(t *testing.T) {
	h := launchedHarness(t, 249)
	d := h.pipeline(slowDispatch)
	w := h.pipeline(fastLimits)
	q := &recorded{}
	if d.BatchSize() != 10 {
		t.Fatalf("batch size %d", d.BatchSize())
	}

	dispatch(t, d, q)
	first := q.take()
	if len(first) != MaxWaitingBatches || len(idsOf(first)) != 100 || h.waiting(t) != 100 {
		t.Fatalf("%d batches, %d rows, %d waiting", len(first), len(idsOf(first)), h.waiting(t))
	}
	// Full: the next ticks add nothing.
	dispatch(t, d, q)
	dispatch(t, d, q)
	if got := q.take(); len(got) != 0 {
		t.Fatalf("batched past the cap: %d", len(got))
	}

	// Half a batch sent isn't room for a whole one, so nothing is added.
	half := first[0]
	half.IDs = half.IDs[:5]
	handle(t, w, half)
	dispatch(t, d, q)
	if got := q.take(); len(got) != 0 {
		t.Fatalf("topped up with a part batch: %+v", got)
	}
	// The rest of that batch makes room for one more.
	handle(t, w, first[0])
	dispatch(t, d, q)
	if got := q.take(); len(got) != 1 || len(got[0].IDs) != 10 || got[0].IDs[0] <= first[len(first)-1].IDs[9] {
		t.Fatalf("top-up %+v", got)
	} else {
		first = append(first[1:], got...)
	}

	// The worker drains, the dispatcher tops up, and the cap holds to the
	// end, the last batch short.
	pending := first
	for range 100 {
		if n := h.waiting(t); n > MaxWaitingBatches*d.BatchSize() {
			t.Fatalf("%d waiting", n)
		}
		if len(pending) == 0 {
			break
		}
		k := min(3, len(pending))
		handle(t, w, pending[:k]...)
		pending = pending[k:]
		dispatch(t, d, q)
		pending = append(pending, q.take()...)
	}
	dispatch(t, d, q)
	sent := h.fake.Sent()
	if len(sent) != 250 || len(perAddress(sent)) != 250 || h.status(t) != campaigns.StatusSent {
		t.Errorf("%d sent to %d addresses, status %s", len(sent), len(perAddress(sent)), h.status(t))
	}
}

// Batches that never came are taken again after RequeueAfter, as before:
// the cap doesn't count them once they're due.
func TestPipelineQueueCapRequeue(t *testing.T) {
	h := launchedHarness(t, 249)
	d := h.pipeline(slowDispatch)
	q := &recorded{}
	dispatch(t, d, q)
	lost := idsOf(q.take())
	if len(lost) != 100 {
		t.Fatalf("%d enqueued", len(lost))
	}

	h.now = h.now.Add(RequeueAfter - time.Second)
	dispatch(t, d, q)
	if got := q.take(); len(got) != 0 {
		t.Fatalf("batched before RequeueAfter: %d", len(got))
	}
	h.now = h.now.Add(time.Second)
	dispatch(t, d, q)
	again := idsOf(q.take())
	if len(again) != 100 || again[0] != lost[0] || again[99] != lost[99] {
		t.Fatalf("requeued %d rows, %v..., want the lost ones", len(again), again[:min(3, len(again))])
	}
	if h.waiting(t) != 100 {
		t.Errorf("%d waiting", h.waiting(t))
	}
}

// Rows held back after SES asked to retry later (MG-87) count against the
// cap while they wait, so no more copies go to a throttled SES, and stop
// counting when they're due, so they're batched again a minute later,
// first, and never counted twice.
func TestPipelineQueueCapRetryLater(t *testing.T) {
	h := launchedHarness(t, 249)
	d := h.pipeline(slowDispatch)
	w := h.pipeline(fastLimits)
	stub := &sesStub{ok: 3, err: retryLaterErrors["too many requests"]}
	w.Mail = mail.SES{Client: stub, ConfigurationSet: "cs"}
	q := &recorded{}

	// The first batch sends 3 and is throttled: its other 7 wait.
	dispatch(t, d, q)
	first := q.take()
	handle(t, w, first[0])
	held := first[0].IDs[3:]
	if c := h.counts(t); c["sent"] != 3 || h.waiting(t) != 97 {
		t.Fatalf("after the throttle: %v, %d waiting", c, h.waiting(t))
	}
	// Every other batch is throttled too, on its first copy.
	handle(t, w, first[1:]...)
	if c := h.counts(t); c["sent"] != 3 || h.waiting(t) != 97 {
		t.Fatalf("all throttled: %v, %d waiting", c, h.waiting(t))
	}
	// Within the minute nothing more is batched: the held rows fill the cap.
	for range 3 {
		h.now = h.now.Add(15 * time.Second)
		dispatch(t, d, q)
	}
	if got := q.take(); len(got) != 0 {
		t.Fatalf("batched while SES throttles: %d", len(got))
	}

	// A minute on, SES accepts again. The held rows are due: they don't
	// count any more, and they're the batches the dispatcher takes.
	stub.set(0, nil)
	h.now = h.now.Add(RetryLaterAfter - 45*time.Second)
	if n := h.waiting(t); n != 0 {
		t.Fatalf("%d still counted once due", n)
	}
	dispatch(t, d, q)
	retry := q.take()
	ids := idsOf(retry)
	if len(retry) != MaxWaitingBatches || len(ids) != 100 || ids[0] != held[0] {
		t.Fatalf("after the wait: %d batches, %d rows, first %v (held from %d)", len(retry), len(ids), ids[:min(1, len(ids))], held[0])
	}
	got := map[int64]bool{}
	for _, id := range ids {
		got[id] = true
	}
	for _, id := range idsOf(first)[3:] {
		if !got[id] {
			t.Fatalf("held row %d not batched again", id)
		}
	}

	// The campaign then goes out whole, each copy once.
	pending := retry
	for range 100 {
		if len(pending) == 0 {
			break
		}
		handle(t, w, pending...)
		dispatch(t, d, q)
		pending = q.take()
	}
	dispatch(t, d, q)
	if c := h.counts(t); c["sent"] != 250 || h.status(t) != campaigns.StatusSent {
		t.Errorf("%v, status %s", c, h.status(t))
	}
	for addr, n := range stub.perTo() {
		if n != 1 {
			t.Errorf("%s got %d copies", addr, n)
		}
	}
}

// The cap sits under the account's daily limit and the platform cap: the
// smallest room wins.
func TestPipelineQueueCapWithDailyLimits(t *testing.T) {
	h := launchedHarness(t, 249)
	if _, err := h.db.Exec(context.Background(), `UPDATE accounts SET daily_send_limit = 150 WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	limits := slowDispatch
	limits.PlatformDaily = 130
	d := h.pipeline(limits)
	w := h.pipeline(fastLimits)
	q := &recorded{}

	dispatch(t, d, q)
	if n := len(idsOf(q.batches)); n != 100 {
		t.Fatalf("first tick %d, want the cap's 100", n)
	}
	handle(t, w, q.take()...)
	dispatch(t, d, q)
	if n := len(idsOf(q.batches)); n != 30 {
		t.Fatalf("second tick %d, want the platform's 30 left", n)
	}
	handle(t, w, q.take()...)
	dispatch(t, d, q)
	if got := q.take(); len(got) != 0 {
		t.Fatalf("batched past the platform cap: %d", len(idsOf(got)))
	}

	// With the platform cap lifted, the account's limit is the lower.
	d.Limits.PlatformDaily = 0
	dispatch(t, d, q)
	if n := len(idsOf(q.batches)); n != 20 {
		t.Fatalf("%d batched, want the account's 20 left", n)
	}
	handle(t, w, q.take()...)
	if c := h.counts(t); c["sent"] != 150 || c["queued"] != 100 || h.status(t) != campaigns.StatusSending {
		t.Errorf("%v, status %s", c, h.status(t))
	}
}

// A slice bigger than the cap goes out in full before the rest waits:
// a full queue isn't taken for a slice that's all out.
func TestPipelineQueueCapSlice(t *testing.T) {
	h := slicedHarness(t, 2999, time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC), 0)
	d := h.pipeline(slowDispatch)
	w := h.pipeline(fastLimits)
	q := &recorded{}

	dispatch(t, d, q)
	pending := q.take()
	if len(idsOf(pending)) != 100 {
		t.Fatalf("%d batched", len(idsOf(pending)))
	}
	// A full queue batches nothing, and that isn't the slice being out.
	dispatch(t, d, q)
	if got := q.take(); len(got) != 0 {
		t.Fatalf("batched past the cap: %d", len(got))
	}
	for range 100 {
		if len(pending) == 0 {
			break
		}
		if sl, _ := h.slice(t); sl.State != SliceSending {
			t.Fatalf("slice %s with %d sent", sl.State, len(h.fake.Sent()))
		}
		handle(t, w, pending[0])
		pending = pending[1:]
		dispatch(t, d, q)
		pending = append(pending, q.take()...)
	}
	var outside int
	if err := h.db.QueryRow(context.Background(), `
		SELECT count(*) FROM campaign_recipients WHERE campaign_id = $1 AND status = 'sent' AND NOT in_slice`,
		h.campaign.ID).Scan(&outside); err != nil {
		t.Fatal(err)
	}
	sl, in := h.slice(t)
	if len(h.fake.Sent()) != in || in != 300 || outside != 0 || sl.State != SliceHold {
		t.Errorf("%d sent of a slice of %d, %d outside it, state %s", len(h.fake.Sent()), in, outside, sl.State)
	}
}

// Workers drain a capped campaign at the configured rate: the dispatcher
// tops the queue up faster than they empty it, so they never wait for
// work, and the cap holds throughout.
func TestPipelineQueueCapKeepsRate(t *testing.T) {
	const n, rate = 600, 100
	h := launchedHarness(t, n-1)
	d := h.pipeline(slowDispatch) // 10 batches of 10: a second of sending
	limits := fastLimits
	limits.PerSecond = rate
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	jobs := make(chan Batch, 2*MaxWaitingBatches)
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := h.pipeline(limits)
			for b := range jobs {
				if err := w.HandleBatch(ctx, b, 1); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	q := &recorded{}
	most := 0
	start := time.Now()
	for h.status(t) == campaigns.StatusSending && time.Since(start) < time.Minute {
		dispatch(t, d, q)
		most = max(most, h.waiting(t))
		for _, b := range q.take() {
			jobs <- b
		}
		time.Sleep(50 * time.Millisecond)
	}
	close(jobs)
	wg.Wait()
	took := time.Since(start)

	sent := h.fake.Sent()
	if len(sent) != n || len(perAddress(sent)) != n || h.status(t) != campaigns.StatusSent {
		t.Fatalf("%d sent to %d addresses, status %s", len(sent), len(perAddress(sent)), h.status(t))
	}
	if most > MaxWaitingBatches*d.BatchSize() {
		t.Errorf("%d waiting at most", most)
	}
	// 600 at 100 a second is 6 seconds; allow for a slow machine, but not
	// for workers left idle by the cap.
	t.Logf("queue cap: %d in %v (%.1f a second), at most %d waiting", n, took.Round(time.Millisecond), float64(n)/took.Seconds(), most)
	if took > 12*time.Second {
		t.Errorf("took %v, want about 6s at %d a second", took, rate)
	}
}
