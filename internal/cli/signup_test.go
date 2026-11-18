package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// signupAPI fakes the sign-up endpoints.
type signupAPI struct {
	mu        sync.Mutex
	signups   []map[string]any
	tokenReqs []map[string]string
}

func (s *signupAPI) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if r.Header.Get("Authorization") != "" {
			t.Errorf("%s %s sent a credential", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /v1/signup":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			s.signups = append(s.signups, body)
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"status":"code_sent","message":"Check your email."}`))
		case "POST /v1/cli-tokens":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			s.tokenReqs = append(s.tokenReqs, body)
			if body["code"] != "042917" {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(`{"error":{"code":"invalid_code","message":"That code is wrong, expired or already used.","hint":"Ask for a new code."}}`))
				return
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"token":"mg_cli_signup","key_id":"11111111-1111-1111-1111-111111111111","account":{"id":"a","name":"Pat's Bakery","status":"active","plan":"free","created_at":"2026-11-18T12:00:00Z"},"user":{"id":"u","email":"pat@example.com","created_at":"2026-11-18T12:00:00Z"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func signupHarness(t *testing.T, apiURL string) (*harness, *fakeKeyring) {
	t.Helper()
	h := newHarness(t, "")
	kr := newFakeKeyring()
	h.app.Keyring = kr
	h.app.Hostname = func() (string, error) { return "pat-laptop", nil }
	// No terminal: an agent runs both steps.
	h.app.IsTerminal = func() bool { return false }
	h.env[EnvAPIURL] = apiURL
	return h, kr
}

func TestSignupTwoSteps(t *testing.T) {
	api := &signupAPI{}
	srv := api.server(t)
	h, kr := signupHarness(t, srv.URL)

	if code := h.run("signup", "--email", "pat@example.com", "--account-name", "Pat's Bakery", "--accept-terms"); code != ExitOK {
		t.Fatalf("step 1: exit %d: %s", code, h.errOut)
	}
	if len(api.signups) != 1 || api.signups[0]["email"] != "pat@example.com" || api.signups[0]["account_name"] != "Pat's Bakery" || api.signups[0]["accepted_terms"] != true {
		t.Fatalf("signup requests = %v", api.signups)
	}
	if out := h.out.String(); !strings.Contains(out, "mailgoose signup --email pat@example.com --code <code>") || !strings.Contains(out, "10 minutes") {
		t.Errorf("step 1 output:\n%s", out)
	}
	if _, err := kr.Get(keyringService, srv.URL); err == nil {
		t.Error("step 1 stored a token")
	}

	if code := h.run("signup", "--email", "pat@example.com", "--code", " 042917 "); code != ExitOK {
		t.Fatalf("step 2: exit %d: %s", code, h.errOut)
	}
	if len(api.tokenReqs) != 1 || api.tokenReqs[0]["code"] != "042917" || api.tokenReqs[0]["client_name"] != "pat-laptop" {
		t.Errorf("token requests = %v", api.tokenReqs)
	}
	if out := h.out.String(); !strings.Contains(out, "Signed in to Pat's Bakery as pat@example.com") || !strings.Contains(out, "OS keychain") {
		t.Errorf("step 2 output:\n%s", out)
	}
	if got, _ := kr.Get(keyringService, srv.URL); got != "mg_cli_signup" {
		t.Errorf("keychain token = %q", got)
	}
}

func TestSignupJSON(t *testing.T) {
	api := &signupAPI{}
	srv := api.server(t)
	h, _ := signupHarness(t, srv.URL)

	if code := h.run("signup", "--json", "--email", "pat@example.com", "--account-name", "Pat's Bakery", "--accept-terms"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	var sent map[string]any
	if err := json.Unmarshal(h.out.Bytes(), &sent); err != nil {
		t.Fatalf("%v: %s", err, h.out)
	}
	if sent["status"] != "code_sent" || sent["next"] != "mailgoose signup --email pat@example.com --code <code>" {
		t.Errorf("step 1 = %v", sent)
	}

	if code := h.run("signup", "--json", "--email", "pat@example.com", "--code", "042917"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	var done map[string]any
	if err := json.Unmarshal(h.out.Bytes(), &done); err != nil {
		t.Fatalf("%v: %s", err, h.out)
	}
	if done["key_id"] != "11111111-1111-1111-1111-111111111111" || done["stored_in"] != StoreKeychain {
		t.Errorf("step 2 = %v", done)
	}
}

func TestSignupErrors(t *testing.T) {
	api := &signupAPI{}
	srv := api.server(t)
	h, kr := signupHarness(t, srv.URL)

	for name, tc := range map[string]struct {
		args []string
		exit int
		want string
	}{
		"no email":         {[]string{"signup", "--account-name", "Pat", "--accept-terms"}, ExitUsage, "--email"},
		"no account name":  {[]string{"signup", "--email", "pat@example.com", "--accept-terms"}, ExitUsage, "--account-name"},
		"terms not given":  {[]string{"signup", "--email", "pat@example.com", "--account-name", "Pat"}, ExitUsage, "--accept-terms"},
		"code with name":   {[]string{"signup", "--email", "pat@example.com", "--code", "042917", "--account-name", "Pat"}, ExitUsage, "--code"},
		"wrong code":       {[]string{"signup", "--email", "pat@example.com", "--code", "111111"}, ExitError, "That code is wrong"},
		"extra positional": {[]string{"signup", "pat@example.com"}, ExitUsage, ""},
	} {
		t.Run(name, func(t *testing.T) {
			if code := h.run(tc.args...); code != tc.exit {
				t.Errorf("exit %d, want %d: %s", code, tc.exit, h.errOut)
			}
			if !strings.Contains(h.errOut.String(), tc.want) {
				t.Errorf("stderr lacks %q:\n%s", tc.want, h.errOut)
			}
		})
	}
	if len(api.signups) != 0 {
		t.Errorf("usage errors reached the API: %v", api.signups)
	}
	if _, err := kr.Get(keyringService, srv.URL); err == nil {
		t.Error("a failed sign-up stored a token")
	}
}
