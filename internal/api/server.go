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
	"strings"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/admin"
	"github.com/gremlin-technologies/mailgoose/internal/api/apigen"
	"github.com/gremlin-technologies/mailgoose/internal/auth"
	"github.com/gremlin-technologies/mailgoose/internal/billing"
	"github.com/gremlin-technologies/mailgoose/internal/campaigns"
	"github.com/gremlin-technologies/mailgoose/internal/contacts"
	"github.com/gremlin-technologies/mailgoose/internal/credits"
	"github.com/gremlin-technologies/mailgoose/internal/domains"
	"github.com/gremlin-technologies/mailgoose/internal/emailcheck"
	"github.com/gremlin-technologies/mailgoose/internal/emails"
	"github.com/gremlin-technologies/mailgoose/internal/forms"
	"github.com/gremlin-technologies/mailgoose/internal/images"
	"github.com/gremlin-technologies/mailgoose/internal/linkrisk"
	"github.com/gremlin-technologies/mailgoose/internal/reports"
	"github.com/gremlin-technologies/mailgoose/internal/sending"
	"github.com/gremlin-technologies/mailgoose/internal/signups"
	"github.com/gremlin-technologies/mailgoose/internal/testmode"
	"github.com/gremlin-technologies/mailgoose/internal/tracking"
	"github.com/gremlin-technologies/mailgoose/internal/unsubscribe"
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
	// Domains manages sending domains. Nil (no database) makes the domain
	// endpoints answer 503.
	Domains *domains.Service
	// Contacts is the account audience; nil without a database.
	Contacts *contacts.Service
	// Emails stores the account's emails; nil without a database.
	Emails *emails.Service
	// Forms stores the account's signup forms; nil without a database.
	Forms *forms.Service
	// Campaigns stores the account's campaigns; nil without a database.
	Campaigns *campaigns.Service
	// Sending sends test emails; nil without a database, which makes test
	// sends answer 503.
	Sending *sending.Service
	// Unsubscribe checks recipients' unsubscribe links; nil without a
	// database, which makes the unsubscribe pages answer 503.
	Unsubscribe *unsubscribe.Service
	// Signups takes posts to signup forms and confirm links; nil without a
	// database, which makes them answer 503.
	Signups *signups.Service
	// Tracking checks and records opens and clicks; nil without a
	// database, which makes tracked links answer 503 (the pixel still
	// loads).
	Tracking *tracking.Service
	// Billing sells credit packs through Stripe; nil (no database) makes
	// checkout and the Stripe webhook answer 503.
	Billing *billing.Service
	// Images stores pictures for image blocks; nil without a database or an
	// image store, which makes the image endpoints answer 503.
	Images *images.Service
	// ImageFiles serves the image store at /images/: a local directory, or
	// the bucket through presigned redirects when there's no CDN; nil when
	// images are served from a CDN.
	ImageFiles http.Handler
	// Reports counts campaign results; nil answers 503.
	Reports *reports.Service
	// Admin backs the mailgoose staff tools (OPS-6); nil without a
	// database, which makes them answer 503.
	Admin *admin.Service
	// TestAddresses keeps accounts' test addresses (MG-80); nil without a
	// database or a system sending domain, which makes their endpoints
	// answer 503.
	TestAddresses *testmode.Service
	// TrustedOrigins are other origins (such as the web app's, when it isn't
	// served from the API's own origin) allowed to send cookie-authenticated
	// requests that change something. The API's own origin always is.
	TrustedOrigins []string
	// AbuseEmail is where a suspended account appeals (MG-65), named in
	// its errors; empty names none.
	AbuseEmail string
	// WebURL is where people reach the web app, for links such as the CLI
	// login approval page. Empty means the request's own origin.
	WebURL string
	// Web serves the web app (internal/web) on every GET or HEAD that no API
	// route matches. Nil means those get the API's JSON 404.
	Web http.Handler
	// Links fetches the links in an email for the email check. Nil means
	// links are only looked at, never fetched; the server wires
	// emailcheck.HTTPLinks.
	Links emailcheck.LinkChecker
	// LinkSafety looks the email check's links up on the phishing and
	// malware lists (MG-64); nil skips that. The server wires the one the
	// sending service uses, so they share a cache.
	LinkSafety *linkrisk.Checker
	// linkChecks limits how often one account's email checks fetch links;
	// Handler sets it.
	linkChecks *RateLimiter
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

	s.linkChecks = &RateLimiter{PerMinute: linkChecksPerMinute, Now: now}

	mux := s.routes()
	var h http.Handler = mux
	h = (&Idempotency{Store: store, Now: now, Log: s.Log, Scope: callerIdempotencyScope}).Middleware(h)
	h = s.authenticate(h, now)
	h = s.crossOriginProtection(h)
	h = (&RateLimiter{PerMinute: s.RateLimitPerMinute, Now: now, Key: sessionRateLimitKey}).Middleware(h)
	h = s.logRequests(h)
	h = s.recoverer(h)
	api := requestIDs(h)
	// Recipient pages (unsubscribing, signup forms, confirm links) have their own, higher limit and none
	// of the API's sign-in layers.
	pages := requestIDs(s.recoverer(s.logRequests(
		(&RateLimiter{PerMinute: recipientPagesPerMinute, Now: now, Key: ClientIPKey, Limited: s.recipientPageLimited}).Middleware(s.recipientPages(now)))))
	tracked := requestIDs(s.recoverer(s.logRequests(
		(&RateLimiter{PerMinute: trackingPerMinute, Now: now, Key: ClientIPKey}).Middleware(s.trackingRoutes()))))
	// Stripe's webhook is signed by Stripe, so it skips the API's sign-in,
	// cross-origin check and rate limit.
	stripeHook := requestIDs(s.recoverer(s.logRequests(http.HandlerFunc(s.stripeWebhook))))
	// The web app skips the API's middleware: its files need no rate limit,
	// session lookup or idempotency.
	var imageFiles http.Handler
	if s.ImageFiles != nil {
		imageFiles = requestIDs(s.recoverer(http.StripPrefix("/images", s.ImageFiles)))
	}
	var web http.Handler
	if s.Web != nil {
		web = requestIDs(s.recoverer(s.Web))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == StripeWebhookPath:
			stripeHook.ServeHTTP(w, r)
		case imageFiles != nil && strings.HasPrefix(r.URL.Path, "/images/") && (r.Method == http.MethodGet || r.Method == http.MethodHead):
			imageFiles.ServeHTTP(w, r)
		case isTrackingPath(r.URL.Path):
			tracked.ServeHTTP(w, r)
		case isRecipientPage(r.URL.Path):
			pages.ServeHTTP(w, r)
		case web == nil || isAPIRequest(mux, r):
			api.ServeHTTP(w, r)
		default:
			web.ServeHTTP(w, r)
		}
	})
}

// isAPIRequest reports whether r is for the API rather than the web app:
// anything under /v1, any other route the spec defines (such as /healthz),
// and anything that isn't a GET or HEAD.
func isAPIRequest(mux *http.ServeMux, r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return true
	}
	if r.URL.Path == "/v1" || strings.HasPrefix(r.URL.Path, "/v1/") {
		return true
	}
	_, pattern := mux.Handler(r)
	return pattern != "/"
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
			"path", logPath(r.URL.Path),
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
				s.Log.ErrorContext(r.Context(), "panic in handler", "panic", v, "path", logPath(r.URL.Path))
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
