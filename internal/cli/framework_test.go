package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// apiStub answers each path with a fixed status and body.
func apiStub(t *testing.T, routes map[string]func(w http.ResponseWriter)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f, ok := routes[r.URL.Path]; ok {
			f(w)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"No such thing.","hint":""}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func apiError(status int, code string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"code":"` + code + `","message":"m","hint":"h"}}`))
	}
}

// probe is a test-only command that calls GET /probe/<arg> with the API key.
func (h *harness) withProbe() {
	app := h.app
	h.app.extra = []*cobra.Command{calls("getProbe", &cobra.Command{
		Use:  "probe <path>",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.call(cmd.Context(), http.MethodGet, "/probe/"+args[0], nil, nil, true)
		},
	})}
}

func TestExitCodes(t *testing.T) {
	srv := apiStub(t, map[string]func(http.ResponseWriter){
		"/healthz":       func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"status":"ok","version":"v1"}`)) },
		"/probe/ok":      func(w http.ResponseWriter) { w.WriteHeader(200) },
		"/probe/broken":  apiError(500, "internal"),
		"/probe/denied":  apiError(401, "unauthenticated"),
		"/probe/scope":   apiError(403, "insufficient_scope"),
		"/probe/confirm": apiError(409, "confirmation_required"),
	})
	h := newHarness(t, "")
	h.withProbe()
	h.env[EnvAPIURL] = srv.URL
	h.env[EnvAPIKey] = "mg_test_abc"

	cases := []struct {
		args []string
		want int
	}{
		{[]string{"version"}, ExitOK},
		{[]string{"probe", "ok"}, ExitOK},
		{[]string{"probe", "broken"}, ExitError},
		{[]string{"status", "--nope"}, ExitUsage},
		{[]string{"probe"}, ExitUsage},
		{[]string{"probe", "denied"}, ExitAuth},
		{[]string{"probe", "scope"}, ExitAuth},
		{[]string{"probe", "missing"}, ExitNotFound},
		{[]string{"probe", "confirm"}, ExitNeedsConfirmation},
	}
	for _, tc := range cases {
		if got := h.run(tc.args...); got != tc.want {
			t.Errorf("%v: exit %d, want %d (%s)", tc.args, got, tc.want, h.errOut)
		}
	}

	// No API key at all is an auth error before any request.
	delete(h.env, EnvAPIKey)
	if got := h.run("probe", "ok"); got != ExitAuth {
		t.Errorf("no key: exit %d, want %d", got, ExitAuth)
	}
}

func TestJSONErrorsGoToStderrInAPIShape(t *testing.T) {
	srv := apiStub(t, map[string]func(http.ResponseWriter){"/probe/denied": apiError(401, "unauthenticated")})
	h := newHarness(t, "")
	h.withProbe()
	h.env[EnvAPIKey] = "k"

	for _, args := range [][]string{
		{"probe", "denied", "--json", "--api-url", srv.URL},
		{"status", "--json", "--bogus"},       // flag error before --json is parsed
		{"config", "set", "colour", "--json"}, // CLI-raised usage error
	} {
		h.run(args...)
		if h.out.Len() != 0 {
			t.Errorf("%v: stdout should be empty, got %q", args, h.out)
		}
		var body map[string]map[string]string
		if err := json.Unmarshal(h.errOut.Bytes(), &body); err != nil {
			t.Fatalf("%v: stderr is not JSON: %q", args, h.errOut)
		}
		e := body["error"]
		if len(body) != 1 || len(e) != 3 || e["code"] == "" || e["message"] == "" {
			t.Errorf("%v: error shape %v", args, body)
		}
		if _, ok := e["hint"]; !ok {
			t.Errorf("%v: hint missing", args)
		}
	}
}

func TestPlainErrorsShowHint(t *testing.T) {
	h := newHarness(t, "")
	h.run("config", "set", "colour", "blue")
	if !strings.Contains(h.errOut.String(), "Error: Unknown setting") || !strings.Contains(h.errOut.String(), "Hint: Use api-url or api-key.") {
		t.Errorf("stderr = %q", h.errOut)
	}
}

func TestExitCodesHelpTopic(t *testing.T) {
	h := newHarness(t, "")
	if code := h.run("help", "exit-codes"); code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{"0  ok", "2  usage", "3  auth", "4  not found", "5  needs confirmation"} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("help exit-codes lacks %q", want)
		}
	}
}

func TestVersionShowsCommitAndBuildDate(t *testing.T) {
	h := newHarness(t, "")
	h.run("version")
	for _, want := range []string{"mailgoose ", "commit ", "built "} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("version output %q lacks %q", h.out, want)
		}
	}
}

// TestRegistry checks every command says which API operation it calls.
func TestRegistry(t *testing.T) {
	reg := NewApp().Commands()
	if len(reg) == 0 {
		t.Fatal("empty registry")
	}
	seen := map[string]string{}
	for _, c := range reg {
		if !c.Declared {
			t.Errorf("%q doesn't declare its API operation; wrap it in calls(...) or local(...)", c.Command)
		}
		seen[c.Command] = c.Operation
	}
	if seen["status"] != "getHealth" {
		t.Errorf("status should call getHealth, registry says %q", seen["status"])
	}
	if op, ok := seen["config show"]; !ok || op != "" {
		t.Errorf("config show should be local: %q %v", op, ok)
	}

	h := newHarness(t, "")
	if code := h.run("commands", "--json"); code != ExitOK {
		t.Fatalf("commands: exit %d", code)
	}
	var listed []CommandInfo
	if err := json.Unmarshal(h.out.Bytes(), &listed); err != nil || len(listed) != len(reg) {
		t.Errorf("commands --json = %s", h.out)
	}
}

// TestNoCommandWaitsForInput runs every command with stdin closed and no
// terminal: each must finish within 5 seconds, so an agent never hangs.
func TestNoCommandWaitsForInput(t *testing.T) {
	srv := apiStub(t, map[string]func(http.ResponseWriter){
		"/healthz":     func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"status":"ok","version":"v1"}`)) },
		"/v1/api-keys": func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"data":[],"next_cursor":null}`)) },
		"/v1/credits": func(w http.ResponseWriter) {
			_, _ = w.Write([]byte(`{"balance":0,"plan":"paid","next_grant_at":null}`))
		},
		"/v1/account": func(w http.ResponseWriter) {
			_, _ = w.Write([]byte(`{"id":"x","name":"A","plan":"free","from_name":null,"from_email":null,"postal_address":null,"time_zone":"UTC","missing_for_send":[],"created_at":"2026-11-18T12:00:00Z"}`))
		},
		"/v1/credits/transactions": func(w http.ResponseWriter) {
			_, _ = w.Write([]byte(`{"data":[],"next_cursor":null}`))
		},
		"/v1/contacts":  func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"data":[],"next_cursor":null}`)) },
		"/v1/domains":   func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"data":[],"next_cursor":null}`)) },
		"/v1/tags":      func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"data":[],"next_cursor":null}`)) },
		"/v1/imports":   func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"data":[],"next_cursor":null}`)) },
		"/v1/emails":    func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"data":[],"next_cursor":null}`)) },
		"/v1/forms":     func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"data":[],"next_cursor":null}`)) },
		"/v1/campaigns": func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"data":[],"next_cursor":null}`)) },
		"/v1/contacts/export": func(w http.ResponseWriter) {
			_, _ = w.Write([]byte("email,first_name,last_name,status,tags,source,created_at,status_changed_at,permission_confirmed\n"))
		},
		"/v1/whoami": func(w http.ResponseWriter) {
			_, _ = w.Write([]byte(`{"account":{"id":"x","name":"A","status":"active","plan":"free","created_at":"2026-11-18T12:00:00Z"},"user":null,"auth_method":"api_key","scope":"full","key_id":"k"}`))
		},
	})
	for _, c := range NewApp().Commands() {
		t.Run(c.Command, func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			_ = w.Close()
			defer func() { _ = r.Close() }()

			h := newHarness(t, "")
			h.app.Stdin = r
			h.app.IsTerminal = func() bool { return false }
			h.env[EnvAPIURL] = srv.URL
			h.env[EnvAPIKey] = "mg_test_abc"

			done := make(chan int, 1)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			go func() {
				a := *h.app
				done <- a.Run(ctx, append(strings.Fields(c.Command), "--json"))
			}()
			select {
			case code := <-done:
				if code != ExitOK && code != ExitUsage {
					t.Errorf("exit %d: %s", code, h.errOut)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("still running after 5 seconds with stdin closed")
			}
		})
	}
}

func TestNeedFlag(t *testing.T) {
	e := needFlag("confirm", "A confirmation code")
	if e.Exit != ExitUsage || !strings.Contains(e.Hint, "--confirm") {
		t.Errorf("needFlag = %+v", e)
	}
}
