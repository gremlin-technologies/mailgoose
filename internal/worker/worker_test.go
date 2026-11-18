package worker

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// fakeSQS hands out one batch of messages, then long-polls (blocks) until the
// context is cancelled, like an empty queue.
type fakeSQS struct {
	mu      sync.Mutex
	batch   []types.Message
	deleted []string
}

func (f *fakeSQS) ReceiveMessage(ctx context.Context, _ *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	f.mu.Lock()
	b := f.batch
	f.batch = nil
	f.mu.Unlock()
	if len(b) > 0 {
		return &sqs.ReceiveMessageOutput{Messages: b}, nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (f *fakeSQS) DeleteMessage(_ context.Context, in *sqs.DeleteMessageInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, aws.ToString(in.ReceiptHandle))
	return &sqs.DeleteMessageOutput{}, nil
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func msg(id string) types.Message {
	return types.Message{MessageId: aws.String(id), ReceiptHandle: aws.String("rh-" + id)}
}

func TestDeletesHandledAndKeepsFailed(t *testing.T) {
	f := &fakeSQS{batch: []types.Message{msg("ok"), msg("fail")}}
	handled := make(chan string, 2)
	h := HandlerFunc(func(_ context.Context, m types.Message) error {
		handled <- aws.ToString(m.MessageId)
		if aws.ToString(m.MessageId) == "fail" {
			return ErrNotImplemented
		}
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	w := &Worker{SQS: f, Log: quietLog(), Queues: []Queue{{Name: "send", URL: "https://sqs/send", Handler: h}}}
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	<-handled
	<-handled
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(f.deleted) != 1 || f.deleted[0] != "rh-ok" {
		t.Errorf("deleted = %v, want only rh-ok", f.deleted)
	}
}

func TestIdlesWithNoQueues(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	w := &Worker{SQS: &fakeSQS{}, Log: quietLog(), Queues: []Queue{{Name: "send", Handler: NotImplemented()}}}
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	select {
	case err := <-done:
		t.Fatalf("Run returned before it was stopped: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop")
	}
}
