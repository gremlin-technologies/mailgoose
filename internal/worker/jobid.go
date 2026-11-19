package worker

import (
	"context"
	"log/slog"
)

// Job names the queue message a worker is handling, so every log line made
// while handling it can be matched to it: the worker's counterpart of the
// API's request ID.
type Job struct {
	Queue     string
	MessageID string
}

type jobKey struct{}

// WithJob returns ctx carrying job.
func WithJob(ctx context.Context, job Job) context.Context {
	return context.WithValue(ctx, jobKey{}, job)
}

// JobFrom returns the job in ctx, if there is one.
func JobFrom(ctx context.Context) (Job, bool) {
	job, ok := ctx.Value(jobKey{}).(Job)
	return job, ok
}

// jobHandler adds queue and message_id to every log record made with a
// context that carries a job.
type jobHandler struct{ slog.Handler }

// LogWithJobs wraps h so records logged with a job's context carry its queue
// and message_id. Wrapping twice, even with another wrapper in between (such
// as api.LogWithRequestIDs), adds them once.
func LogWithJobs(h slog.Handler) slog.Handler {
	if _, ok := h.(jobHandler); ok {
		return h
	}
	return jobHandler{h}
}

func (h jobHandler) Handle(ctx context.Context, r slog.Record) error {
	if job, ok := JobFrom(ctx); ok && !hasAttr(r, "message_id") {
		r.AddAttrs(slog.String("queue", job.Queue), slog.String("message_id", job.MessageID))
	}
	return h.Handler.Handle(ctx, r)
}

func (h jobHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return jobHandler{h.Handler.WithAttrs(attrs)}
}

func (h jobHandler) WithGroup(name string) slog.Handler {
	return jobHandler{h.Handler.WithGroup(name)}
}

// hasAttr reports whether r already has a top-level attribute named key, as
// it does when a second wrapper further in sees the same record.
func hasAttr(r slog.Record, key string) bool {
	found := false
	r.Attrs(func(a slog.Attr) bool {
		found = a.Key == key
		return !found
	})
	return found
}
