package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type deviceStart struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURL         string `json:"verification_url"`
	VerificationURLComplete string `json:"verification_url_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// deviceLogins holds each test's started device login, made by
// startedDeviceLogin.
var deviceLogins sync.Map // *testing.T -> deviceStart

// startedDeviceLogin starts a device login through h and keeps it for
// pollRequest and userCodePath.
func startedDeviceLogin(t *testing.T, h http.Handler) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, jsonRequest(http.MethodPost, "/v1/device-logins", `{"client_name":"pat-laptop"}`)(t))
	if rec.Code != http.StatusCreated {
		t.Fatalf("start device login: %d %s", rec.Code, rec.Body)
	}
	var d deviceStart
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	deviceLogins.Store(t, d)
	t.Cleanup(func() { deviceLogins.Delete(t) })
}

func startedForSignedIn(t *testing.T, h http.Handler) {
	signedIn(t, h)
	startedDeviceLogin(t, h)
}

func then(steps ...func(*testing.T, http.Handler)) func(*testing.T, http.Handler) {
	return func(t *testing.T, h http.Handler) {
		for _, s := range steps {
			s(t, h)
		}
	}
}

func started(t *testing.T) deviceStart {
	v, ok := deviceLogins.Load(t)
	if !ok {
		return deviceStart{DeviceCode: "no-such-device-code", UserCode: "BBBB-BBBB"}
	}
	return v.(deviceStart)
}

// pollRequest polls the test's device login.
func pollRequest(t *testing.T) *http.Request {
	return jsonRequest(http.MethodPost, "/v1/device-logins/token", `{"device_code":"`+started(t).DeviceCode+`"}`)(t)
}

// userCodePath is /v1/device-logins/<the test's user code><suffix>.
func userCodePath(method, suffix string) func(*testing.T) *http.Request {
	return func(t *testing.T) *http.Request {
		return httptest.NewRequest(method, "/v1/device-logins/"+started(t).UserCode+suffix, nil)
	}
}

// withCLIToken wraps a server constructor: it also makes an active account
// with a CLI token, kept for bearer.
func withCLIToken(server func(*testing.T) *Server) func(*testing.T) *Server {
	return func(t *testing.T) *Server {
		t.Helper()
		s := server(t)
		ctx := context.Background()
		pool := s.DB.(*pgxpool.Pool)
		var accountID, userID string
		if err := pool.QueryRow(ctx, `INSERT INTO accounts (name, status) VALUES ('Test', 'active') RETURNING id`).Scan(&accountID); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `INSERT INTO users (account_id, email) VALUES ($1, 'cli@example.com') RETURNING id`, accountID).Scan(&userID); err != nil {
			t.Fatal(err)
		}
		k, err := s.Auth.CreateCLIToken(ctx, accountID, userID, "test")
		if err != nil {
			t.Fatal(err)
		}
		testKeys.Store(t, testKey{id: k.ID, secret: k.Secret})
		t.Cleanup(func() { testKeys.Delete(t) })
		return s
	}
}

// TestDeviceLogin runs the whole flow: start, poll while pending, approve
// in a web session, collect the token once, use it, sign out.
func TestDeviceLogin(t *testing.T) {
	s := authServer(t)
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	s.Auth.Now = func() time.Time { return now }
	h := s.Handler()
	signedIn(t, h)
	startedDeviceLogin(t, h)
	d := started(t)

	if len(d.UserCode) != 9 || d.UserCode[4] != '-' || d.ExpiresIn != 600 || d.Interval != 5 ||
		!strings.HasSuffix(d.VerificationURL, "/device") || !strings.Contains(d.VerificationURLComplete, "?code="+d.UserCode) {
		t.Errorf("start = %+v", d)
	}

	if rec := do(t, h, pollRequest(t)); rec.Code != http.StatusAccepted {
		t.Fatalf("first poll: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, pollRequest(t)); rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), "slow_down") {
		t.Errorf("immediate second poll: %d %s", rec.Code, rec.Body)
	}

	// The approval page finds the request however the code is typed.
	typed := strings.ToLower(strings.ReplaceAll(d.UserCode, "-", " "))
	rec := do(t, h, withSession(request(http.MethodGet, "/v1/device-logins/"+strings.ReplaceAll(typed, " ", "%20")))(t))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"client_name":"pat-laptop"`) {
		t.Fatalf("get: %d %s", rec.Code, rec.Body)
	}
	// API keys and CLI tokens can't approve.
	if rec := do(t, h, bearer(userCodePath(http.MethodPost, "/approve"))(t)); rec.Code != http.StatusUnauthorized {
		t.Errorf("approve with a bearer token: %d", rec.Code)
	}
	if rec := do(t, h, withSession(userCodePath(http.MethodPost, "/approve"))(t)); rec.Code != http.StatusNoContent {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, withSession(userCodePath(http.MethodPost, "/approve"))(t)); rec.Code != http.StatusNotFound {
		t.Errorf("approve twice: %d", rec.Code)
	}

	now = now.Add(5 * time.Second)
	rec = do(t, h, pollRequest(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("poll after approval: %d %s", rec.Code, rec.Body)
	}
	var tok struct {
		Token string `json:"token"`
		User  struct {
			Email string `json:"email"`
		} `json:"user"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &tok)
	if !strings.HasPrefix(tok.Token, "mg_cli_") || tok.User.Email != "pat@example.com" {
		t.Errorf("token = %+v", tok)
	}
	now = now.Add(5 * time.Second)
	if rec := do(t, h, pollRequest(t)); rec.Code != http.StatusGone {
		t.Errorf("poll after collecting: %d", rec.Code)
	}

	withToken := func(r *http.Request) *http.Request {
		r.Header.Set("Authorization", "Bearer "+tok.Token)
		return r
	}
	rec = do(t, h, withToken(request(http.MethodGet, "/v1/whoami")(t)))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"auth_method":"cli_token"`) || !strings.Contains(rec.Body.String(), "pat@example.com") {
		t.Fatalf("whoami: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, withToken(request(http.MethodDelete, "/v1/cli-tokens/current")(t))); rec.Code != http.StatusNoContent {
		t.Fatalf("logout: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, withToken(request(http.MethodGet, "/v1/whoami")(t))); rec.Code != http.StatusUnauthorized {
		t.Errorf("whoami after logout: %d", rec.Code)
	}
}

func TestDeviceLoginDeniedAndExpired(t *testing.T) {
	s := authServer(t)
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	s.Auth.Now = func() time.Time { return now }
	h := s.Handler()
	signedIn(t, h)

	startedDeviceLogin(t, h)
	if rec := do(t, h, withSession(userCodePath(http.MethodPost, "/deny"))(t)); rec.Code != http.StatusNoContent {
		t.Fatalf("deny: %d", rec.Code)
	}
	if rec := do(t, h, pollRequest(t)); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "device_login_denied") {
		t.Errorf("poll after deny: %d %s", rec.Code, rec.Body)
	}

	startedDeviceLogin(t, h)
	now = now.Add(10 * time.Minute)
	if rec := do(t, h, withSession(userCodePath(http.MethodPost, "/approve"))(t)); rec.Code != http.StatusNotFound {
		t.Errorf("approve after 10 minutes: %d", rec.Code)
	}
	if rec := do(t, h, pollRequest(t)); rec.Code != http.StatusGone {
		t.Errorf("poll after 10 minutes: %d", rec.Code)
	}
}

func TestWhoamiWithKeyAndSession(t *testing.T) {
	h := fullKey(authServer)(t).Handler()
	rec := do(t, h, bearer(request(http.MethodGet, "/v1/whoami"))(t))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"auth_method":"api_key"`) || !strings.Contains(rec.Body.String(), "key@example.com") {
		t.Errorf("api key: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, bearer(request(http.MethodDelete, "/v1/cli-tokens/current"))(t)); rec.Code != http.StatusBadRequest {
		t.Errorf("logout with an API key: %d", rec.Code)
	}
	signedIn(t, h)
	rec = do(t, h, withSession(request(http.MethodGet, "/v1/whoami"))(t))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"auth_method":"session"`) || !strings.Contains(rec.Body.String(), `"key_id":null`) {
		t.Errorf("session: %d %s", rec.Code, rec.Body)
	}
}
