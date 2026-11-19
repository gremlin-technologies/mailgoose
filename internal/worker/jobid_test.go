package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// lockedBuffer lets the worker's goroutines and the test share one log.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) records(t *testing.T) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(b.buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line isn't JSON: %q", line)
		}
		out = append(out, rec)
	}
	return out
}

func TestLogsCarryTheJob(t *testing.T) {
	var buf lockedBuffer
	// mailgoosed wraps its root logger once, so the worker's and the
	// handlers' lines alike carry the job.
	log := slog.New(LogWithJobs(slog.NewJSONHandler(&buf, nil)))

	f := &fakeSQS{batch: []types.Message{msg("m-1")}}
	handled := make(chan struct{})
	h := HandlerFunc(func(ctx context.Context, m types.Message) error {
		// A handler's own lines, logged with the context it was given,
		// carry the job without passing it around.
		log.InfoContext(ctx, "sending batch")
		close(handled)
		return errors.New("SES throttled")
	})

	ctx, cancel := context.WithCancel(context.Background())
	w := &Worker{SQS: f, Log: log, Queues: []Queue{{Name: "send", URL: "https://sqs/send", Handler: h}}}
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	<-handled
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}

	byMsg := map[string]map[string]any{}
	for _, rec := range buf.records(t) {
		byMsg[rec["msg"].(string)] = rec
	}
	for _, m := range []string{"sending batch", "message left on queue"} {
		rec, ok := byMsg[m]
		if !ok {
			t.Fatalf("no %q line; got %v", m, byMsg)
		}
		if rec["queue"] != "send" || rec["message_id"] != "m-1" {
			t.Errorf("%q: queue=%v message_id=%v, want send and m-1", m, rec["queue"], rec["message_id"])
		}
	}
}

func TestLogWithJobs(t *testing.T) {
	var buf lockedBuffer
	log := slog.New(LogWithJobs(LogWithJobs(slog.NewJSONHandler(&buf, nil)))).With("component", "sending")

	log.InfoContext(context.Background(), "no job")
	log.InfoContext(WithJob(context.Background(), Job{Queue: "ses-events", MessageID: "m-2"}), "with job")

	recs := buf.records(t)
	if len(recs) != 2 {
		t.Fatalf("got %d records", len(recs))
	}
	if _, ok := recs[0]["message_id"]; ok {
		t.Errorf("a line without a job got a message_id: %v", recs[0])
	}
	if recs[1]["queue"] != "ses-events" || recs[1]["message_id"] != "m-2" || recs[1]["component"] != "sending" {
		t.Errorf("with job = %v", recs[1])
	}
	// Wrapping twice adds the attributes once, even with another wrapper
	// in between.
	buf.buf.Reset()
	other := slog.New(LogWithJobs(otherWrapper{LogWithJobs(slog.NewJSONHandler(&buf, nil))}))
	other.InfoContext(WithJob(context.Background(), Job{Queue: "send", MessageID: "m-3"}), "nested")
	if n := strings.Count(buf.buf.String(), `"message_id"`); n != 1 {
		t.Errorf("message_id appears %d times", n)
	}
}

// otherWrapper stands for another handler wrapper, like the API's request IDs.
type otherWrapper struct{ slog.Handler }
