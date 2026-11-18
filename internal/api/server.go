// Package api is the mailgoose HTTP API.
//
// The contract lives in api/openapi.yaml. `make generate` turns it into the Go
// types and the ServerInterface in internal/api/apigen; Server implements that
// interface, so an operation added to the spec doesn't compile until it has a
// handler. Every handler shares the plumbing in
// this package: one error shape (errors.go), cursor pagination
// (pagination.go), request IDs (requestid.go), idempotency keys
// (idempotency.go) and per-key rate limits (ratelimit.go).
package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/api/apigen"
	"github.com/gremlin-technologies/mailgoose/internal/auth"
	"github.com/gremlin-technologies/mailgoose/internal/credits"
	"github.com/gremlin-technologies/mailgoose/internal/version"
)

var _ apigen.ServerInterface = (*Server)(nil)

// Pinger reports whether a dependency (the database) is reachable.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Server holds what the handlers need.
type Server struct {
	Log *slog.Logger
	// DB is checked by /readyz. Nil means no database is configured.
	DB Pinger
	// Idempotency stores Idempotency-Key responses. Nil keeps keys in memory,
	// which is only right for tests and a single local process.
	Idempotency IdempotencyStore
	// RateLimitPerMinute is the per-key request limit. Zero turns it off.
	RateLimitPerMinute int
	// Auth signs people up and in. Nil (no database) makes those endpoints
	// answer 503.
	Auth *auth.Service
	// Credits is the credit ledger. Nil (no database) makes the credits
	// endpoints answer 503.
	Credits *credits.Ledger
	// TrustedOrigins are other origins (such as the web app's, when it isn't
	// served from the API's own origin) allowed to send cookie-authenticated
	// requests that change something. The API's own origin always is.
	TrustedOrigins []string
	// WebURL is where people reach the web app, for links such as the CLI
	// login approval page. Empty means the request's own origin.
	WebURL string
	// Now is the clock for rate limits and idempotency; nil means time.Now.
	Now func() time.Time
}

// Handler returns the API's routes wrapped in the shared middleware. From the
// outside in: request ID, panic recovery, request log, rate limit,
// cross-origin protection, authentication, idempotency, routes.
func (s *Server) Handler() http.Handler {
	if s.Log == nil {
		s.Log = slog.New(slog.DiscardHandler)
	}
	s.Log = slog.New(LogWithRequestIDs(s.Log.Handler()))
	now := s.Now
	if now == nil {
		now = time.Now
	}
	store := s.Idempotency
	if store == nil {
		store = &MemoryIdempotencyStore{}
	}

	var h http.Handler = s.routes()
	h = (&Idempotency{Store: store, Now: now, Log: s.Log, Scope: callerIdempotencyScope}).Middleware(h)
	h = s.authenticate(h, now)
	h = s.crossOriginProtection(h)
	h = (&RateLimiter{PerMinute: s.RateLimitPerMinute, Now: now, Key: sessionRateLimitKey}).Middleware(h)
	h = s.logRequests(h)
	h = s.recoverer(h)
	return requestIDs(h)
}

// routes registers every operation in the spec, through the generated router,
// and a JSON 404 for everything else.
func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	apigen.HandlerWithOptions(s, apigen.StdHTTPServerOptions{
		BaseRouter: mux,
		// Called when a path, query or header parameter doesn't parse.
		ErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
			WriteError(w, BadRequest("invalid_parameter", err.Error()))
		},
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		WriteError(w, NotFound("not_found", "No such endpoint.").
			WithHint("See the API reference for the endpoints and methods that exist."))
	})
	return mux
}

func health() apigen.Health {
	info := version.Get()
	h := apigen.Health{Status: apigen.HealthStatusOk, Version: info.Version}
	if info.Commit != "" {
		h.Commit = &info.Commit
	}
	if info.BuildDate != "" {
		h.BuildDate = &info.BuildDate
	}
	return h
}

// GetHealth is the liveness check (GET /healthz) the ALB target group polls.
// It must not depend on the database: a database blip should not make the
// load balancer drain every task.
func (s *Server) GetHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, health())
}

// GetReadiness (GET /readyz) also checks the database.
func (s *Server) GetReadiness(w http.ResponseWriter, r *http.Request) {
	if s.DB == nil {
		writeError(w, http.StatusServiceUnavailable, "database_not_configured", "No database is configured.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.DB.Ping(ctx); err != nil {
		s.Log.WarnContext(r.Context(), "readiness check failed", "err", err)
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "The database is not reachable.")
		return
	}
	writeJSON(w, http.StatusOK, health())
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		// Health checks arrive every 15 seconds per target; keep them out of the logs.
		if r.URL.Path == "/healthz" && rec.status == http.StatusOK {
			return
		}
		s.Log.InfoContext(r.Context(), "request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(v)
				}
				s.Log.ErrorContext(r.Context(), "panic in handler", "panic", v, "path", r.URL.Path)
				WriteError(w, Internal())
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// ListenAndServe serves h on addr until ctx is cancelled, then drains
// in-flight requests. ECS sends SIGTERM and waits 30 seconds before SIGKILL.
func ListenAndServe(ctx context.Context, log *slog.Logger, addr string, h http.Handler) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second, // above the ALB's 60-second idle timeout
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("api listening", "addr", addr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	log.Info("api shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if err := <-errCh; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
