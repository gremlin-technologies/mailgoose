package api

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

// passThrough stands for another handler wrapper, like the worker's job IDs.
type passThrough struct{ slog.Handler }

func TestRequestIDLoggedOnce(t *testing.T) {
	var buf bytes.Buffer
	// mailgoosed wraps its root logger, and Server.Handler wraps it again.
	log := slog.New(LogWithRequestIDs(passThrough{LogWithRequestIDs(slog.NewJSONHandler(&buf, nil))}))

	log.InfoContext(context.Background(), "no request")
	log.InfoContext(WithRequestID(context.Background(), "req_abc12345"), "in a request")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines", len(lines))
	}
	if strings.Contains(lines[0], "request_id") {
		t.Errorf("line without a request got one: %s", lines[0])
	}
	if n := strings.Count(lines[1], `"request_id":"req_abc12345"`); n != 1 {
		t.Errorf("request_id appears %d times: %s", n, lines[1])
	}
}
