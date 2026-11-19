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
	asked   []int32
	// extended are the receipt handles whose visibility was extended, and
	// the timeouts asked for.
	extended []string
	timeouts []int32
}

func (f *fakeSQS) ReceiveMessage(ctx context.Context, in *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	f.mu.Lock()
	f.asked = append(f.asked, in.MaxNumberOfMessages)
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

func (f *fakeSQS) ChangeMessageVisibility(_ context.Context, in *sqs.ChangeMessageVisibilityInput, _ ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.extended = append(f.extended, aws.ToString(in.ReceiptHandle))
	f.timeouts = append(f.timeouts, in.VisibilityTimeout)
	return &sqs.ChangeMessageVisibilityOutput{}, nil
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

func TestMaxMessagesPerReceive(t *testing.T) {
	for _, tc := range []struct{ set, want int32 }{{0, 10}, {1, 1}, {4, 4}, {11, 10}} {
		f := &fakeSQS{batch: []types.Message{msg("a")}}
		handled := make(chan struct{}, 1)
		h := HandlerFunc(func(context.Context, types.Message) error { handled <- struct{}{}; return nil })
		ctx, cancel := context.WithCancel(context.Background())
		w := &Worker{SQS: f, Log: quietLog(), Queues: []Queue{{Name: "send", URL: "https://sqs/send", Handler: h, MaxMessages: tc.set}}}
		done := make(chan error, 1)
		go func() { done <- w.Run(ctx) }()
		<-handled
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if f.asked[0] != tc.want {
			t.Errorf("MaxMessages %d: asked for %d, want %d", tc.set, f.asked[0], tc.want)
		}
	}
}

// A message that takes longer than half its queue's visibility timeout has
// its visibility extended until it's handled, and not after.
func TestKeepsSlowMessagesInvisible(t *testing.T) {
	f := &fakeSQS{batch: []types.Message{msg("slow")}}
	handled := make(chan struct{})
	h := HandlerFunc(func(context.Context, types.Message) error {
		time.Sleep(3 * time.Second)
		close(handled)
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	w := &Worker{SQS: f, Log: quietLog(), Queues: []Queue{{Name: "send", URL: "https://sqs/send", Handler: h, Visibility: 2 * time.Second}}}
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	<-handled
	time.Sleep(1500 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	// Extended at about 1s and 2s while handled (3s), then no more.
	if n := len(f.extended); n < 2 || n > 3 {
		t.Errorf("extended %d times: %v", n, f.extended)
	}
	for i, rh := range f.extended {
		if rh != "rh-slow" || f.timeouts[i] != 2 {
			t.Errorf("extension %d: %s for %ds", i, rh, f.timeouts[i])
		}
	}
	if len(f.deleted) != 1 {
		t.Errorf("deleted %v", f.deleted)
	}
}
