package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type seenRequest struct {
	method, path, query, auth string
	body                      map[string]any
}

// keysStub answers the key operations and records each request.
func keysStub(t *testing.T) (*httptest.Server, *[]seenRequest) {
	t.Helper()
	var seen []seenRequest
	key := `{"id":"3f2a9c1e-8b7d-4e6f-a5c4-1d2e3f4a5b6c","name":"agent","scope":"draft","hint":"mg_test_…k3x9","created_at":"2026-11-18T12:00:00Z","last_used_at":null}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := seenRequest{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, auth: r.Header.Get("Authorization")}
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			_ = json.Unmarshal(b, &req.body)
		}
		seen = append(seen, req)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/api-keys":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"key":` + key + `,"secret":"mg_test_secretsecretsecretsecretsecretk3x9"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/api-keys":
			_, _ = w.Write([]byte(`{"data":[` + key + `],"next_cursor":"abc"}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/api-keys/3f2a9c1e-8b7d-4e6f-a5c4-1d2e3f4a5b6c":
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"No active API key with that id in this account.","hint":""}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func keysHarness(t *testing.T) (*harness, *[]seenRequest) {
	srv, seen := keysStub(t)
	h := newHarness(t, "")
	h.env[EnvAPIURL] = srv.URL
	h.env[EnvAPIKey] = "mg_test_fullkey"
	return h, seen
}

func TestKeysCreate(t *testing.T) {
	h, seen := keysHarness(t)
	if code := h.run("keys", "create", "--name", "agent", "--scope", "draft"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if !strings.Contains(h.out.String(), "mg_test_secretsecretsecretsecretsecretk3x9") || !strings.Contains(h.out.String(), "only time") {
		t.Errorf("output %q", h.out)
	}
	got := (*seen)[0]
	if got.method != http.MethodPost || got.auth != "Bearer mg_test_fullkey" || got.body["name"] != "agent" || got.body["scope"] != "draft" {
		t.Errorf("request = %+v", got)
	}

	if code := h.run("keys", "create", "--name", "agent", "--scope", "draft", "--json"); code != ExitOK {
		t.Fatalf("--json: exit %d", code)
	}
	var out struct {
		Secret string `json:"secret"`
		Key    struct {
			ID string `json:"id"`
		} `json:"key"`
	}
	if err := json.Unmarshal(h.out.Bytes(), &out); err != nil || out.Secret == "" || out.Key.ID == "" {
		t.Errorf("--json output %q", h.out)
	}

	for _, args := range [][]string{
		{"keys", "create", "--scope", "draft"},
		{"keys", "create", "--name", "x"},
		{"keys", "create", "--name", "x", "--scope", "admin"},
	} {
		if code := h.run(args...); code != ExitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, ExitUsage)
		}
	}
}

func TestKeysList(t *testing.T) {
	h, seen := keysHarness(t)
	if code := h.run("keys", "list", "--limit", "10", "--cursor", "xyz"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if q := (*seen)[0].query; !strings.Contains(q, "limit=10") || !strings.Contains(q, "cursor=xyz") {
		t.Errorf("query = %q", q)
	}
	for _, want := range []string{"agent", "draft", "mg_test_…k3x9", "never", "--cursor abc"} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, h.out)
		}
	}
}

func TestKeysRevoke(t *testing.T) {
	h, seen := keysHarness(t)
	if code := h.run("keys", "revoke", "3f2a9c1e-8b7d-4e6f-a5c4-1d2e3f4a5b6c"); code != ExitUsage || !strings.Contains(h.errOut.String(), "--yes") {
		t.Errorf("without --yes: exit %d: %s", code, h.errOut)
	}
	if len(*seen) != 0 {
		t.Error("revoked without --yes")
	}
	if code := h.run("keys", "revoke", "3f2a9c1e-8b7d-4e6f-a5c4-1d2e3f4a5b6c", "--yes"); code != ExitOK {
		t.Errorf("exit %d: %s", code, h.errOut)
	}
	if code := h.run("keys", "revoke", "nope", "--yes"); code != ExitNotFound {
		t.Errorf("unknown key: exit %d", code)
	}
}

func TestKeysDraftScopeIsAnAuthError(t *testing.T) {
	srv := apiStub(t, map[string]func(http.ResponseWriter){"/v1/api-keys": apiError(http.StatusForbidden, "insufficient_scope")})
	h := newHarness(t, "")
	h.env[EnvAPIURL] = srv.URL
	h.env[EnvAPIKey] = "mg_test_draft"
	if code := h.run("keys", "list", "--json"); code != ExitAuth || !strings.Contains(h.errOut.String(), "insufficient_scope") {
		t.Errorf("exit %d: %s", code, h.errOut)
	}
}
