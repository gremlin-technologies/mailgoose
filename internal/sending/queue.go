package sending

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// SQSSender is the part of the SQS client the dispatcher uses.
type SQSSender interface {
	SendMessageBatch(ctx context.Context, in *sqs.SendMessageBatchInput, opts ...func(*sqs.Options)) (*sqs.SendMessageBatchOutput, error)
}

// SQSQueue puts batches on the send queue in SQS.
type SQSQueue struct {
	SQS SQSSender
	URL string
}

// Enqueue implements Queue. It sends up to ten batches a call, SQS's most.
func (q *SQSQueue) Enqueue(ctx context.Context, batches []Batch) error {
	for len(batches) > 0 {
		n := min(10, len(batches))
		entries := make([]types.SendMessageBatchRequestEntry, n)
		for i, b := range batches[:n] {
			body, err := json.Marshal(b)
			if err != nil {
				return err
			}
			entries[i] = types.SendMessageBatchRequestEntry{Id: aws.String(strconv.Itoa(i)), MessageBody: aws.String(string(body))}
		}
		out, err := q.SQS.SendMessageBatch(ctx, &sqs.SendMessageBatchInput{QueueUrl: aws.String(q.URL), Entries: entries})
		if err != nil {
			return err
		}
		if len(out.Failed) > 0 {
			f := out.Failed[0]
			return fmt.Errorf("%d of %d batches not queued: %s", len(out.Failed), n, aws.ToString(f.Message))
		}
		batches = batches[n:]
	}
	return nil
}

// SQSHandler handles a send-queue message for the worker.
func (p *Pipeline) SQSHandler() func(ctx context.Context, msg types.Message) error {
	return func(ctx context.Context, msg types.Message) error {
		var b Batch
		if err := json.Unmarshal([]byte(aws.ToString(msg.Body)), &b); err != nil || b.CampaignID == "" {
			// Not a batch: nothing to retry, so it's dropped (and logged).
			p.log().ErrorContext(ctx, "dropping a send message that isn't a batch", "message_id", aws.ToString(msg.MessageId))
			return nil
		}
		attempt, _ := strconv.Atoi(msg.Attributes[string(types.MessageSystemAttributeNameApproximateReceiveCount)])
		return p.HandleBatch(ctx, b, max(1, attempt))
	}
}

// LocalQueue runs batches in this process, for running without SQS
// (local development). It retries a failed batch up to MaxAttempts times
// straight away.
type LocalQueue struct {
	Pipeline *Pipeline
	Log      *slog.Logger

	once sync.Once
	jobs chan Batch
}

// ErrLocalQueueFull means the in-process queue is full; the dispatcher
// tries again on its next pass.
var ErrLocalQueueFull = errors.New("the local send queue is full")

// Run sends batches until ctx ends.
func (q *LocalQueue) Run(ctx context.Context) {
	q.init()
	for {
		select {
		case <-ctx.Done():
			return
		case b := <-q.jobs:
			for attempt := 1; attempt <= MaxAttempts; attempt++ {
				err := q.Pipeline.HandleBatch(ctx, b, attempt)
				if err == nil || ctx.Err() != nil {
					break
				}
				if q.Log != nil {
					q.Log.WarnContext(ctx, "send batch failed", "campaign_id", b.CampaignID, "attempt", attempt, "err", err)
				}
			}
		}
	}
}

func (q *LocalQueue) init() { q.once.Do(func() { q.jobs = make(chan Batch, 1000) }) }

// Enqueue implements Queue.
func (q *LocalQueue) Enqueue(_ context.Context, batches []Batch) error {
	q.init()
	for _, b := range batches {
		select {
		case q.jobs <- b:
		default:
			// The rest wait for the next pass. A batch already queued
			// whose rows stay unmarked is harmless: rows are claimed one
			// at a time, so none is sent twice.
			return ErrLocalQueueFull
		}
	}
	return nil
}
