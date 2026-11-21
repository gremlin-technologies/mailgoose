package sending

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	sestypes "github.com/aws/aws-sdk-go-v2/service/sesv2/types"

	"github.com/gremlin-technologies/mailgoose/internal/campaigns"
	"github.com/gremlin-technologies/mailgoose/internal/mail"
	"github.com/gremlin-technologies/mailgoose/internal/policy"
)

// sesStub plays the SES API behind the real SES adapter: it accepts ok
// sends, then answers every send with err until err is cleared.
type sesStub struct {
	mu  sync.Mutex
	ok  int
	err error
	// cancel, when set, is called before err is returned, as a worker
	// shutting down mid-send.
	cancel func()
	to     []string
}

func (s *sesStub) SendEmail(_ context.Context, in *sesv2.SendEmailInput, _ ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil && s.ok <= 0 {
		if s.cancel != nil {
			s.cancel()
		}
		return nil, s.err
	}
	s.ok--
	s.to = append(s.to, in.Destination.ToAddresses...)
	return &sesv2.SendEmailOutput{MessageId: aws.String("ses-" + in.Destination.ToAddresses[0])}, nil
}

func (s *sesStub) set(ok int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ok, s.err = ok, err
}

// perTo counts the copies each address got.
func (s *sesStub) perTo() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]int{}
	for _, a := range s.to {
		out[a]++
	}
	return out
}

// withErrors counts the campaign's rows that have an error recorded.
func (h *harness) withErrors(t *testing.T) int {
	t.Helper()
	var n int
	if err := h.db.QueryRow(context.Background(),
		`SELECT count(*) FROM campaign_recipients WHERE campaign_id = $1 AND error IS NOT NULL`, h.campaign.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The SES errors that mean "not now" (MG-87), as SES returns them.
var retryLaterErrors = map[string]error{
	"too many requests": &sestypes.TooManyRequestsException{Message: aws.String("Too many requests")},
	"limit exceeded":    &sestypes.LimitExceededException{Message: aws.String("Limit exceeded")},
	"sending paused":    &sestypes.SendingPausedException{Message: aws.String("Sending is paused for this account")},
	"account suspended": &sestypes.AccountSuspendedException{Message: aws.String("The account is suspended")},
	"daily quota":       &sestypes.MessageRejected{Message: aws.String("Daily message quota exceeded.")},
	"sending rate":      &sestypes.MessageRejected{Message: aws.String("Maximum sending rate exceeded.")},
}

// Each "not now" error leaves the copies queued with no error and no
// refund, even on the last attempt, and the batch is done with.
func TestPipelineRetryLaterEachClass(t *testing.T) {
	for name, sesErr := range retryLaterErrors {
		t.Run(name, func(t *testing.T) {
			h := launchedHarness(t, 2)
			p := h.pipeline(fastLimits)
			stub := &sesStub{err: sesErr}
			p.Mail = mail.SES{Client: stub, ConfigurationSet: "cs"}
			q := &recorded{}
			dispatch(t, p, q)
			b := q.take()[0]
			before := h.balance(t)
			if err := p.HandleBatch(context.Background(), b, MaxAttempts); err != nil {
				t.Fatalf("HandleBatch: %v", err)
			}
			if c := h.counts(t); c["queued"] != 3 || len(c) != 1 {
				t.Errorf("counts %v", c)
			}
			if n := h.withErrors(t); n != 0 {
				t.Errorf("%d rows got an error", n)
			}
			if h.balance(t) != before {
				t.Errorf("balance %d, was %d: refunded", h.balance(t), before)
			}
		})
	}
}

// A throttled batch keeps what it sent, waits RetryLaterAfter without
// using an attempt, and goes out once SES accepts again, each copy once.
func TestPipelineRetryLaterWaitsThenSends(t *testing.T) {
	h := launchedHarness(t, 9)
	ctx := context.Background()
	p := h.pipeline(fastLimits)
	stub := &sesStub{ok: 3, err: retryLaterErrors["daily quota"]}
	p.Mail = mail.SES{Client: stub, ConfigurationSet: "cs"}
	q := &recorded{}
	dispatch(t, p, q)
	batches := q.take()
	if len(batches) != 1 || len(batches[0].IDs) != 10 {
		t.Fatalf("batches %+v", batches)
	}
	b := batches[0]
	before := h.balance(t)
	if err := p.HandleBatch(ctx, b, 1); err != nil {
		t.Fatalf("throttled batch: %v", err)
	}
	if c := h.counts(t); c["sent"] != 3 || c["queued"] != 7 || h.withErrors(t) != 0 || h.balance(t) != before {
		t.Fatalf("after the throttle: %v, %d errors, balance %d (was %d)", c, h.withErrors(t), h.balance(t), before)
	}

	// The rest isn't batched again on the next ticks, so SES isn't asked
	// on every one while it's throttling.
	dispatch(t, p, q)
	h.now = h.now.Add(RetryLaterAfter - time.Second)
	dispatch(t, p, q)
	if got := q.take(); len(got) != 0 {
		t.Fatalf("batched again within RetryLaterAfter: %+v", got)
	}

	// Still throttled a minute later: it waits again, still with no
	// attempt used and nothing failed.
	h.now = h.now.Add(time.Second)
	dispatch(t, p, q)
	again := q.take()
	if len(again) != 1 || len(again[0].IDs) != 7 {
		t.Fatalf("batched after the wait: %+v", again)
	}
	if err := p.HandleBatch(ctx, again[0], MaxAttempts); err != nil {
		t.Fatalf("still throttled: %v", err)
	}
	if c := h.counts(t); c["sent"] != 3 || c["queued"] != 7 {
		t.Fatalf("still throttled: %v", c)
	}

	// SES accepts again: the campaign carries on and finishes.
	stub.set(0, nil)
	h.now = h.now.Add(RetryLaterAfter)
	dispatch(t, p, q)
	last := q.take()
	if len(last) != 1 || len(last[0].IDs) != 7 {
		t.Fatalf("batched once SES accepts: %+v", last)
	}
	if err := p.HandleBatch(ctx, last[0], 1); err != nil {
		t.Fatal(err)
	}
	// The first batch delivered again sends nothing more.
	if err := p.HandleBatch(ctx, b, 2); err != nil {
		t.Fatal(err)
	}
	dispatch(t, p, q)
	got := stub.perTo()
	if len(got) != 10 {
		t.Errorf("%d addresses got mail", len(got))
	}
	for addr, n := range got {
		if n != 1 {
			t.Errorf("%s got %d copies", addr, n)
		}
	}
	if c := h.counts(t); c["sent"] != 10 || h.status(t) != campaigns.StatusSent || h.balance(t) != before {
		t.Errorf("at the end: %v, status %s, balance %d (was %d)", c, h.status(t), h.balance(t), before)
	}
}

// A batch delivered again while its rows wait sends each copy once, and
// the dispatcher then has nothing left to batch.
func TestPipelineRetryLaterRedelivered(t *testing.T) {
	h := launchedHarness(t, 4)
	ctx := context.Background()
	p := h.pipeline(fastLimits)
	stub := &sesStub{ok: 2, err: retryLaterErrors["too many requests"]}
	p.Mail = mail.SES{Client: stub, ConfigurationSet: "cs"}
	q := &recorded{}
	dispatch(t, p, q)
	b := q.take()[0]
	if err := p.HandleBatch(ctx, b, 1); err != nil {
		t.Fatal(err)
	}
	stub.set(0, nil)
	if err := p.HandleBatch(ctx, b, 2); err != nil {
		t.Fatal(err)
	}
	h.now = h.now.Add(RetryLaterAfter)
	dispatch(t, p, q)
	if got := q.take(); len(got) != 0 {
		t.Errorf("batched rows already sent: %+v", got)
	}
	for addr, n := range stub.perTo() {
		if n != 1 {
			t.Errorf("%s got %d copies", addr, n)
		}
	}
	if c := h.counts(t); c["sent"] != 5 || h.status(t) != campaigns.StatusSent {
		t.Errorf("%v, status %s", c, h.status(t))
	}
}

// A worker shutting down mid-send keeps its claim even when the error is
// a "not now" one: the copy may have gone out, so it's never sent twice.
func TestPipelineRetryLaterWhileShuttingDown(t *testing.T) {
	h := launchedHarness(t, 2)
	p := h.pipeline(fastLimits)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stub := &sesStub{ok: 1, err: retryLaterErrors["too many requests"], cancel: cancel}
	p.Mail = mail.SES{Client: stub, ConfigurationSet: "cs"}
	q := &recorded{}
	dispatch(t, p, q)
	if err := p.HandleBatch(ctx, q.take()[0], 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("HandleBatch: %v", err)
	}
	if c := h.counts(t); c["sent"] != 1 || c["sending"] != 1 || c["queued"] != 1 {
		t.Errorf("counts %v", c)
	}
}

// Any other SES error still fails the copies on the last attempt.
func TestPipelineOtherSESErrorsStillFail(t *testing.T) {
	h := launchedHarness(t, 2)
	ctx := context.Background()
	p := h.pipeline(fastLimits)
	p.Mail = mail.SES{Client: &sesStub{err: &sestypes.MessageRejected{Message: aws.String("Email address is not verified.")}},
		ConfigurationSet: "cs"}
	q := &recorded{}
	dispatch(t, p, q)
	b := q.take()[0]
	for attempt := 1; attempt < MaxAttempts; attempt++ {
		var sf *sendFailed
		if err := p.HandleBatch(ctx, b, attempt); !errors.As(err, &sf) {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
	}
	before := h.balance(t)
	if err := p.HandleBatch(ctx, b, MaxAttempts); err == nil {
		t.Fatal("last attempt succeeded")
	}
	if c := h.counts(t); c["failed"] != 3 || h.balance(t) != before+3 {
		t.Errorf("counts %v, balance %d (was %d)", c, h.balance(t), before)
	}
}

// The platform cap (MG-87) counts every account's copies, sent and on
// their way, and the account's own limit still applies under it.
func TestPipelinePlatformDailyCap(t *testing.T) {
	h := launchedHarness(t, 29)
	ctx := context.Background()
	// Another account sent 20 copies an hour ago and has 5 on their way.
	var other, otherCampaign string
	if err := h.db.QueryRow(ctx, `
		INSERT INTO accounts (name, from_name, from_email, postal_address, terms_version, terms_accepted_at)
		VALUES ('Other Shop', 'Other Shop', 'hi@othershop.example', '1 High St', $1, now()) RETURNING id`,
		policy.Current().ID).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRow(ctx, `
		INSERT INTO campaigns (account_id, name, all_contacts, status, launched_at) VALUES ($1, 'Sale', true, 'sending', $2) RETURNING id`,
		other, h.now.Add(-2*time.Hour)).Scan(&otherCampaign); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(ctx, `
		INSERT INTO campaign_recipients (account_id, campaign_id, status, sent_at, enqueued_at)
		SELECT $1, $2, 'sent', $3, $3 FROM generate_series(1, 20)`, other, otherCampaign, h.now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(ctx, `
		INSERT INTO campaign_recipients (account_id, campaign_id, status, enqueued_at)
		SELECT $1, $2, 'queued', $3 FROM generate_series(1, 5)`, other, otherCampaign, h.now.Add(-10*time.Minute)); err != nil {
		t.Fatal(err)
	}

	limits := fastLimits
	limits.PlatformDaily = 40
	p := h.pipeline(limits)
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
	dispatch(t, p, q)
	if n := sendAll(); n != 15 {
		t.Fatalf("batched %d, want 15 (40 less the other account's 25)", n)
	}
	dispatch(t, p, q)
	if n := sendAll(); n != 0 {
		t.Errorf("batched %d past the platform cap", n)
	}

	// A day later the other account's copies have aged out (its 5 waited
	// past RequeueAfter, so they no longer count as on their way), and the
	// account's own limit is the lower one.
	h.now = h.now.Add(25 * time.Hour)
	if _, err := h.db.Exec(ctx, `UPDATE accounts SET daily_send_limit = 10 WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	dispatch(t, p, q)
	if n := sendAll(); n != 10 {
		t.Errorf("batched %d, want the account's 10", n)
	}
	if c := h.counts(t); c["sent"] != 25 || c["queued"] != 5 || h.status(t) != campaigns.StatusSending {
		t.Errorf("%v, status %s", c, h.status(t))
	}
}
