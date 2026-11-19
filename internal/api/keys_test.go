package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.yaml.in/yaml/v3"

	"github.com/gremlin-technologies/mailgoose/internal/auth"
	"github.com/gremlin-technologies/mailgoose/internal/policy"
)

type testKey struct{ id, secret string }

// testKeys holds each test's API key, made by withKey.
var testKeys sync.Map // *testing.T -> testKey

// withKey wraps a server constructor: it also makes an active account with
// an API key of the given scope, kept for bearer.
func withKey(scope string, server func(*testing.T) *Server) func(*testing.T) *Server {
	return func(t *testing.T) *Server {
		t.Helper()
		s := server(t)
		k := newTestKey(t, s, "key@example.com", scope)
		testKeys.Store(t, k)
		t.Cleanup(func() { testKeys.Delete(t) })
		return s
	}
}

// newTestKey makes an active account for email and an API key in it.
func newTestKey(t *testing.T, s *Server, email, scope string) testKey {
	t.Helper()
	ctx := context.Background()
	pool := s.DB.(*pgxpool.Pool)
	var accountID, userID string
	if err := pool.QueryRow(ctx, `INSERT INTO accounts (name, status, terms_version, terms_accepted_at) VALUES ('Test', 'active', $1, now()) RETURNING id`,
		policy.Current().ID).Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users (account_id, email) VALUES ($1, $2) RETURNING id`, accountID, email).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	k, err := s.Auth.CreateKey(ctx, accountID, userID, "test", scope)
	if err != nil {
		t.Fatal(err)
	}
	return testKey{id: k.ID, secret: k.Secret}
}

// bearer sends the test's API key, or a well-formed key that matches none.
func bearer(req func(*testing.T) *http.Request) func(*testing.T) *http.Request {
	return func(t *testing.T) *http.Request {
		r := req(t)
		secret := "mg_test_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		if v, ok := testKeys.Load(t); ok {
			secret = v.(testKey).secret
		}
		r.Header.Set("Authorization", "Bearer "+secret)
		return r
	}
}

// ownKeyPath is DELETE /v1/api-keys/{the test's own key}.
func ownKeyPath(t *testing.T) *http.Request {
	v, ok := testKeys.Load(t)
	if !ok {
		t.Fatal("no key for this test")
	}
	return httptest.NewRequest(http.MethodDelete, "/v1/api-keys/"+v.(testKey).id, nil)
}

const createKeyBody = `{"name":"laptop","scope":"draft"}`

var (
	fullKey  = func(s func(*testing.T) *Server) func(*testing.T) *Server { return withKey(auth.ScopeFull, s) }
	draftKey = func(s func(*testing.T) *Server) func(*testing.T) *Server { return withKey(auth.ScopeDraft, s) }
)

func TestKeysThroughTheAPI(t *testing.T) {
	s := fullKey(authServer)(t)
	h := s.Handler()

	rec := do(t, h, bearer(jsonRequest(http.MethodPost, "/v1/api-keys", `{"name":"agent","scope":"draft"}`))(t))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var created struct {
		Key struct {
			ID    string `json:"id"`
			Scope string `json:"scope"`
		} `json:"key"`
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || created.Secret == "" || created.Key.Scope != "draft" {
		t.Fatalf("created = %+v, %v", created, err)
	}

	// The new draft key works, but can't manage keys.
	asDraft := func(r *http.Request) *http.Request {
		r.Header.Set("Authorization", "Bearer "+created.Secret)
		return r
	}
	rec = do(t, h, asDraft(httptest.NewRequest(http.MethodGet, "/v1/api-keys", nil)))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "insufficient_scope") {
		t.Errorf("draft key listing keys: %d %s", rec.Code, rec.Body)
	}

	// Listing shows both keys, with no secrets.
	rec = do(t, h, bearer(request(http.MethodGet, "/v1/api-keys"))(t))
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), created.Secret) || strings.Count(rec.Body.String(), `"id"`) != 2 {
		t.Errorf("list: %d %s", rec.Code, rec.Body)
	}

	// A revoked key fails on its next request.
	rec = do(t, h, bearer(request(http.MethodDelete, "/v1/api-keys/"+created.Key.ID))(t))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, h, asDraft(httptest.NewRequest(http.MethodGet, "/v1/api-keys", nil)))
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "invalid_api_key") {
		t.Errorf("revoked key: %d %s", rec.Code, rec.Body)
	}
}

func TestKeysWithWebSession(t *testing.T) {
	h := authServer(t).Handler()
	signedIn(t, h)
	rec := do(t, h, withSession(jsonRequest(http.MethodPost, "/v1/api-keys", createKeyBody))(t))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create with session: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, h, fromAnotherSite(withSession(jsonRequest(http.MethodPost, "/v1/api-keys", createKeyBody)))(t))
	if rec.Code != http.StatusForbidden {
		t.Errorf("create from another site: %d", rec.Code)
	}
}

func TestKeysAreKeptPerAccount(t *testing.T) {
	s := authServer(t)
	h := s.Handler()
	mine := newTestKey(t, s, "mine@example.com", auth.ScopeFull)
	theirs := newTestKey(t, s, "theirs@example.com", auth.ScopeFull)

	r := httptest.NewRequest(http.MethodDelete, "/v1/api-keys/"+theirs.id, nil)
	r.Header.Set("Authorization", "Bearer "+mine.secret)
	if rec := do(t, h, r); rec.Code != http.StatusNotFound {
		t.Errorf("revoking another account's key: %d", rec.Code)
	}
	r = httptest.NewRequest(http.MethodGet, "/v1/api-keys", nil)
	r.Header.Set("Authorization", "Bearer "+mine.secret)
	if rec := do(t, h, r); strings.Contains(rec.Body.String(), theirs.id) {
		t.Error("listing shows another account's key")
	}
}

// TestIdempotencyKeysArePerCredential: one key can't replay a response made
// for another, even in the same account.
func TestIdempotencyKeysArePerCredential(t *testing.T) {
	s := authServer(t)
	h := s.Handler()
	full := newTestKey(t, s, "full@example.com", auth.ScopeFull)
	send := func(secret string) *httptest.ResponseRecorder {
		r := jsonRequest(http.MethodPost, "/v1/api-keys", createKeyBody)(t)
		r.Header.Set("Authorization", "Bearer "+secret)
		r.Header.Set("Idempotency-Key", "same")
		return do(t, h, r)
	}
	first := send(full.secret)
	if first.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", first.Code, first.Body)
	}
	var created struct {
		Secret string `json:"secret"`
	}
	_ = json.Unmarshal(first.Body.Bytes(), &created)
	// The draft key it made sends the same request with the same key.
	second := send(created.Secret)
	if second.Header().Get("Idempotent-Replayed") == "true" || strings.Contains(second.Body.String(), created.Secret) {
		t.Fatalf("a draft key replayed a full key's response: %s", second.Body)
	}
	if second.Code != http.StatusForbidden {
		t.Errorf("draft key: %d, want 403", second.Code)
	}
}

func TestBadBearerSecretsAreLimitedPerIP(t *testing.T) {
	h := authServer(t).Handler()
	var rec *httptest.ResponseRecorder
	for range failedAuthPerMinute + 1 {
		rec = do(t, h, bearer(request(http.MethodGet, "/v1/api-keys"))(t))
	}
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Errorf("after %d bad keys: %d %s", failedAuthPerMinute, rec.Code, rec.Body)
	}
	r := bearer(request(http.MethodGet, "/v1/api-keys"))(t)
	r.RemoteAddr = "198.51.100.9:1234"
	if rec := do(t, h, r); rec.Code != http.StatusUnauthorized {
		t.Errorf("another IP: %d", rec.Code)
	}
}

// TestDraftScope sends a draft-scope key to every operation: the ones the
// spec marks `x-required-scope: full` must answer 403 insufficient_scope, and
// no other may.
func TestDraftScope(t *testing.T) {
	raw, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]struct {
			Scope string `yaml:"x-required-scope"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	h := draftKey(authServer)(t).Handler()
	v, _ := testKeys.Load(t)
	secret := v.(testKey).secret
	marked := 0
	for path, ops := range spec.Paths {
		for method, op := range ops {
			method = strings.ToUpper(method)
			if method == "PARAMETERS" {
				continue
			}
			t.Run(method+" "+path, func(t *testing.T) {
				p := path
				for strings.Contains(p, "{") {
					i, j := strings.Index(p, "{"), strings.Index(p, "}")
					p = p[:i] + "00000000-0000-4000-8000-000000000000" + p[j+1:]
				}
				r := jsonRequest(method, p, `{}`)(t)
				r.Header.Set("Authorization", "Bearer "+secret)
				rec := do(t, h, r)
				refused := rec.Code == http.StatusForbidden && strings.Contains(rec.Body.String(), "insufficient_scope")
				switch op.Scope {
				case "full":
					marked++
					if !refused {
						t.Errorf("draft key got %d %s, want 403 insufficient_scope", rec.Code, rec.Body)
					}
				case "":
					if refused {
						t.Error("refused a draft key, but the spec doesn't mark it x-required-scope: full")
					}
				default:
					t.Errorf("x-required-scope %q; the only value is full", op.Scope)
				}
			})
		}
	}
	if marked < 3 {
		t.Errorf("only %d operations need a full key; the key operations at least should", marked)
	}
}
