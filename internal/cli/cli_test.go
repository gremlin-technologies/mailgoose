package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type harness struct {
	app    *App
	out    *bytes.Buffer
	errOut *bytes.Buffer
	env    map[string]string
}

func newHarness(t *testing.T, stdin string) *harness {
	t.Helper()
	h := &harness{out: &bytes.Buffer{}, errOut: &bytes.Buffer{}, env: map[string]string{
		EnvConfig: filepath.Join(t.TempDir(), "mailgoose", "config.json"),
	}}
	h.app = &App{
		Stdin:  strings.NewReader(stdin),
		Stdout: h.out,
		Stderr: h.errOut,
		Getenv: func(k string) string { return h.env[k] },
		HTTP:   http.DefaultClient,
	}
	return h
}

func (h *harness) run(args ...string) int {
	h.out.Reset()
	h.errOut.Reset()
	// A fresh App per run, like a fresh process, so flags don't carry over.
	a := *h.app
	a.jsonOut, a.apiURL = false, ""
	return a.Run(context.Background(), args)
}

func TestVersionJSON(t *testing.T) {
	h := newHarness(t, "")
	if code := h.run("version", "--json"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	var v map[string]string
	if err := json.Unmarshal(h.out.Bytes(), &v); err != nil || v["version"] == "" {
		t.Fatalf("output %q", h.out)
	}
}

func TestConfigSetShowUnset(t *testing.T) {
	h := newHarness(t, "secret-key-1234\n")

	if code := h.run("config", "set", "api-url", "https://api.staging.mailgoose.app/"); code != ExitOK {
		t.Fatalf("set api-url: exit %d: %s", code, h.errOut)
	}
	if code := h.run("config", "set", "api-key", "-"); code != ExitOK {
		t.Fatalf("set api-key: exit %d: %s", code, h.errOut)
	}

	info, err := os.Stat(h.env[EnvConfig])
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("config file mode = %o, want 600", perm)
	}

	if code := h.run("config", "show", "--json"); code != ExitOK {
		t.Fatalf("show: exit %d", code)
	}
	var s map[string]string
	if err := json.Unmarshal(h.out.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	if s["api_url"] != "https://api.staging.mailgoose.app" || s["api_url_source"] != "file" {
		t.Errorf("api_url = %q (%s)", s["api_url"], s["api_url_source"])
	}
	if s["api_key"] != "****1234" || strings.Contains(h.out.String(), "secret-key") {
		t.Errorf("API key not masked: %s", h.out)
	}

	h.env[EnvAPIKey] = "from-env-9999"
	h.run("config", "show", "--json")
	_ = json.Unmarshal(h.out.Bytes(), &s)
	if s["api_key_source"] != "env" {
		t.Errorf("env API key should win, got %s", s["api_key_source"])
	}

	if code := h.run("config", "unset", "api-url"); code != ExitOK {
		t.Fatalf("unset: exit %d", code)
	}
	h.run("config", "show", "--json")
	_ = json.Unmarshal(h.out.Bytes(), &s)
	if s["api_url"] != DefaultAPIURL {
		t.Errorf("api_url after unset = %q", s["api_url"])
	}
}

func TestConfigRejectsBadInput(t *testing.T) {
	h := newHarness(t, "")
	for _, args := range [][]string{
		{"config", "set", "api-url", "http://api.example.com"},
		{"config", "set", "api-url", "not a url"},
		{"config", "set", "colour", "blue"},
		{"config", "set", "api-key", "-"}, // empty stdin
		{"config", "set", "token", "abc"}, // old name
		{"config", "set"},
		{"no-such-command"},
	} {
		if code := h.run(args...); code != ExitUsage {
			t.Errorf("%v: exit %d, want %d (%s)", args, code, ExitUsage, h.errOut)
		}
	}
}

func TestStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok","version":"v9"}`))
	}))
	defer srv.Close()

	h := newHarness(t, "")
	if code := h.run("status", "--json", "--api-url", srv.URL); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.out)
	}
	var s map[string]string
	if err := json.Unmarshal(h.out.Bytes(), &s); err != nil || s["status"] != "ok" || s["server_version"] != "v9" {
		t.Errorf("output %q", h.out)
	}
}

func TestStatusUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	h := newHarness(t, "")
	if code := h.run("status", "--json", "--api-url", srv.URL); code != ExitError {
		t.Fatalf("exit %d, want %d", code, ExitError)
	}
	if h.out.Len() != 0 {
		t.Errorf("stdout should be empty on error, got %q", h.out)
	}
	var e map[string]map[string]string
	if err := json.Unmarshal(h.errOut.Bytes(), &e); err != nil || e["error"]["code"] != "http_503" {
		t.Errorf("error output %q", h.errOut)
	}
}

func TestStatusUnreachableIsPlainError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens there now

	h := newHarness(t, "")
	if code := h.run("status", "--api-url", url); code != ExitError {
		t.Fatalf("exit %d, want %d (3 is reserved for auth)", code, ExitError)
	}
}
