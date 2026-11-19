package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
)

// RequestIDHeader carries the request ID on every response. A client may
// send its own; we keep it if it looks safe, so a caller can match its logs
// to ours.
const RequestIDHeader = "X-Request-Id"

type ctxKey int

const requestIDKey ctxKey = iota

// RequestID returns the request ID in ctx, or "".
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// WithRequestID returns ctx carrying id.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// newRequestID returns "req_" and 24 hex characters.
func newRequestID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "req_" + hex.EncodeToString(b[:])
}

// validRequestID accepts 8 to 64 letters, digits, '-' and '_', which keeps
// log lines and headers safe from whatever a client sends.
func validRequestID(id string) bool {
	if len(id) < 8 || len(id) > 64 {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

func requestIDs(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(RequestIDHeader)
		if !validRequestID(id) {
			id = newRequestID()
		}
		w.Header().Set(RequestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(WithRequestID(r.Context(), id)))
	})
}

// requestIDHandler adds request_id to every log record made with a context
// that carries one, so a handler's own log lines match the request's.
type requestIDHandler struct{ slog.Handler }

// LogWithRequestIDs wraps h so records logged with a request's context carry
// its request_id. Wrapping twice, even with another wrapper in between (such
// as worker.LogWithJobs), adds it once.
func LogWithRequestIDs(h slog.Handler) slog.Handler {
	if _, ok := h.(requestIDHandler); ok {
		return h
	}
	return requestIDHandler{h}
}

func (h requestIDHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := RequestID(ctx); id != "" && !hasRequestID(r) {
		r.AddAttrs(slog.String("request_id", id))
	}
	return h.Handler.Handle(ctx, r)
}

func (h requestIDHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return requestIDHandler{h.Handler.WithAttrs(attrs)}
}

func (h requestIDHandler) WithGroup(name string) slog.Handler {
	return requestIDHandler{h.Handler.WithGroup(name)}
}

// hasRequestID reports whether r already has a request_id, as it does when a
// second wrapper further in sees the same record.
func hasRequestID(r slog.Record) bool {
	found := false
	r.Attrs(func(a slog.Attr) bool {
		found = a.Key == "request_id"
		return !found
	})
	return found
}
