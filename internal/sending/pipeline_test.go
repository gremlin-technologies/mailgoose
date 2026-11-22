package sending

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gremlin-technologies/mailgoose/internal/campaigns"
	"github.com/gremlin-technologies/mailgoose/internal/mail"
	"github.com/gremlin-technologies/mailgoose/internal/unsubscribe"
)

// fastLimits sends without waiting, for tests that aren't about the rate.
var fastLimits = Limits{PerSecond: 100000, Daily: 10000, NewDaily: 1000}

// recorded is a queue that keeps the batches it's given.
type recorded struct {
	mu      sync.Mutex
	batches []Batch
}

func (r *recorded) Enqueue(_ context.Context, b []Batch) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.batches = append(r.batches, b...)
	return nil
}

func (r *recorded) take() []Batch {
	r.mu.Lock()
	defer r.mu.Unlock()
	b := r.batches
	r.batches = nil
	return b
}

// launchedHarness is a harness whose campaign has gone out to Sam and n more
// subscribers, with an established account: past its first month and its
// first campaigns, so it isn't sliced (MG-62).
func launchedHarness(t *testing.T, n int) *harness {
	t.Helper()
	ctx := context.Background()
	h := newHarness(t)
	if _, err := h.db.Exec(ctx, `
		INSERT INTO campaigns (account_id, name, all_contacts, status, launched_at)
		SELECT $1, 'Earlier ' || i, true, 'sent', $2 FROM generate_series(1, $3) i`,
		h.acc, h.now.AddDate(0, 0, -20), SliceCampaigns); err != nil {
		t.Fatal(err)
	}
	return launchTo(t, h, n, h.now.AddDate(0, -1, 0))
}

// launchTo launches h's campaign to Sam and n more subscribers, with the
// account made at created.
func launchTo(t *testing.T, h *harness, n int, created time.Time) *harness {
	t.Helper()
	ctx := context.Background()
	h.buy(t, n+10)
	if _, err := h.db.Exec(ctx, `UPDATE accounts SET created_at = $2 WHERE id = $1`, h.acc, created); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(ctx, `
		INSERT INTO contacts (account_id, email, first_name, status, source)
		SELECT $1, 'reader' || i || '@example.com', 'Reader' || i, 'subscribed', 'api' FROM generate_series(1, $2) i`,
		h.acc, n); err != nil {
		t.Fatal(err)
	}
	if _, err := h.test(); err != nil {
		t.Fatal(err)
	}
	if _, err := h.launch(h.summary(t).Code); err != nil {
		t.Fatal(err)
	}
	h.fake.Reset()
	return h
}

func (h *harness) pipeline(limits Limits) *Pipeline {
	return &Pipeline{DB: h.db, Mail: h.fake, Unsubscribe: &unsubscribe.Service{DB: h.db}, BaseURL: baseURL + "/",
		Limits: limits, Now: func() time.Time { return h.now }}
}

// counts is how many of the campaign's rows are in each status.
func (h *harness) counts(t *testing.T) map[string]int {
	t.Helper()
	rows, err := h.db.Query(context.Background(),
		`SELECT status, count(*) FROM campaign_recipients WHERE campaign_id = $1 GROUP BY status`, h.campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			t.Fatal(err)
		}
		out[s] = n
	}
	return out
}

func (h *harness) status(t *testing.T) string {
	t.Helper()
	var s string
	if err := h.db.QueryRow(context.Background(), `SELECT status FROM campaigns WHERE id = $1`, h.campaign.ID).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// perAddress counts the copies each address got.
func perAddress(sent []mail.Sent) map[string]int {
	out := map[string]int{}
	for _, s := range sent {
		out[s.Message.To.Email]++
	}
	return out
}

func dispatch(t *testing.T, p *Pipeline, q Queue) {
	t.Helper()
	if err := p.Dispatch(context.Background(), q); err != nil {
		t.Fatal(err)
	}
}

func TestPipelineSendsCampaign(t *testing.T) {
	h := launchedHarness(t, 149)
	ctx := context.Background()
	p := h.pipeline(Limits{PerSecond: 5, Daily: 10000, NewDaily: 1000})
	if p.BatchSize() != 50 {
		t.Errorf("batch size %d at 5 a second", p.BatchSize())
	}
	p.Limits = fastLimits
	q := &recorded{}
	dispatch(t, p, q)
	batches := q.take()
	if len(batches) != 2 || len(batches[0].IDs) != MaxBatch || len(batches[1].IDs) != 50 ||
		batches[0].CampaignID != h.campaign.ID || batches[0].AccountID != h.acc {
		t.Fatalf("batches %d: %+v", len(batches), batches[0])
	}
	// Enqueued rows aren't batched again while they wait.
	dispatch(t, p, q)
	if b := q.take(); len(b) != 0 {
		t.Errorf("batched again: %d", len(b))
	}
	for _, b := range batches {
		if err := p.HandleBatch(ctx, b, 1); err != nil {
			t.Fatal(err)
		}
	}
	sent := h.fake.Sent()
	if len(sent) != 150 || len(perAddress(sent)) != 150 {
		t.Fatalf("sent %d to %d addresses", len(sent), len(perAddress(sent)))
	}
	var sam mail.Message
	for _, s := range sent {
		if s.Message.To.Email == "sam@patsbakery.com" {
			sam = s.Message
		}
	}
	if sam.Subject != "Fresh bread, Sam" || !strings.Contains(sam.HTML, "Hi Sam") || sam.From.Email != "hello@patsbakery.com" ||
		sam.Kind != mail.KindCampaign || sam.CampaignID != h.campaign.ID || sam.AccountID != h.acc {
		t.Errorf("Sam's copy: %+v", sam)
	}
	headers := map[string]string{}
	for _, x := range sam.Headers {
		headers[x.Name] = x.Value
	}
	if !strings.HasPrefix(headers["List-Unsubscribe"], "<"+baseURL+"/u/") || headers["List-Unsubscribe-Post"] != "List-Unsubscribe=One-Click" {
		t.Errorf("headers %v", headers)
	}
	var withID int
	_ = h.db.QueryRow(ctx, `SELECT count(*) FROM campaign_recipients WHERE campaign_id = $1 AND message_id LIKE 'fake-%' AND sent_at = $2`,
		h.campaign.ID, h.now).Scan(&withID)
	if withID != 150 {
		t.Errorf("%d rows with a message id", withID)
	}

	// A batch delivered again sends nothing more.
	if err := p.HandleBatch(ctx, batches[0], 2); err != nil {
		t.Fatal(err)
	}
	if len(h.fake.Sent()) != 150 {
		t.Errorf("redelivery sent %d", len(h.fake.Sent())-150)
	}

	// The next pass finishes the campaign; the one after has nothing to do.
	if h.status(t) != campaigns.StatusSending {
		t.Errorf("status %s before the finishing pass", h.status(t))
	}
	h.now = h.now.Add(time.Minute)
	dispatch(t, p, q)
	var changed time.Time
	var done *time.Time
	_ = h.db.QueryRow(ctx, `SELECT c.status_changed_at, q.done_at FROM campaigns c JOIN send_queue q ON q.campaign_id = c.id WHERE c.id = $1`,
		h.campaign.ID).Scan(&changed, &done)
	if h.status(t) != campaigns.StatusSent || !changed.Equal(h.now) || done == nil {
		t.Errorf("status %s changed %v done %v", h.status(t), changed, done)
	}
	if h.balance(t) != 9 {
		t.Errorf("balance %d: 150 credits should stay spent", h.balance(t))
	}
	dispatch(t, p, q)
	if len(q.take()) != 0 {
		t.Error("a finished campaign was batched")
	}
}

func TestPipelineSkipsWhoLeft(t *testing.T) {
	h := launchedHarness(t, 3)
	ctx := context.Background()
	p := h.pipeline(fastLimits)
	q := &recorded{}
	dispatch(t, p, q)
	// After launch, one reader unsubscribes and one is deleted.
	for _, sql := range []string{
		`UPDATE contacts SET status = 'unsubscribed' WHERE account_id = $1 AND email = 'reader1@example.com'`,
		`DELETE FROM contacts WHERE account_id = $1 AND email = 'reader2@example.com'`,
	} {
		if _, err := h.db.Exec(ctx, sql, h.acc); err != nil {
			t.Fatal(err)
		}
	}
	before := h.balance(t)
	for _, b := range q.take() {
		if err := p.HandleBatch(ctx, b, 1); err != nil {
			t.Fatal(err)
		}
	}
	got := perAddress(h.fake.Sent())
	if len(got) != 2 || got["sam@patsbakery.com"] != 1 || got["reader3@example.com"] != 1 {
		t.Errorf("sent to %v", got)
	}
	if c := h.counts(t); c["sent"] != 2 || c["skipped"] != 2 {
		t.Errorf("counts %v", c)
	}
	if h.balance(t) != before+2 {
		t.Errorf("balance %d, want %d: two credits back", h.balance(t), before+2)
	}
	dispatch(t, p, q)
	if h.status(t) != campaigns.StatusSent {
		t.Errorf("status %s", h.status(t))
	}
}

// dying sends n copies, then stops its goroutine in the middle of the
// next send, as a worker killed mid-batch would. The copy it was sending
// may or may not have gone out; it's recorded as gone.
type dying struct {
	mail.Fake
	left int
}

func (d *dying) Send(ctx context.Context, m mail.Message) (string, error) {
	id, err := d.Fake.Send(ctx, m)
	if d.left--; d.left < 0 {
		runtime.Goexit()
	}
	return id, err
}

func TestPipelineWorkerKilledMidBatch(t *testing.T) {
	h := launchedHarness(t, 9)
	ctx := context.Background()
	p := h.pipeline(fastLimits)
	q := &recorded{}
	dispatch(t, p, q)
	batches := q.take()
	if len(batches) != 1 || len(batches[0].IDs) != 10 {
		t.Fatalf("batches %+v", batches)
	}

	d := &dying{left: 3}
	p.Mail = d
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = p.HandleBatch(ctx, batches[0], 1)
		t.Error("the worker wasn't killed")
	}()
	<-done
	if n := len(d.Sent()); n != 4 {
		t.Fatalf("killed worker sent %d", n)
	}
	if c := h.counts(t); c["sent"] != 3 || c["sending"] != 1 || c["queued"] != 6 {
		t.Fatalf("after the kill: %v", c)
	}

	// SQS delivers the batch again, to another worker.
	p.Mail = &d.Fake
	if err := p.HandleBatch(ctx, batches[0], 2); err != nil {
		t.Fatal(err)
	}
	got := perAddress(d.Sent())
	if len(got) != 10 {
		t.Errorf("%d addresses got mail", len(got))
	}
	for addr, n := range got {
		if n != 1 {
			t.Errorf("%s got %d copies", addr, n)
		}
	}
	if c := h.counts(t); c["sent"] != 9 || c["sending"] != 1 {
		t.Errorf("after the retry: %v", c)
	}

	// The interrupted row may have gone out, so it's never sent again: once
	// its claim is old, it ends failed with its credit back, and the
	// campaign finishes.
	before := h.balance(t)
	dispatch(t, p, q)
	if h.status(t) != campaigns.StatusSending {
		t.Errorf("finished with a claim still fresh: %s", h.status(t))
	}
	h.now = h.now.Add(ClaimTimeout)
	dispatch(t, p, q)
	if c := h.counts(t); c["sent"] != 9 || c["failed"] != 1 {
		t.Errorf("after the timeout: %v", c)
	}
	if h.balance(t) != before+1 || h.status(t) != campaigns.StatusSent || len(q.take()) != 0 || len(d.Sent()) != 10 {
		t.Errorf("balance %d (was %d), status %s, %d sent", h.balance(t), before, h.status(t), len(d.Sent()))
	}
}

func TestPipelineRetriesThenFails(t *testing.T) {
	h := launchedHarness(t, 2)
	ctx := context.Background()
	p := h.pipeline(fastLimits)
	q := &recorded{}
	dispatch(t, p, q)
	b := q.take()[0]
	h.fake.Err = errors.New("SES: throttled")

	// Early tries hand the rows back for the next one.
	for attempt := 1; attempt < MaxAttempts; attempt++ {
		var sf *sendFailed
		if err := p.HandleBatch(ctx, b, attempt); !errors.As(err, &sf) {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		if c := h.counts(t); c["queued"] != 3 {
			t.Fatalf("attempt %d: %v", attempt, c)
		}
	}
	var lastError string
	_ = h.db.QueryRow(ctx, `SELECT error FROM campaign_recipients WHERE campaign_id = $1 AND error IS NOT NULL LIMIT 1`, h.campaign.ID).Scan(&lastError)
	if lastError != "SES: throttled" {
		t.Errorf("error kept %q", lastError)
	}

	// The last try marks every row failed with its credit back, and still
	// fails, so SQS moves the message to the dead-letter queue.
	before := h.balance(t)
	if err := p.HandleBatch(ctx, b, MaxAttempts); err == nil || !strings.Contains(err.Error(), "after 5 attempts") {
		t.Fatalf("last attempt: %v", err)
	}
	if c := h.counts(t); c["failed"] != 3 {
		t.Errorf("counts %v", c)
	}
	if h.balance(t) != before+3 {
		t.Errorf("balance %d, want %d", h.balance(t), before+3)
	}
	dispatch(t, p, q)
	if h.status(t) != campaigns.StatusFailed || len(q.take()) != 0 {
		t.Errorf("status %s", h.status(t))
	}
}

func TestPipelineSQSHandler(t *testing.T) {
	h := launchedHarness(t, 1)
	p := h.pipeline(fastLimits)
	q := &recorded{}
	dispatch(t, p, q)
	b := q.take()[0]
	h.fake.Err = errors.New("SES: down")
	handle := p.SQSHandler()
	body := fmt.Sprintf(`{"account_id":%q,"campaign_id":%q,"ids":[%d,%d]}`, b.AccountID, b.CampaignID, b.IDs[0], b.IDs[1])
	message := func(count int) types.Message {
		return types.Message{MessageId: aws.String("m"), Body: aws.String(body),
			Attributes: map[string]string{"ApproximateReceiveCount": strconv.Itoa(count)}}
	}
	if err := handle(context.Background(), message(1)); err == nil {
		t.Error("first receive succeeded")
	}
	if c := h.counts(t); c["queued"] != 2 {
		t.Errorf("after the first receive: %v", c)
	}
	if err := handle(context.Background(), message(5)); err == nil {
		t.Error("fifth receive succeeded")
	}
	if c := h.counts(t); c["failed"] != 2 {
		t.Errorf("after the fifth receive: %v", c)
	}
	// A message that isn't a batch is dropped, not retried.
	if err := handle(context.Background(), types.Message{MessageId: aws.String("x"), Body: aws.String("hello")}); err != nil {
		t.Errorf("junk: %v", err)
	}
}

func TestPipelineDailyLimit(t *testing.T) {
	h := launchedHarness(t, 29)
	ctx := context.Background()
	p := h.pipeline(Limits{PerSecond: 100000, Daily: 15, NewDaily: 10})
	q := &recorded{}
	sendAll := func() int {
		n := 0
		for _, b := range q.take() {
			n += len(b.IDs)
			if err := p.HandleBatch(ctx, b, 1); err != nil {
				t.Fatal(err)
			}
		}
		return n
	}

	// A new account gets the new-account limit.
	if _, err := h.db.Exec(ctx, `UPDATE accounts SET created_at = $2 WHERE id = $1`, h.acc, h.now.AddDate(0, 0, -6)); err != nil {
		t.Fatal(err)
	}
	dispatch(t, p, q)
	if n := sendAll(); n != 10 {
		t.Fatalf("new account: %d batched, want 10", n)
	}
	dispatch(t, p, q)
	if n := sendAll(); n != 0 {
		t.Errorf("batched %d past the limit", n)
	}

	// Past its first week, the usual limit counts the last 24 hours.
	h.now = h.now.AddDate(0, 0, 1)
	dispatch(t, p, q)
	if n := sendAll(); n != 15 {
		t.Errorf("a day later: %d batched, want 15 (the 10 sent are a day old)", n)
	}

	// The account's own limit wins, and rows waiting in a batch count.
	if _, err := h.db.Exec(ctx, `UPDATE accounts SET daily_send_limit = 17 WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	dispatch(t, p, q)
	dispatch(t, p, q)
	if n := sendAll(); n != 2 {
		t.Errorf("own limit: %d batched, want 2", n)
	}
	if c := h.counts(t); len(h.fake.Sent()) != 27 || c["queued"] != 3 || h.status(t) != campaigns.StatusSending {
		t.Errorf("%d sent, %v, status %s", len(h.fake.Sent()), c, h.status(t))
	}
	// A paused account sends nothing.
	if _, err := h.db.Exec(ctx, `UPDATE accounts SET sending_paused_at = $2, daily_send_limit = NULL WHERE id = $1`, h.acc, h.now); err != nil {
		t.Fatal(err)
	}
	h.now = h.now.AddDate(0, 0, 2)
	dispatch(t, p, q)
	if len(q.take()) != 0 || h.status(t) != campaigns.StatusSending {
		t.Errorf("paused account batched, status %s", h.status(t))
	}
}

func TestPipelinePausedCampaign(t *testing.T) {
	h := launchedHarness(t, 4)
	ctx := context.Background()
	p := h.pipeline(fastLimits)
	q := &recorded{}
	dispatch(t, p, q)
	b := q.take()[0]
	if _, err := h.db.Exec(ctx, `UPDATE campaigns SET status = 'paused' WHERE id = $1`, h.campaign.ID); err != nil {
		t.Fatal(err)
	}
	// A batch for a paused campaign sends nothing and hands its rows back.
	if err := p.HandleBatch(ctx, b, 1); err != nil {
		t.Fatal(err)
	}
	var waiting int
	_ = h.db.QueryRow(ctx, `SELECT count(*) FROM campaign_recipients WHERE campaign_id = $1 AND enqueued_at IS NULL AND status = 'queued'`,
		h.campaign.ID).Scan(&waiting)
	if len(h.fake.Sent()) != 0 || waiting != 5 {
		t.Errorf("sent %d, %d rows back", len(h.fake.Sent()), waiting)
	}
	dispatch(t, p, q)
	if len(q.take()) != 0 {
		t.Error("batched a paused campaign")
	}
	// Resumed, it goes out.
	if _, err := h.db.Exec(ctx, `UPDATE campaigns SET status = 'sending' WHERE id = $1`, h.campaign.ID); err != nil {
		t.Fatal(err)
	}
	dispatch(t, p, q)
	for _, b := range q.take() {
		if err := p.HandleBatch(ctx, b, 1); err != nil {
			t.Fatal(err)
		}
	}
	if len(h.fake.Sent()) != 5 {
		t.Errorf("sent %d after resuming", len(h.fake.Sent()))
	}
	// A cancelled campaign's queue row is closed.
	if _, err := h.db.Exec(ctx, `UPDATE campaigns SET status = 'cancelled' WHERE id = $1`, h.campaign.ID); err != nil {
		t.Fatal(err)
	}
	dispatch(t, p, q)
	var done *time.Time
	_ = h.db.QueryRow(ctx, `SELECT done_at FROM send_queue WHERE campaign_id = $1`, h.campaign.ID).Scan(&done)
	if done == nil {
		t.Error("cancelled campaign still queued")
	}
}

func TestPipelineStartsScheduled(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.buy(t, 10)
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
	p := h.pipeline(fastLimits)
	q := &recorded{}
	dispatch(t, p, q)
	if len(q.take()) != 0 || h.status(t) != campaigns.StatusScheduled {
		t.Errorf("started early: %s", h.status(t))
	}
	h.now = at
	dispatch(t, p, q)
	if b := q.take(); len(b) != 1 || h.status(t) != campaigns.StatusSending {
		t.Errorf("at its time: %d batches, status %s", len(b), h.status(t))
	}
}

func TestPipelineGlobalRate(t *testing.T) {
	h := launchedHarness(t, 11)
	ctx := context.Background()
	p := h.pipeline(Limits{PerSecond: 20, Daily: 10000, NewDaily: 1000})
	q := &recorded{}
	dispatch(t, p, q)
	b := q.take()[0]
	// Two workers share the rate: 12 copies at 20 a second take at least
	// 11 gaps of 50ms.
	half := len(b.IDs) / 2
	start := time.Now()
	var wg sync.WaitGroup
	for _, ids := range [][]int64{b.IDs[:half], b.IDs[half:]} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := h.pipeline(p.Limits).HandleBatch(ctx, Batch{AccountID: b.AccountID, CampaignID: b.CampaignID, IDs: ids}, 1); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	took := time.Since(start)
	sent := h.fake.Sent()
	if len(sent) != 12 || took < 500*time.Millisecond {
		t.Errorf("%d sent in %v", len(sent), took)
	}
	// The fake stamps a copy when it's handed over, after the work that
	// follows its slot, so one copy can land a little late and the next on
	// time. Pairs are too close to tell the rate from that. Every eight
	// slots are 400ms apart when the rate is shared, and 200ms when each
	// worker keeps its own: 300ms tells them apart with room for a copy up
	// to 100ms late, as one can be on a busy CI runner.
	const span = 8
	for i := span; i < len(sent); i++ {
		if gap := sent[i].At.Sub(sent[i-span].At); gap < 300*time.Millisecond {
			t.Errorf("copies %d and %d %v apart", i-span, i, gap)
		}
	}
}

// fakeBatchSQS records SendMessageBatch calls.
type fakeBatchSQS struct {
	calls  [][]string
	failed bool
}

func (f *fakeBatchSQS) SendMessageBatch(_ context.Context, in *sqs.SendMessageBatchInput, _ ...func(*sqs.Options)) (*sqs.SendMessageBatchOutput, error) {
	var bodies []string
	for _, e := range in.Entries {
		bodies = append(bodies, aws.ToString(e.MessageBody))
	}
	f.calls = append(f.calls, bodies)
	if f.failed {
		return &sqs.SendMessageBatchOutput{Failed: []types.BatchResultErrorEntry{{Id: aws.String("0"), Message: aws.String("nope")}}}, nil
	}
	return &sqs.SendMessageBatchOutput{}, nil
}

func TestSQSQueue(t *testing.T) {
	f := &fakeBatchSQS{}
	q := &SQSQueue{SQS: f, URL: "https://sqs/send"}
	var batches []Batch
	for i := range 23 {
		batches = append(batches, Batch{AccountID: "a", CampaignID: "c", IDs: []int64{int64(i)}})
	}
	if err := q.Enqueue(context.Background(), batches); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 3 || len(f.calls[0]) != 10 || len(f.calls[2]) != 3 ||
		f.calls[2][2] != `{"account_id":"a","campaign_id":"c","ids":[22]}` {
		t.Errorf("calls %v", f.calls)
	}
	f.failed = true
	if err := q.Enqueue(context.Background(), batches[:1]); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("failed entry: %v", err)
	}
}

func TestLocalQueue(t *testing.T) {
	h := launchedHarness(t, 30)
	p := h.pipeline(fastLimits)
	local := &LocalQueue{Pipeline: p}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go local.Run(ctx)
	go p.RunDispatcher(ctx, local, 20*time.Millisecond)
	deadline := time.Now().Add(10 * time.Second)
	for h.status(t) != campaigns.StatusSent && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if h.status(t) != campaigns.StatusSent || len(h.fake.Sent()) != 31 {
		t.Errorf("status %s, %d sent", h.status(t), len(h.fake.Sent()))
	}
}

// TestPipelineLoad sends a campaign to many recipients through several
// workers at the configured rate, and checks each gets exactly one copy.
// CI runs 500 at a fast rate; MAILGOOSE_LOAD_TEST=10000 runs the ticket's
// test, 10,000 recipients at SEND_RATE_PER_SECOND (default 10).
func TestPipelineLoad(t *testing.T) {
	n, rate := 500, 1000
	if v := os.Getenv("MAILGOOSE_LOAD_TEST"); v != "" {
		var err error
		if n, err = strconv.Atoi(v); err != nil {
			t.Fatal(err)
		}
		rate = 10
		if v := os.Getenv("SEND_RATE_PER_SECOND"); v != "" {
			if rate, err = strconv.Atoi(v); err != nil {
				t.Fatal(err)
			}
		}
	}
	h := launchedHarness(t, n-1)
	p := h.pipeline(Limits{PerSecond: rate, Daily: n, NewDaily: n})
	local := &recorded{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	start := time.Now()
	jobs := make(chan Batch)
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := h.pipeline(p.Limits)
			for b := range jobs {
				if err := w.HandleBatch(ctx, b, 1); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	for h.status(t) == campaigns.StatusSending {
		dispatch(t, p, local)
		for _, b := range local.take() {
			jobs <- b
		}
		time.Sleep(50 * time.Millisecond)
	}
	close(jobs)
	wg.Wait()
	took := time.Since(start)

	sent := h.fake.Sent()
	got := perAddress(sent)
	dupes := 0
	for _, c := range got {
		if c > 1 {
			dupes++
		}
	}
	if len(sent) != n || len(got) != n || dupes != 0 || h.status(t) != campaigns.StatusSent {
		t.Errorf("%d sent to %d addresses, %d duplicates, status %s", len(sent), len(got), dupes, h.status(t))
	}
	t.Logf("load: %d recipients through 3 workers at %d a second in %v (%.1f a second)", n, rate, took.Round(time.Millisecond),
		float64(n)/took.Seconds())
	if took > time.Hour {
		t.Errorf("took %v, over an hour", took)
	}
}

// A redelivered batch whose copies already went out takes no send slots,
// so it doesn't slow the other campaigns sharing the rate.
func TestPipelinePacesOnlyRealSends(t *testing.T) {
	h := launchedHarness(t, 3)
	ctx := context.Background()
	q := &recorded{}
	dispatch(t, h.pipeline(fastLimits), q)
	batches := q.take()
	for _, b := range batches {
		if err := h.pipeline(fastLimits).HandleBatch(ctx, b, 1); err != nil {
			t.Fatal(err)
		}
	}
	var before time.Time
	_ = h.db.QueryRow(ctx, `SELECT next_at FROM send_pace WHERE id = 1`).Scan(&before)

	// Again, at one a second: four rows already sent would take 3s if each
	// took a slot.
	slow := h.pipeline(Limits{PerSecond: 1, Daily: 10000, NewDaily: 1000})
	start := time.Now()
	for _, b := range batches {
		if err := slow.HandleBatch(ctx, b, 2); err != nil {
			t.Fatal(err)
		}
	}
	var after time.Time
	_ = h.db.QueryRow(ctx, `SELECT next_at FROM send_pace WHERE id = 1`).Scan(&after)
	if took := time.Since(start); took > time.Second || !after.Equal(before) {
		t.Errorf("redelivery took %v; pace moved from %v to %v", took, before, after)
	}
	if n := len(h.fake.Sent()); n != 4 {
		t.Errorf("%d sent", n)
	}
}

// recordFails makes recording a sent copy fail after the send, as a
// database blip would.
type recordFails struct {
	mail.Fake
	db interface {
		Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	}
}

func (r *recordFails) Send(ctx context.Context, m mail.Message) (string, error) {
	if _, err := r.db.Exec(ctx, `ALTER TABLE campaign_recipients ADD CONSTRAINT not_sent CHECK (status <> 'sent') NOT VALID`); err != nil {
		return "", err
	}
	return r.Fake.Send(ctx, m)
}

// A copy that went out but couldn't be recorded is logged with its message
// id, so it can be matched up.
func TestPipelineLogsUnrecordedSend(t *testing.T) {
	h := launchedHarness(t, 0)
	ctx := context.Background()
	q := &recorded{}
	p := h.pipeline(fastLimits)
	var logs strings.Builder
	p.Log = slog.New(slog.NewTextHandler(&logs, nil))
	sender := &recordFails{db: h.db}
	p.Mail = sender
	dispatch(t, p, q)
	for _, b := range q.take() {
		if err := p.HandleBatch(ctx, b, 1); err == nil {
			t.Error("no error")
		}
	}
	sent, ok := sender.Last()
	if !ok || !strings.Contains(logs.String(), "a sent copy wasn't recorded") ||
		!strings.Contains(logs.String(), "message_id="+sent.ID) || !strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("sent %v; logs:\n%s", ok, logs.String())
	}
}

// trackedCopy sends a campaign with two links to Sam, tracked or not, and
// returns Sam's copy.
func trackedCopy(t *testing.T, track bool) mail.Message {
	t.Helper()
	ctx := context.Background()
	h := newHarness(t)
	h.buy(t, 10)
	doc := `{"settings":{"subject":"Menu"},"blocks":[
		{"id":"a","type":"button","text":"Order","href":"https://patsbakery.com/order"},
		{"id":"b","type":"button","text":"Menu","href":"https://patsbakery.com/menu?day=fri&size=2"}]}`
	if _, err := h.db.Exec(ctx, `UPDATE emails SET document = $2 WHERE id = $1`, h.email.ID, doc); err != nil {
		t.Fatal(err)
	}
	if !track {
		off := false
		c, err := h.svc.Campaigns.Update(ctx, h.acc, h.campaign.ID, campaigns.Changes{Tracking: &off})
		if err != nil {
			t.Fatal(err)
		}
		h.campaign = c
	} else if !h.campaign.Tracking {
		t.Fatal("tracking isn't on by default")
	}
	if _, err := h.test(); err != nil {
		t.Fatal(err)
	}
	if test := h.fake.Sent(); len(test) != 1 || strings.Contains(test[0].Message.HTML, "/t/") {
		t.Fatalf("test send: %d copies, tracked %v", len(test), len(test) == 1)
	}
	if _, err := h.launch(h.summary(t).Code); err != nil {
		t.Fatal(err)
	}
	h.fake.Reset()
	p := h.pipeline(fastLimits)
	q := &recorded{}
	dispatch(t, p, q)
	for _, b := range q.take() {
		if err := p.HandleBatch(ctx, b, 1); err != nil {
			t.Fatal(err)
		}
	}
	sent := h.fake.Sent()
	if len(sent) != 1 {
		t.Fatalf("sent %d", len(sent))
	}
	m := sent[0].Message
	if strings.Contains(m.Text, "/t/") {
		t.Error("the plain-text part has tracked links")
	}
	unsub := ""
	for _, x := range m.Headers {
		if x.Name == "List-Unsubscribe" {
			unsub = strings.Trim(x.Value, "<>")
		}
	}
	if unsub == "" || !strings.Contains(m.HTML, `href="`+unsub+`"`) {
		t.Errorf("the unsubscribe link %q isn't in the copy as it is", unsub)
	}
	return m
}

func TestPipelineTracksOpensAndClicks(t *testing.T) {
	m := trackedCopy(t, true)
	if strings.Contains(m.HTML, `href="https://patsbakery.com/`) {
		t.Error("a link wasn't tracked")
	}
	if strings.Count(m.HTML, `href="`+baseURL+`/t/c/t1`) != 2 {
		t.Errorf("want two tracked links in:\n%s", m.HTML)
	}
	if strings.Count(m.HTML, `<img src="`+baseURL+`/t/o/t1`) != 1 {
		t.Errorf("want one open pixel in:\n%s", m.HTML)
	}
}

// TestPipelineTrackingOff is REP-3's check: a campaign with tracking off
// has no pixel and no rewritten links.
func TestPipelineTrackingOff(t *testing.T) {
	m := trackedCopy(t, false)
	if strings.Contains(m.HTML, "/t/o/") || strings.Contains(m.HTML, "/t/c/") {
		t.Errorf("tracking off, but the copy is tracked:\n%s", m.HTML)
	}
	if !strings.Contains(m.HTML, `href="https://patsbakery.com/order"`) ||
		!strings.Contains(m.HTML, `href="https://patsbakery.com/menu?day=fri&amp;size=2"`) {
		t.Errorf("links changed:\n%s", m.HTML)
	}
}
