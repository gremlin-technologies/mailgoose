package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/dbtest"
)

// TestErrorShape checks the body of every error status handlers use.
func TestErrorShape(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{BadRequest("invalid_json", "bad"), 400, "invalid_json"},
		{Unauthorized("unauthenticated", "who?"), 401, "unauthenticated"},
		{Forbidden("insufficient_scope", "no"), 403, "insufficient_scope"},
		{NotFound("contact_not_found", "gone"), 404, "contact_not_found"},
		{Conflict("already_exists", "dup").WithHint("use another"), 409, "already_exists"},
		{Unprocessable("validation_failed", "nope"), 422, "validation_failed"},
		{TooManyRequests("rate_limited", "slow down"), 429, "rate_limited"},
		{Internal(), 500, "internal"},
		// A plain error never leaks: it becomes a generic 500.
		{fmt.Errorf("pq: password authentication failed"), 500, "internal"},
	}
	for _, tc := range cases {
		t.Run(tc.code+fmt.Sprint(tc.status), func(t *testing.T) {
			rec := httptest.NewRecorder()
			WriteError(rec, tc.err)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.status)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q", ct)
			}
			assertErrorShape(t, rec.Body.Bytes(), tc.code)
			if strings.Contains(rec.Body.String(), "password") {
				t.Error("internal error text leaked to the client")
			}
		})
	}
}

// assertErrorShape checks body is exactly {"error": {"code","message","hint"}}.
func assertErrorShape(t *testing.T, body []byte, code string) {
	t.Helper()
	var outer map[string]json.RawMessage
	if err := json.Unmarshal(body, &outer); err != nil {
		t.Fatalf("not JSON: %q", body)
	}
	if len(outer) != 1 || outer["error"] == nil {
		t.Fatalf("top level should be only \"error\": %s", body)
	}
	var inner map[string]any
	if err := json.Unmarshal(outer["error"], &inner); err != nil {
		t.Fatal(err)
	}
	if len(inner) != 3 {
		t.Errorf("error should have code, message and hint only: %s", body)
	}
	for _, f := range []string{"code", "message", "hint"} {
		if _, ok := inner[f].(string); !ok {
			t.Errorf("error.%s missing or not a string: %s", f, body)
		}
	}
	if inner["code"] != code {
		t.Errorf("code = %v, want %s", inner["code"], code)
	}
	if inner["message"] == "" {
		t.Error("message is empty")
	}
}

func TestErrorShapeFromServer(t *testing.T) {
	h := newServer(nil).Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))
	assertErrorShape(t, rec.Body.Bytes(), "not_found")

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	assertErrorShape(t, rec.Body.Bytes(), "database_not_configured")
}

func TestPanicIs500InErrorShape(t *testing.T) {
	s := newServer(nil)
	h := s.recoverer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != 500 {
		t.Fatalf("status = %d", rec.Code)
	}
	assertErrorShape(t, rec.Body.Bytes(), "internal")
}

func TestDecodeJSON(t *testing.T) {
	type in struct {
		Name string `json:"name"`
	}
	cases := []struct {
		body, code string
	}{
		{`{"name":"a"}`, ""},
		{``, "invalid_json"},
		{`{"name":`, "invalid_json"},
		{`{"title":"a"}`, "invalid_json"}, // unknown field
		{`{"name":"a"} {}`, "invalid_json"},
		{`{"name":"` + strings.Repeat("x", MaxBodyBytes) + `"}`, "request_too_large"},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
		r.Header.Set("Content-Type", "application/json")
		var v in
		err := DecodeJSON(httptest.NewRecorder(), r, &v)
		got := ""
		var e *Error
		if errors.As(err, &e) {
			got = e.Code
		} else if err != nil {
			t.Fatalf("not an *Error: %v", err)
		}
		if got != tc.code {
			t.Errorf("body %.30q: code %q, want %q", tc.body, got, tc.code)
		}
	}
}

// TestPagination pages through 250 rows 100 at a time, the way a handler
// does: fetch limit+1 rows after the cursor, then build the page.
func TestPagination(t *testing.T) {
	rows := make([]string, 250)
	for i := range rows {
		rows[i] = fmt.Sprintf("row-%04d", i)
	}
	// query mimics `WHERE id > $after ORDER BY id LIMIT $limit+1`.
	query := func(after string, limit int) []string {
		var out []string
		for _, r := range rows {
			if r > after && len(out) < limit+1 {
				out = append(out, r)
			}
		}
		return out
	}

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pr, err := ParsePageRequest(r)
		if err != nil {
			WriteError(w, err)
			return
		}
		WriteJSON(w, 200, NewPage(query(pr.After, pr.Limit), pr.Limit, func(s string) string { return s }))
	})

	var seen []string
	cursor, pages := "", 0
	for {
		url := "/things?limit=100"
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
		var page Page[string]
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		pages++
		seen = append(seen, page.Data...)
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
		if pages > 5 {
			t.Fatal("too many pages")
		}
	}
	if pages != 3 || len(seen) != 250 {
		t.Fatalf("pages = %d, rows = %d; want 3 and 250", pages, len(seen))
	}
	for i, r := range seen {
		if r != rows[i] {
			t.Fatalf("row %d = %s, want %s (gap or repeat)", i, r, rows[i])
		}
	}
}

func TestPaginationEdges(t *testing.T) {
	empty := NewPage[string](nil, 10, func(s string) string { return s })
	b, _ := json.Marshal(empty)
	if string(b) != `{"data":[],"next_cursor":null}` {
		t.Errorf("empty page = %s", b)
	}
	exact := NewPage([]string{"a", "b"}, 2, func(s string) string { return s })
	if exact.NextCursor != nil {
		t.Error("a full last page should have no next cursor")
	}

	for _, q := range []string{"limit=0", "limit=101", "limit=x", "cursor=!!", "cursor=" + "e30"} {
		_, err := ParsePageRequest(httptest.NewRequest(http.MethodGet, "/?"+q, nil))
		if err == nil {
			t.Errorf("%s should be rejected", q)
		}
	}
	pr, err := ParsePageRequest(httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil || pr.Limit != DefaultPageLimit || pr.After != "" {
		t.Errorf("defaults = %+v, %v", pr, err)
	}
}

func TestRequestIDHeaderAndLog(t *testing.T) {
	var logs bytes.Buffer
	s := &Server{Log: slog.New(slog.NewJSONHandler(&logs, nil))}
	h := s.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))
	id := rec.Header().Get(RequestIDHeader)
	if !strings.HasPrefix(id, "req_") {
		t.Fatalf("X-Request-Id = %q", id)
	}
	var line map[string]any
	if err := json.Unmarshal(logs.Bytes(), &line); err != nil {
		t.Fatalf("log line: %q", logs.String())
	}
	if line["msg"] != "request" || line["request_id"] != id {
		t.Errorf("log line %v should carry request_id %s", line, id)
	}

	// A sane ID from the client is kept; a hostile one is replaced.
	for in, keep := range map[string]bool{"client-trace-0123": true, "bad id\nInjected: 1": false, "short": false} {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		r.Header.Set(RequestIDHeader, in)
		h.ServeHTTP(rec, r)
		got := rec.Header().Get(RequestIDHeader)
		if (got == in) != keep || got == "" {
			t.Errorf("sent %q, got %q", in, got)
		}
	}
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func TestRateLimit(t *testing.T) {
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	s := &Server{RateLimitPerMinute: 3, Now: c.now}
	h := s.Handler()
	do := func(key string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/nope", nil)
		r.Header.Set("Authorization", "Bearer "+key)
		h.ServeHTTP(rec, r)
		return rec
	}

	for i, want := range []string{"2", "1", "0"} {
		rec := do("a")
		if rec.Code == 429 {
			t.Fatalf("request %d limited too early", i)
		}
		if rec.Header().Get("RateLimit-Limit") != "3" || rec.Header().Get("RateLimit-Remaining") != want {
			t.Errorf("request %d headers: %v", i, rec.Header())
		}
	}
	rec := do("a")
	if rec.Code != 429 {
		t.Fatalf("4th request status = %d, want 429", rec.Code)
	}
	assertErrorShape(t, rec.Body.Bytes(), "rate_limited")
	if rec.Header().Get("Retry-After") != "20" || rec.Header().Get("RateLimit-Remaining") != "0" {
		t.Errorf("429 headers: %v", rec.Header())
	}

	// Another key has its own bucket.
	if do("b").Code == 429 {
		t.Error("key b limited by key a's use")
	}
	// Health checks are never limited.
	r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	r.Header.Set("Authorization", "Bearer a")
	hrec := httptest.NewRecorder()
	h.ServeHTTP(hrec, r)
	if hrec.Code != 200 {
		t.Errorf("healthz limited: %d", hrec.Code)
	}

	// One token comes back every 20 seconds at 3 a minute.
	c.advance(20 * time.Second)
	if do("a").Code == 429 {
		t.Error("should be allowed after Retry-After")
	}
	if do("a").Code != 429 {
		t.Error("only one token should have refilled")
	}
}

func TestRateLimitOff(t *testing.T) {
	h := (&Server{}).Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))
	if rec.Header().Get("RateLimit-Limit") != "" {
		t.Error("no limit headers when limiting is off")
	}
}

func TestClientIP(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.5:1234"
	if ClientIP(r) != "10.0.0.5" {
		t.Errorf("got %s", ClientIP(r))
	}
	r.Header.Set("X-Forwarded-For", "6.6.6.6, 203.0.113.9")
	if ClientIP(r) != "203.0.113.9" {
		t.Errorf("should take the ALB's address, got %s", ClientIP(r))
	}
}

// idemHarness counts how often the handler runs.
type idemHarness struct {
	calls atomic.Int32
	h     http.Handler
	clock *clock
}

func newIdemHarness(store IdempotencyStore) *idemHarness {
	ih := &idemHarness{clock: &clock{t: time.Unix(1_800_000_000, 0)}}
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := ih.calls.Add(1)
		var body map[string]any
		if err := DecodeJSON(w, r, &body); err != nil {
			WriteError(w, err)
			return
		}
		if body["fail"] == true {
			WriteError(w, Internal())
			return
		}
		w.Header().Set("Location", "/v1/things/1")
		WriteJSON(w, http.StatusCreated, map[string]any{"call": n, "name": body["name"]})
	})
	ih.h = requestIDs((&Idempotency{Store: store, Now: ih.clock.now}).Middleware(inner))
	return ih
}

func (ih *idemHarness) post(key, auth, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/things", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if key != "" {
		r.Header.Set(IdempotencyKeyHeader, key)
	}
	if auth != "" {
		r.Header.Set("Authorization", "Bearer "+auth)
	}
	ih.h.ServeHTTP(rec, r)
	return rec
}

func testIdempotency(t *testing.T, store IdempotencyStore) {
	ih := newIdemHarness(store)

	first := ih.post("k1", "key-a", `{"name":"x"}`)
	if first.Code != 201 {
		t.Fatalf("first: %d %s", first.Code, first.Body)
	}
	again := ih.post("k1", "key-a", `{"name":"x"}`)
	if again.Code != 201 || again.Body.String() != first.Body.String() {
		t.Fatalf("replay: %d %s, want %s", again.Code, again.Body, first.Body)
	}
	if ih.calls.Load() != 1 {
		t.Fatalf("handler ran %d times, want 1", ih.calls.Load())
	}
	if again.Header().Get("Idempotent-Replayed") != "true" || again.Header().Get("Location") != "/v1/things/1" {
		t.Errorf("replay headers: %v", again.Header())
	}
	if again.Header().Get(RequestIDHeader) == first.Header().Get(RequestIDHeader) {
		t.Error("a replay gets its own request ID")
	}

	// Same key, different body.
	reused := ih.post("k1", "key-a", `{"name":"y"}`)
	if reused.Code != 422 {
		t.Fatalf("reused key: %d", reused.Code)
	}
	assertErrorShape(t, reused.Body.Bytes(), "idempotency_key_reused")

	// Keys are scoped to the caller's credentials.
	if rec := ih.post("k1", "key-b", `{"name":"y"}`); rec.Code != 201 {
		t.Errorf("other caller's same key: %d", rec.Code)
	}
	// No key, or no credentials: the handler runs every time.
	before := ih.calls.Load()
	ih.post("", "key-a", `{"name":"x"}`)
	ih.post("k1", "", `{"name":"x"}`)
	if ih.calls.Load() != before+2 {
		t.Error("requests without a key or credentials should always run")
	}

	// A 5xx isn't stored, so a retry runs again.
	before = ih.calls.Load()
	ih.post("k-fail", "key-a", `{"fail":true}`)
	ih.post("k-fail", "key-a", `{"fail":true}`)
	if ih.calls.Load() != before+2 {
		t.Error("a failed request should run again on retry")
	}

	// After 24 hours the key is free again.
	ih.clock.advance(IdempotencyTTL + time.Second)
	before = ih.calls.Load()
	if rec := ih.post("k1", "key-a", `{"name":"y"}`); rec.Code != 201 || ih.calls.Load() != before+1 {
		t.Errorf("after TTL: %d, calls %d", rec.Code, ih.calls.Load()-before)
	}

	// Bad keys are refused.
	if rec := ih.post(strings.Repeat("k", 256), "key-a", `{}`); rec.Code != 400 {
		t.Errorf("long key: %d", rec.Code)
	}
}

func TestIdempotencyMemory(t *testing.T) {
	testIdempotency(t, &MemoryIdempotencyStore{})
}

func TestIdempotencyInProgress(t *testing.T) {
	store := &MemoryIdempotencyStore{}
	now := time.Unix(1_800_000_000, 0)
	ok, _, _ := store.Claim(context.Background(), "s", "k", []byte("h"), now)
	if !ok {
		t.Fatal("first claim failed")
	}
	ok, rec, _ := store.Claim(context.Background(), "s", "k", []byte("h"), now.Add(time.Second))
	if ok || rec.Response != nil {
		t.Fatal("second claim should see the running request")
	}
	// A claim whose process died is taken over.
	if ok, _, _ := store.Claim(context.Background(), "s", "k", []byte("h"), now.Add(3*time.Minute)); !ok {
		t.Error("a stale claim should be taken over")
	}
}

// TestIdempotencyPostgres runs the same checks against the Postgres store.
func TestIdempotencyPostgres(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `DELETE FROM idempotency_keys`); err != nil {
		t.Fatal(err)
	}
	store := PGIdempotencyStore{DB: pool}
	testIdempotency(t, store)

	// In progress, then completed.
	now := time.Now()
	if ok, _, err := store.Claim(ctx, "s", "running", []byte("h"), now); !ok || err != nil {
		t.Fatalf("claim: %v %v", ok, err)
	}
	ok, rec, err := store.Claim(ctx, "s", "running", []byte("h"), now.Add(time.Second))
	if ok || err != nil || rec.Response != nil {
		t.Fatalf("second claim: %v %+v %v", ok, rec, err)
	}
	if ok, _, _ := store.Claim(ctx, "s", "running", []byte("h"), now.Add(3*time.Minute)); !ok {
		t.Error("a stale claim should be taken over")
	}
}
