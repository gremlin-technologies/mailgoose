package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// IdempotencyKeyHeader is the request header that makes a mutating request
// safe to retry.
const IdempotencyKeyHeader = "Idempotency-Key"

// IdempotencyTTL is how long a stored response is replayed.
const IdempotencyTTL = 24 * time.Hour

// idempotencyStaleClaim is how long an unfinished request holds its key. It is
// longer than the server's write timeout, so a claim this old belongs to a
// request whose process died, and the key can be used again.
const idempotencyStaleClaim = 2 * time.Minute

// StoredResponse is a response saved for replay.
type StoredResponse struct {
	Status int
	Header http.Header
	Body   []byte
}

// IdempotencyRecord is what the store holds for a key.
type IdempotencyRecord struct {
	RequestHash []byte
	// Response is nil while the first request is still running.
	Response *StoredResponse
}

// IdempotencyStore keeps idempotency keys and their responses.
type IdempotencyStore interface {
	// Claim records that a request with this scope, key and hash has started,
	// unless a live record exists. It returns claimed=true when the caller
	// should run the request, and the existing record otherwise. A record
	// older than IdempotencyTTL, or a claim older than idempotencyStaleClaim
	// that never completed, doesn't count as live.
	Claim(ctx context.Context, scope, key string, hash []byte, now time.Time) (claimed bool, existing *IdempotencyRecord, err error)
	// Complete stores the response for the claim made at claimedAt (the now
	// passed to Claim). It returns ErrIdempotencyClaimLost and stores nothing
	// when that claim is gone: taken over as stale by a later request, or
	// already completed or released.
	Complete(ctx context.Context, scope, key string, claimedAt time.Time, resp StoredResponse) error
	// Release drops the claim made at claimedAt, if it's still unfinished, so
	// the client can retry. It leaves alone a claim another request has taken
	// over.
	Release(ctx context.Context, scope, key string, claimedAt time.Time) error
}

// ErrIdempotencyClaimLost means a request finished after its claim was taken
// over by a later request, so its response wasn't stored.
var ErrIdempotencyClaimLost = errors.New("idempotency claim was taken over")

// Idempotency makes POST, PUT, PATCH and DELETE requests that carry an
// Idempotency-Key safe to retry. Within IdempotencyTTL, a repeat with the same
// key and the same method, path and body gets the stored response without
// the handler running again, marked with "Idempotent-Replayed: true". The same
// key with a different request gets 422 idempotency_key_reused. A repeat
// while the first is still running gets 409 idempotency_request_in_progress.
//
// Responses with a 5xx status aren't stored, so a retry runs again.
type Idempotency struct {
	Store IdempotencyStore
	// Scope returns who the key belongs to; keys from different callers never
	// collide. An empty scope skips idempotency for that request. Nil means
	// DefaultIdempotencyScope.
	Scope func(*http.Request) string
	// Now is the clock; tests replace it.
	Now func() time.Time
	// Log records store failures.
	Log *slog.Logger
}

// DefaultIdempotencyScope scopes keys to the request's credentials (a hash of
// the Authorization header). Requests without credentials skip idempotency:
// scoping them by IP could hand one caller's response to another behind the
// same address. The server uses callerIdempotencyScope, which scopes keys to
// the account and the credential; this is its fallback.
func DefaultIdempotencyScope(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(auth))
	return "auth:" + hex.EncodeToString(sum[:])
}

func validIdempotencyKey(k string) bool {
	if k == "" || len(k) > 255 {
		return false
	}
	for _, c := range k {
		if c < 0x21 || c > 0x7e {
			return false
		}
	}
	return true
}

// replayHeaderSkip lists response headers that belong to one exchange and are
// never replayed.
var replayHeaderSkip = map[string]bool{
	"Set-Cookie":          true,
	"X-Request-Id":        true,
	"Ratelimit-Limit":     true,
	"Ratelimit-Remaining": true,
	"Retry-After":         true,
	"Date":                true,
	"Content-Length":      true,
}

// Middleware applies idempotency to the requests it covers.
func (m *Idempotency) Middleware(next http.Handler) http.Handler {
	if m == nil || m.Store == nil {
		return next
	}
	scopeFn := m.Scope
	if scopeFn == nil {
		scopeFn = DefaultIdempotencyScope
	}
	now := m.Now
	if now == nil {
		now = time.Now
	}
	log := m.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get(IdempotencyKeyHeader)
		if key == "" || !mutating(r.Method) {
			next.ServeHTTP(w, r)
			return
		}
		if !validIdempotencyKey(key) {
			WriteError(w, BadRequest("invalid_idempotency_key", "Idempotency-Key must be 1 to 255 printable ASCII characters.").
				WithHint("Use a fresh UUID for each distinct request."))
			return
		}
		scope := scopeFn(r)
		if scope == "" {
			next.ServeHTTP(w, r)
			return
		}

		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, bodyLimit(r)))
		if err != nil {
			WriteError(w, &Error{Status: http.StatusRequestEntityTooLarge, Code: "request_too_large", Message: "The request body is too large."})
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		hash := requestHash(r, body)

		ctx := r.Context()
		claimedAt := now()
		claimed, existing, err := m.Store.Claim(ctx, scope, key, hash, claimedAt)
		if err != nil {
			log.ErrorContext(ctx, "idempotency claim failed", "err", err)
			WriteError(w, Internal())
			return
		}
		if !claimed {
			switch {
			case !bytes.Equal(existing.RequestHash, hash):
				WriteError(w, Unprocessable("idempotency_key_reused", "This Idempotency-Key was already used for a different request.").
					WithHint("Use a new key for each distinct request; reuse a key only to retry the same request."))
			case existing.Response == nil:
				WriteError(w, Conflict("idempotency_request_in_progress", "A request with this Idempotency-Key is still running.").
					WithHint("Wait a moment and retry with the same key to get its result."))
			default:
				replay(w, existing.Response)
			}
			return
		}

		rec := &captureWriter{header: http.Header{}, status: http.StatusOK}
		completed := false
		defer func() {
			if !completed {
				// The handler panicked; let the client retry. The recoverer
				// further out writes the 500.
				_ = m.Store.Release(context.WithoutCancel(ctx), scope, key, claimedAt)
			}
		}()
		next.ServeHTTP(rec, r)
		completed = true

		resp := StoredResponse{Status: rec.status, Header: storableHeader(rec.header), Body: rec.body.Bytes()}
		storeCtx := context.WithoutCancel(ctx)
		if rec.status >= 500 {
			err = m.Store.Release(storeCtx, scope, key, claimedAt)
		} else {
			err = m.Store.Complete(storeCtx, scope, key, claimedAt, resp)
		}
		if errors.Is(err, ErrIdempotencyClaimLost) {
			// This request ran past idempotencyStaleClaim and a retry took
			// the key over; the retry's response is the one kept.
			log.WarnContext(ctx, "idempotency claim taken over before the request finished", "key", key)
		} else if err != nil {
			log.ErrorContext(ctx, "idempotency store failed", "err", err)
		}
		writeCaptured(w, resp)
	})
}

func mutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

func requestHash(r *http.Request, body []byte) []byte {
	h := sha256.New()
	h.Write([]byte(r.Method))
	h.Write([]byte{0})
	h.Write([]byte(r.URL.RequestURI()))
	h.Write([]byte{0})
	h.Write(body)
	return h.Sum(nil)
}

func replay(w http.ResponseWriter, resp *StoredResponse) {
	w.Header().Set("Idempotent-Replayed", "true")
	writeCaptured(w, *resp)
}

func writeCaptured(w http.ResponseWriter, resp StoredResponse) {
	h := w.Header()
	for k, vs := range resp.Header {
		if replayHeaderSkip[http.CanonicalHeaderKey(k)] {
			continue
		}
		h[k] = append([]string(nil), vs...)
	}
	w.WriteHeader(resp.Status)
	_, _ = w.Write(resp.Body)
}

// captureWriter buffers a handler's response so it can be stored before it's
// sent. API responses are small JSON, so buffering is cheap.
type captureWriter struct {
	header      http.Header
	status      int
	wroteHeader bool
	body        bytes.Buffer
}

func (c *captureWriter) Header() http.Header { return c.header }

func (c *captureWriter) WriteHeader(code int) {
	if !c.wroteHeader {
		c.status, c.wroteHeader = code, true
	}
}

func (c *captureWriter) Write(b []byte) (int, error) {
	c.wroteHeader = true
	return c.body.Write(b)
}

// MemoryIdempotencyStore keeps keys in memory. It's for tests and for running
// the API without a database; in AWS the API uses the Postgres store so every
// task sees the same keys.
type MemoryIdempotencyStore struct {
	mu   sync.Mutex
	recs map[string]*memIdem
}

type memIdem struct {
	hash    []byte
	resp    *StoredResponse
	created time.Time
}

// Claim implements IdempotencyStore.
func (s *MemoryIdempotencyStore) Claim(_ context.Context, scope, key string, hash []byte, now time.Time) (bool, *IdempotencyRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recs == nil {
		s.recs = map[string]*memIdem{}
	}
	k := scope + "\x00" + key
	if r, ok := s.recs[k]; ok && live(r.created, r.resp != nil, now) {
		return false, &IdempotencyRecord{RequestHash: r.hash, Response: r.resp}, nil
	}
	s.recs[k] = &memIdem{hash: hash, created: now}
	return true, nil, nil
}

// Complete implements IdempotencyStore.
func (s *MemoryIdempotencyStore) Complete(_ context.Context, scope, key string, claimedAt time.Time, resp StoredResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.recs[scope+"\x00"+key]
	if !ok || r.resp != nil || !r.created.Equal(claimedAt) {
		return ErrIdempotencyClaimLost
	}
	r.resp = &resp
	return nil
}

// Release implements IdempotencyStore.
func (s *MemoryIdempotencyStore) Release(_ context.Context, scope, key string, claimedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := scope + "\x00" + key
	if r, ok := s.recs[k]; ok && r.resp == nil && r.created.Equal(claimedAt) {
		delete(s.recs, k)
	}
	return nil
}

func live(created time.Time, done bool, now time.Time) bool {
	age := now.Sub(created)
	if done {
		return age < IdempotencyTTL
	}
	return age < idempotencyStaleClaim
}

// storableHeader copies h without the per-exchange headers.
func storableHeader(h http.Header) http.Header {
	out := http.Header{}
	for k, vs := range h {
		if !replayHeaderSkip[http.CanonicalHeaderKey(k)] {
			out[k] = append([]string(nil), vs...)
		}
	}
	return out
}
