// Package worker drains mailgoose's SQS queues: send jobs from the API, and
// SES events (bounces, complaints, deliveries, opens, clicks) through SNS.
//
// The handlers are stubs until the sending and reporting tickets land. A stub
// leaves each message on the queue, so SQS retries it and, after five tries,
// moves it to the queue's dead-letter queue, where nothing is lost.
package worker

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// SQSAPI is the part of the SQS client the worker uses.
type SQSAPI interface {
	ReceiveMessage(ctx context.Context, in *sqs.ReceiveMessageInput, opts ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessage(ctx context.Context, in *sqs.DeleteMessageInput, opts ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
	ChangeMessageVisibility(ctx context.Context, in *sqs.ChangeMessageVisibilityInput, opts ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error)
}

// Handler processes one message. Returning nil deletes it from the queue;
// returning an error leaves it for SQS to deliver again.
type Handler interface {
	Handle(ctx context.Context, msg types.Message) error
}

// HandlerFunc adapts a function to Handler.
type HandlerFunc func(ctx context.Context, msg types.Message) error

// Handle implements Handler.
func (f HandlerFunc) Handle(ctx context.Context, msg types.Message) error { return f(ctx, msg) }

// ErrNotImplemented is what the stub handlers return.
var ErrNotImplemented = errors.New("handler not implemented yet")

// Queue is one queue and the handler for its messages.
type Queue struct {
	Name    string
	URL     string
	Handler Handler
	// MaxMessages is the most messages taken per receive, 1 to 10; 0
	// means 10. Queues whose messages take long to handle take fewer, so
	// none waits past its visibility timeout.
	MaxMessages int32
	// Visibility, when set, is the queue's visibility timeout. While a
	// message is handled, the worker extends its visibility by that much
	// every half of it, so a slow handler isn't delivered again mid-way.
	Visibility time.Duration
}

// Worker polls its queues until its context is cancelled.
type Worker struct {
	SQS    SQSAPI
	Queues []Queue
	Log    *slog.Logger
	// WaitTime is the SQS long-poll wait, at most 20 seconds.
	WaitTime time.Duration
	// ErrorBackoff is the pause after a failed receive.
	ErrorBackoff time.Duration
}

// Run polls every configured queue. With no queues configured it idles until
// ctx is cancelled, so the container stays up and stops cleanly.
func (w *Worker) Run(ctx context.Context) error {
	if w.Log == nil {
		w.Log = slog.New(slog.DiscardHandler)
	}
	w.Log = slog.New(LogWithJobs(w.Log.Handler()))
	var queues []Queue
	for _, q := range w.Queues {
		if q.URL == "" {
			w.Log.Warn("queue not configured, skipping", "queue", q.Name)
			continue
		}
		queues = append(queues, q)
	}
	if len(queues) == 0 {
		w.Log.Info("no queues configured; idling until stopped")
		<-ctx.Done()
		return nil
	}

	var wg sync.WaitGroup
	for _, q := range queues {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.poll(ctx, q)
		}()
	}
	w.Log.Info("worker started", "queues", len(queues))
	wg.Wait()
	w.Log.Info("worker stopped")
	return nil
}

func (w *Worker) poll(ctx context.Context, q Queue) {
	var wait int32 = 20
	if secs := w.WaitTime / time.Second; secs > 0 && secs < 20 {
		wait = int32(secs)
	}
	backoff := w.ErrorBackoff
	if backoff <= 0 {
		backoff = 5 * time.Second
	}

	maxMessages := q.MaxMessages
	if maxMessages <= 0 || maxMessages > 10 {
		maxMessages = 10
	}
	for ctx.Err() == nil {
		out, err := w.SQS.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:                    aws.String(q.URL),
			MaxNumberOfMessages:         maxMessages,
			WaitTimeSeconds:             wait,
			MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameApproximateReceiveCount},
		})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			w.Log.Error("receive failed", "queue", q.Name, "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			continue
		}
		for _, msg := range out.Messages {
			w.handle(ctx, q, msg)
		}
	}
}

func (w *Worker) handle(ctx context.Context, q Queue, msg types.Message) {
	// Everything logged while handling the message, here or in its handler,
	// carries the queue and message_id (OPS-4).
	ctx = WithJob(ctx, Job{Queue: q.Name, MessageID: aws.ToString(msg.MessageId)})
	stop := w.keepInvisible(ctx, q, msg)
	err := q.Handler.Handle(ctx, msg)
	stop()
	if err != nil {
		w.Log.WarnContext(ctx, "message left on queue",
			"receive_count", msg.Attributes[string(types.MessageSystemAttributeNameApproximateReceiveCount)],
			"err", err,
		)
		return
	}
	// Delete with a fresh context so a shutdown mid-delete doesn't make SQS
	// redeliver a message that was already handled.
	delCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if _, err := w.SQS.DeleteMessage(delCtx, &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(q.URL),
		ReceiptHandle: msg.ReceiptHandle,
	}); err != nil {
		w.Log.ErrorContext(delCtx, "delete failed; message will be redelivered", "err", err)
	}
}

// keepInvisible extends msg's visibility every half of q.Visibility until
// the returned stop is called. A failed extension is logged; the message
// may then be delivered again, which the handlers allow for.
func (w *Worker) keepInvisible(ctx context.Context, q Queue, msg types.Message) (stop func()) {
	if q.Visibility <= 0 {
		return func() {}
	}
	// SQS allows at most 12 hours.
	secs := int32(min(q.Visibility, 12*time.Hour) / time.Second) //nolint:gosec // capped above
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(q.Visibility / 2)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
			}
			if _, err := w.SQS.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
				QueueUrl:          aws.String(q.URL),
				ReceiptHandle:     msg.ReceiptHandle,
				VisibilityTimeout: secs,
			}); err != nil && ctx.Err() == nil {
				w.Log.Warn("couldn't extend a message's visibility", "queue", q.Name,
					"message_id", aws.ToString(msg.MessageId), "err", err)
			}
		}
	}()
	return func() {
		close(done)
		wg.Wait()
	}
}

// NotImplemented is the stub handler for queues whose work isn't built yet.
func NotImplemented() Handler {
	return HandlerFunc(func(context.Context, types.Message) error { return ErrNotImplemented })
}
