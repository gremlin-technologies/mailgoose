package api

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RateLimiter limits requests per key with a token bucket: each key may burst
// up to PerMinute requests and refills at PerMinute a minute.
//
// Buckets live in memory, so on ECS each API task enforces the limit on its
// own; with N tasks a key can get up to N times the limit. That's acceptable
// while the API runs one or two tasks. If it needs to be exact, the buckets
// can move to Postgres behind the same middleware.
type RateLimiter struct {
	// PerMinute is the steady rate and the burst size. Zero or less turns
	// limiting off.
	PerMinute int
	// Now is the clock; tests replace it.
	Now func() time.Time
	// Key returns the key a request counts against. Nil means DefaultRateLimitKey.
	Key func(*http.Request) string

	mu      sync.Mutex
	buckets map[string]*bucket
	sweep   time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// DefaultRateLimitKey counts a request against its credentials when it has
// any (a hash of the Authorization header, so the secret never sits in
// memory), and against the client's IP otherwise. Once API keys exist
// (MG-09), authenticated requests should be keyed by key ID instead.
func DefaultRateLimitKey(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); auth != "" {
		sum := sha256.Sum256([]byte(auth))
		return "auth:" + hex.EncodeToString(sum[:16])
	}
	return "ip:" + ClientIP(r)
}

// ClientIP returns the caller's IP. Behind the ALB, that's the last address
// in X-Forwarded-For: the ALB appends the address it saw, and anything
// earlier came from the client and can't be trusted.
func ClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// allow takes a token for key. It returns whether the request may go ahead,
// the tokens left, and how long until one is available when it may not.
func (l *RateLimiter) allow(key string) (ok bool, remaining int, retryAfter time.Duration) {
	now := l.Now()
	limit := float64(l.PerMinute)
	perSecond := limit / 60

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.buckets == nil {
		l.buckets = map[string]*bucket{}
	}
	// Drop buckets idle long enough to be full again, so memory stays bounded.
	if now.Sub(l.sweep) > time.Minute {
		for k, b := range l.buckets {
			if now.Sub(b.last) > time.Minute {
				delete(l.buckets, k)
			}
		}
		l.sweep = now
	}

	b, found := l.buckets[key]
	if !found {
		b = &bucket{tokens: limit, last: now}
		l.buckets[key] = b
	}
	b.tokens = math.Min(limit, b.tokens+now.Sub(b.last).Seconds()*perSecond)
	b.last = now

	if b.tokens < 1 {
		wait := time.Duration((1 - b.tokens) / perSecond * float64(time.Second))
		return false, 0, wait
	}
	b.tokens--
	return true, int(b.tokens), 0
}

// Middleware enforces the limit. Every limited response carries
// RateLimit-Limit and RateLimit-Remaining; a 429 also carries Retry-After in
// whole seconds. Health checks are never limited.
func (l *RateLimiter) Middleware(next http.Handler) http.Handler {
	if l == nil || l.PerMinute <= 0 {
		return next
	}
	if l.Now == nil {
		l.Now = time.Now
	}
	keyFn := l.Key
	if keyFn == nil {
		keyFn = DefaultRateLimitKey
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)
			return
		}
		ok, remaining, retryAfter := l.allow(keyFn(r))
		h := w.Header()
		h.Set("RateLimit-Limit", strconv.Itoa(l.PerMinute))
		h.Set("RateLimit-Remaining", strconv.Itoa(remaining))
		if !ok {
			secs := int(math.Ceil(retryAfter.Seconds()))
			if secs < 1 {
				secs = 1
			}
			h.Set("Retry-After", strconv.Itoa(secs))
			WriteError(w, TooManyRequests("rate_limited", "Too many requests.").
				WithHint("Wait "+strconv.Itoa(secs)+" seconds (see Retry-After) and try again."))
			return
		}
		next.ServeHTTP(w, r)
	})
}
