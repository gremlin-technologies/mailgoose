package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

// fakeKeyring is an in-memory keychain.
type fakeKeyring struct {
	items   map[string]string
	failSet bool
}

func newFakeKeyring() *fakeKeyring { return &fakeKeyring{items: map[string]string{}} }

func (k *fakeKeyring) Get(service, user string) (string, error) {
	v, ok := k.items[service+"|"+user]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return v, nil
}

func (k *fakeKeyring) Set(service, user, secret string) error {
	if k.failSet {
		return errors.New("no secret service")
	}
	k.items[service+"|"+user] = secret
	return nil
}

func (k *fakeKeyring) Delete(service, user string) error {
	if _, ok := k.items[service+"|"+user]; !ok {
		return keyring.ErrNotFound
	}
	delete(k.items, service+"|"+user)
	return nil
}

// deviceAPI fakes the device-login endpoints. polls is what each poll
// answers, in order; the last one repeats.
type deviceAPI struct {
	mu         sync.Mutex
	polls      []string
	pollCount  int
	clientName string
	revoked    []string
	revokeCode int
	tokenN     int
}

const (
	pollPending = "pending"
	pollSlow    = "slow_down"
	pollDenied  = "denied"
	pollExpired = "expired"
	pollToken   = "token"
)

func (d *deviceAPI) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		writeErr := func(status int, code string) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"code":"` + code + `","message":"m","hint":"h"}}`))
		}
		switch r.Method + " " + r.URL.Path {
		case "POST /v1/device-logins":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			d.clientName = body["client_name"]
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"device_code":"dev-secret","user_code":"BCDF-GHJK","verification_url":"https://app.example/device","verification_url_complete":"https://app.example/device?code=BCDF-GHJK","expires_in":600,"interval":5}`))
		case "POST /v1/device-logins/token":
			if r.Header.Get("Authorization") != "" {
				t.Error("poll should send no credential")
			}
			next := d.polls[min(d.pollCount, len(d.polls)-1)]
			d.pollCount++
			switch next {
			case pollPending:
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(`{"status":"pending","interval":5}`))
			case pollSlow:
				writeErr(http.StatusTooManyRequests, "slow_down")
			case pollDenied:
				writeErr(http.StatusForbidden, "device_login_denied")
			case pollExpired:
				writeErr(http.StatusGone, "device_login_expired")
			case pollToken:
				d.tokenN++
				tok := "mg_cli_token" + string(rune('0'+d.tokenN))
				_, _ = w.Write([]byte(`{"token":"` + tok + `","key_id":"11111111-1111-1111-1111-111111111111","account":{"id":"a","name":"Pat's Bakery","status":"active","plan":"paid","created_at":"2026-11-18T12:00:00Z"},"user":{"id":"u","email":"pat@example.com","created_at":"2026-11-18T12:00:00Z"}}`))
			}
		case "DELETE /v1/cli-tokens/current":
			d.revoked = append(d.revoked, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			if d.revokeCode != 0 {
				writeErr(d.revokeCode, "nope")
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case "GET /v1/whoami":
			auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			method := "api_key"
			if strings.HasPrefix(auth, "mg_cli_") {
				method = "cli_token"
			}
			_, _ = w.Write([]byte(`{"account":{"id":"a","name":"Pat's Bakery","status":"active","plan":"paid","created_at":"2026-11-18T12:00:00Z"},"user":{"id":"u","email":"pat@example.com","created_at":"2026-11-18T12:00:00Z"},"auth_method":"` + method + `","scope":"full","key_id":"` + auth + `"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// loginHarness is a harness at a terminal, with instant sleeps it records.
func loginHarness(t *testing.T, kr Keyring, apiURL string) (*harness, *[]time.Duration) {
	t.Helper()
	h := newHarness(t, "")
	var slept []time.Duration
	h.app.IsTerminal = func() bool { return true }
	h.app.Keyring = kr
	h.app.Hostname = func() (string, error) { return "pat-laptop", nil }
	h.app.Sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	h.env[EnvAPIURL] = apiURL
	return h, &slept
}

func TestLoginStoresTokenInKeychain(t *testing.T) {
	api := &deviceAPI{polls: []string{pollPending, pollSlow, pollToken}}
	srv := api.server(t)
	kr := newFakeKeyring()
	h, slept := loginHarness(t, kr, srv.URL)

	if code := h.run("login"); code != ExitOK {
		t.Fatalf("login: exit %d: %s", code, h.errOut)
	}
	out := h.out.String()
	for _, want := range []string{"BCDF-GHJK", "https://app.example/device?code=BCDF-GHJK", "10 minutes", "Signed in to Pat's Bakery as pat@example.com", "OS keychain"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if api.clientName != "pat-laptop" {
		t.Errorf("client_name = %q", api.clientName)
	}
	// 5s, then 5s again after pending, then 10s after slow_down.
	if want := []time.Duration{5 * time.Second, 5 * time.Second, 10 * time.Second}; !equalDurations(*slept, want) {
		t.Errorf("sleeps = %v, want %v", *slept, want)
	}
	if got, _ := kr.Get(keyringService, srv.URL); got != "mg_cli_token1" {
		t.Errorf("keychain token = %q", got)
	}
	raw, _ := os.ReadFile(h.env[EnvConfig])
	if strings.Contains(string(raw), "mg_cli_") {
		t.Errorf("config file holds the token though the keychain has it:\n%s", raw)
	}

	// Later commands use the stored login.
	if code := h.run("whoami", "--json"); code != ExitOK {
		t.Fatalf("whoami: exit %d: %s", code, h.errOut)
	}
	var who map[string]any
	if err := json.Unmarshal(h.out.Bytes(), &who); err != nil {
		t.Fatal(err)
	}
	if who["auth_method"] != "cli_token" || who["key_id"] != "mg_cli_token1" || who["credential_source"] != "login-keychain" {
		t.Errorf("whoami = %v", who)
	}
	if code := h.run("whoami"); code != ExitOK || !strings.Contains(h.out.String(), "pat@example.com") || !strings.Contains(h.out.String(), "cli_token, full scope") {
		t.Errorf("whoami text: exit %d\n%s", code, h.out)
	}
}

func TestLoginFallsBackToConfigFile(t *testing.T) {
	api := &deviceAPI{polls: []string{pollToken}}
	srv := api.server(t)
	for name, kr := range map[string]Keyring{"no keychain": nil, "keychain refuses": &fakeKeyring{items: map[string]string{}, failSet: true}} {
		t.Run(name, func(t *testing.T) {
			h, _ := loginHarness(t, kr, srv.URL)
			if code := h.run("login"); code != ExitOK {
				t.Fatalf("exit %d: %s", code, h.errOut)
			}
			if !strings.Contains(h.out.String(), "readable only by you") {
				t.Errorf("output = %s", h.out)
			}
			info, err := os.Stat(h.env[EnvConfig])
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Errorf("config mode = %v, want 0600", info.Mode().Perm())
			}
			fc, _ := LoadFile(h.env[EnvConfig])
			if l := fc.Logins[srv.URL]; l.Store != StoreFile || !strings.HasPrefix(l.Token, "mg_cli_") || l.Email != "pat@example.com" {
				t.Errorf("stored login = %+v", l)
			}
			if code := h.run("whoami", "--json"); code != ExitOK || !strings.Contains(h.out.String(), `"login-file"`) {
				t.Errorf("whoami: exit %d %s %s", code, h.out, h.errOut)
			}
		})
	}
}

func TestLoginWithoutTerminalPointsToAPIKey(t *testing.T) {
	api := &deviceAPI{polls: []string{pollToken}}
	srv := api.server(t)
	h, _ := loginHarness(t, newFakeKeyring(), srv.URL)
	h.app.IsTerminal = func() bool { return false }
	if code := h.run("login"); code != ExitUsage {
		t.Fatalf("exit %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(h.errOut.String(), EnvAPIKey) {
		t.Errorf("stderr should name %s: %s", EnvAPIKey, h.errOut)
	}
	if api.clientName != "" || api.pollCount != 0 {
		t.Error("login without a terminal should make no request")
	}
}

func TestLoginDeniedOrExpiredExits3(t *testing.T) {
	for poll, want := range map[string]string{pollDenied: "denied", pollExpired: "expired"} {
		t.Run(poll, func(t *testing.T) {
			api := &deviceAPI{polls: []string{pollPending, poll}}
			srv := api.server(t)
			kr := newFakeKeyring()
			h, _ := loginHarness(t, kr, srv.URL)
			if code := h.run("login", "--json"); code != ExitAuth {
				t.Fatalf("exit %d, want %d: %s", code, ExitAuth, h.errOut)
			}
			// Instructions go to stderr with --json, then the error.
			if !strings.Contains(h.errOut.String(), "BCDF-GHJK") || !strings.Contains(h.errOut.String(), want) {
				t.Errorf("stderr = %s", h.errOut)
			}
			if h.out.Len() != 0 {
				t.Errorf("stdout should be empty: %s", h.out)
			}
			if len(kr.items) != 0 {
				t.Error("nothing should be stored")
			}
		})
	}
}

func TestLoginJSONOutput(t *testing.T) {
	api := &deviceAPI{polls: []string{pollToken}}
	srv := api.server(t)
	h, _ := loginHarness(t, newFakeKeyring(), srv.URL)
	if code := h.run("login", "--json"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	var out map[string]any
	if err := json.Unmarshal(h.out.Bytes(), &out); err != nil {
		t.Fatalf("stdout is not JSON: %s", h.out)
	}
	if out["stored_in"] != StoreKeychain || strings.Contains(h.out.String(), "mg_cli_") {
		t.Errorf("login --json = %s", h.out)
	}
	if !strings.Contains(h.errOut.String(), "BCDF-GHJK") {
		t.Errorf("instructions should be on stderr: %s", h.errOut)
	}
}

func TestLoginAgainRevokesOldToken(t *testing.T) {
	api := &deviceAPI{polls: []string{pollToken}}
	srv := api.server(t)
	kr := newFakeKeyring()
	h, _ := loginHarness(t, kr, srv.URL)
	h.run("login")
	if code := h.run("login"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if len(api.revoked) != 1 || api.revoked[0] != "mg_cli_token1" {
		t.Errorf("revoked = %v, want the first token", api.revoked)
	}
	if got, _ := kr.Get(keyringService, srv.URL); got != "mg_cli_token2" {
		t.Errorf("keychain = %q", got)
	}
}

func TestLogout(t *testing.T) {
	t.Run("revokes and removes", func(t *testing.T) {
		api := &deviceAPI{polls: []string{pollToken}}
		srv := api.server(t)
		kr := newFakeKeyring()
		h, _ := loginHarness(t, kr, srv.URL)
		h.run("login")
		if code := h.run("logout", "--json"); code != ExitOK {
			t.Fatalf("exit %d: %s", code, h.errOut)
		}
		if len(api.revoked) != 1 || api.revoked[0] != "mg_cli_token1" {
			t.Errorf("revoked = %v", api.revoked)
		}
		if len(kr.items) != 0 {
			t.Errorf("keychain still holds %v", kr.items)
		}
		fc, _ := LoadFile(h.env[EnvConfig])
		if len(fc.Logins) != 0 {
			t.Errorf("config still has logins %v", fc.Logins)
		}
		if !strings.Contains(h.out.String(), `"revoked": true`) {
			t.Errorf("logout --json = %s", h.out)
		}
		// Signed out: nothing to do, and later commands have no credential.
		if code := h.run("logout"); code != ExitOK || !strings.Contains(h.out.String(), "nothing to do") {
			t.Errorf("second logout: exit %d %s", code, h.out)
		}
		if code := h.run("whoami"); code != ExitAuth {
			t.Errorf("whoami after logout: exit %d", code)
		}
	})

	t.Run("token already invalid", func(t *testing.T) {
		api := &deviceAPI{polls: []string{pollToken}}
		srv := api.server(t)
		h, _ := loginHarness(t, nil, srv.URL)
		h.run("login")
		api.revokeCode = http.StatusUnauthorized
		if code := h.run("logout"); code != ExitOK || !strings.Contains(h.out.String(), "already revoked") {
			t.Errorf("exit %d: %s %s", code, h.out, h.errOut)
		}
		fc, _ := LoadFile(h.env[EnvConfig])
		if len(fc.Logins) != 0 {
			t.Error("login should be removed")
		}
	})

	t.Run("server error keeps the token", func(t *testing.T) {
		api := &deviceAPI{polls: []string{pollToken}}
		srv := api.server(t)
		kr := newFakeKeyring()
		h, _ := loginHarness(t, kr, srv.URL)
		h.run("login")
		api.revokeCode = http.StatusInternalServerError
		if code := h.run("logout"); code != ExitError || !strings.Contains(h.errOut.String(), "still stored") {
			t.Errorf("exit %d: %s", code, h.errOut)
		}
		if len(kr.items) != 1 {
			t.Error("token should stay in the keychain")
		}
	})
}

func TestEnvAPIKeyWinsOverLogin(t *testing.T) {
	api := &deviceAPI{polls: []string{pollToken}}
	srv := api.server(t)
	h, _ := loginHarness(t, newFakeKeyring(), srv.URL)
	h.run("login")
	h.env[EnvAPIKey] = "mg_live_envkey"
	if code := h.run("whoami", "--json"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if !strings.Contains(h.out.String(), `"key_id": "mg_live_envkey"`) || !strings.Contains(h.out.String(), `"credential_source": "env"`) {
		t.Errorf("whoami = %s", h.out)
	}
	// logout touches only the login, never the env key.
	h.run("logout")
	if len(api.revoked) != 1 || api.revoked[0] != "mg_cli_token1" || !strings.Contains(h.out.String(), "still set") {
		t.Errorf("revoked %v, output %s", api.revoked, h.out)
	}
}

func TestLoginBeatsConfigAPIKey(t *testing.T) {
	fc := FileConfig{APIKey: "mg_live_file", Logins: map[string]Login{"https://api.example": {Store: StoreFile, Token: "mg_cli_x"}}}
	env := func(string) string { return "" }
	s := Resolve("https://api.example", env, "p", fc)
	if s.APIKey != "mg_cli_x" || s.APIKeySource != "login-file" {
		t.Errorf("resolve = %+v", s)
	}
	// A login for another API URL doesn't apply.
	s = Resolve("https://api.staging.example", env, "p", fc)
	if s.APIKey != "mg_live_file" || s.APIKeySource != "file" {
		t.Errorf("other URL resolve = %+v", s)
	}
}

func TestWantsJSON(t *testing.T) {
	for args, want := range map[string]bool{
		"status --json":       true,
		"status --json=true":  true,
		"status --json=1":     true,
		"status --json=false": false,
		"status":              false,
		"status -- --json":    false,
	} {
		if got := wantsJSON(strings.Fields(args)); got != want {
			t.Errorf("wantsJSON(%q) = %v", args, got)
		}
	}
	h := newHarness(t, "")
	h.run("status", "--json=true", "--bogus")
	if !json.Valid(h.errOut.Bytes()) {
		t.Errorf("--json=true flag error should be JSON: %s", h.errOut)
	}
}

func equalDurations(a, b []time.Duration) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
