package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/auth"
)

// Caller is who a request comes from: an API key, a CLI token or a web
// session. Handlers get it with CallerFrom.
type Caller struct {
	AccountID string
	// UserID is the person behind the session or token; for an API key, the
	// person who made it, if they still exist.
	UserID string
	// Scope is auth.ScopeFull or auth.ScopeDraft. Sessions are full.
	Scope string
	// KeyID is set for API keys and CLI tokens.
	KeyID string
	// Session is set for web sessions.
	Session *auth.Current
}

type callerKey struct{}

// CallerFrom returns the request's caller, or nil when it carries no valid
// credentials.
func CallerFrom(ctx context.Context) *Caller {
	c, _ := ctx.Value(callerKey{}).(*Caller)
	return c
}

// failedAuthPerMinute is how many bad bearer secrets one IP may send a
// minute before it gets 429s without a database lookup.
const failedAuthPerMinute = 30

// authenticate works out the caller. A bearer secret must be valid: a wrong
// or revoked one is a 401 whatever the endpoint. A session cookie that
// matches no session is ignored here; the endpoints that need a caller answer
// 401. Health checks skip this, so they never touch the database.
func (s *Server) authenticate(next http.Handler, now func() time.Time) http.Handler {
	failures := &RateLimiter{PerMinute: failedAuthPerMinute, Now: now}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Auth == nil || r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)
			return
		}
		var caller *Caller
		if h := r.Header.Get("Authorization"); h != "" {
			ipKey := "ip:" + ClientIP(r)
			if retry, blocked := failures.blocked(ipKey); blocked {
				writeRateLimited(w, retry)
				return
			}
			token, ok := strings.CutPrefix(h, "Bearer ")
			p, err := (*auth.Principal)(nil), auth.ErrInvalidKey
			if ok {
				p, err = s.Auth.Authenticate(r.Context(), strings.TrimSpace(token))
			}
			if errors.Is(err, auth.ErrInvalidKey) {
				failures.allow(ipKey)
				WriteError(w, Unauthorized("invalid_api_key", "That API key or token is wrong or has been revoked.").
					WithHint("Send `Authorization: Bearer <key>` with an active key. Make a new one in the web app or with `mailgoose keys create`."))
				return
			}
			if err != nil {
				s.Log.ErrorContext(r.Context(), "authenticate key", "err", err)
				WriteError(w, Internal())
				return
			}
			caller = &Caller{AccountID: p.Account.ID, UserID: p.UserID, Scope: p.Scope, KeyID: p.KeyID}
		} else if c, err := r.Cookie(SessionCookie); err == nil && c.Value != "" {
			cur, err := s.Auth.SessionByToken(r.Context(), c.Value)
			switch {
			case errors.Is(err, auth.ErrNoSession):
			case err != nil:
				s.Log.ErrorContext(r.Context(), "find session", "err", err)
				WriteError(w, Internal())
				return
			default:
				caller = &Caller{AccountID: cur.Account.ID, UserID: cur.User.ID, Scope: auth.ScopeFull, Session: cur}
			}
		}
		if caller != nil {
			r = r.WithContext(context.WithValue(r.Context(), callerKey{}, caller))
		}
		next.ServeHTTP(w, r)
	})
}

// requireCaller returns the request's caller, or writes a 401 (or a 503 when
// there's no database) and returns nil.
func (s *Server) requireCaller(w http.ResponseWriter, r *http.Request) *Caller {
	if s.authUnavailable(w) {
		return nil
	}
	c := CallerFrom(r.Context())
	if c == nil {
		if _, err := r.Cookie(SessionCookie); err == nil {
			clearSessionCookie(w)
		}
		WriteError(w, Unauthorized("unauthenticated", "Sign in, or send an API key.").
			WithHint("Send `Authorization: Bearer <key>`, or sign in to the web app again."))
		return nil
	}
	return c
}

// requireFull is requireCaller for operations a draft-scope key may not use
// (x-required-scope: full in the spec): launching campaigns, billing changes
// and key management.
func (s *Server) requireFull(w http.ResponseWriter, r *http.Request) *Caller {
	c := s.requireCaller(w, r)
	if c != nil && c.Scope != auth.ScopeFull {
		WriteError(w, Forbidden("insufficient_scope", "A draft-scope key can't do this.").
			WithHint("Use a full-scope key, or do it in the web app."))
		return nil
	}
	return c
}

// callerIdempotencyScope keeps Idempotency-Keys apart per account and per
// credential, so one key can never replay a response made for another (a
// draft key mustn't get the secret a full key's request created).
func callerIdempotencyScope(r *http.Request) string {
	c := CallerFrom(r.Context())
	switch {
	case c == nil:
		return DefaultIdempotencyScope(r)
	case c.KeyID != "":
		return "account:" + c.AccountID + ":key:" + c.KeyID
	default:
		return "account:" + c.AccountID + ":user:" + c.UserID
	}
}

// sessionRateLimitKey counts web-session requests per session cookie (a hash
// of it), and everything else as DefaultRateLimitKey does: per bearer secret,
// or per IP.
func sessionRateLimitKey(r *http.Request) string {
	if r.Header.Get("Authorization") == "" {
		if c, err := r.Cookie(SessionCookie); err == nil && c.Value != "" {
			sum := sha256.Sum256([]byte(c.Value))
			return "session:" + hex.EncodeToString(sum[:16])
		}
	}
	return DefaultRateLimitKey(r)
}
