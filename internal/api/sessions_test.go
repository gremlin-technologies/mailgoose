package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/auth"
)

// do sends req through h and returns the recorder.
func do(t *testing.T, h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestSignOut(t *testing.T) {
	h := authServer(t).Handler()
	signedIn(t, h)
	current := withSession(request(http.MethodGet, "/v1/sessions/current"))

	if rec := do(t, h, current(t)); rec.Code != http.StatusOK {
		t.Fatalf("signed in: status %d: %s", rec.Code, rec.Body)
	}
	rec := do(t, h, withSession(request(http.MethodDelete, "/v1/sessions/current"))(t))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("sign out: status %d: %s", rec.Code, rec.Body)
	}
	var cleared bool
	for _, c := range rec.Result().Cookies() {
		cleared = cleared || (c.Name == SessionCookie && c.MaxAge < 0)
	}
	if !cleared {
		t.Error("sign out didn't clear the cookie")
	}
	if rec := do(t, h, current(t)); rec.Code != http.StatusUnauthorized {
		t.Errorf("after sign out: status %d, want 401", rec.Code)
	}
}

func TestSignOutEverywhere(t *testing.T) {
	srv := authServer(t)
	now := time.Now()
	srv.Auth.Now = func() time.Time { return now }
	h := srv.Handler()
	signedIn(t, h)
	first, _ := sessionCookies.Load(t)

	// A second session for the same person, from a sign-in code.
	now = now.Add(auth.ResendInterval)
	if rec := do(t, h, jsonRequest(http.MethodPost, "/v1/sign-in", signInBody)(t)); rec.Code != http.StatusAccepted {
		t.Fatalf("sign-in request: status %d", rec.Code)
	}
	second := do(t, h, sessionRequest("")(t))
	if second.Code != http.StatusCreated {
		t.Fatalf("second sign-in: status %d: %s", second.Code, second.Body)
	}

	rec := do(t, h, withSession(request(http.MethodDelete, "/v1/sessions"))(t))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("sign out everywhere: status %d: %s", rec.Code, rec.Body)
	}
	for i, c := range append([]*http.Cookie{first.(*http.Cookie)}, second.Result().Cookies()...) {
		r := request(http.MethodGet, "/v1/sessions/current")(t)
		r.AddCookie(c)
		if rec := do(t, h, r); rec.Code != http.StatusUnauthorized {
			t.Errorf("session %d after sign out everywhere: status %d", i+1, rec.Code)
		}
	}
}

func TestCodeRequestLimitAnswers429(t *testing.T) {
	h := authServer(t).Handler()
	var rec *httptest.ResponseRecorder
	for range auth.MaxCodeRequestsPerEmail + 1 {
		rec = do(t, h, jsonRequest(http.MethodPost, "/v1/sign-in", signInBody)(t))
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429: %s", rec.Code, rec.Body)
	}
	secs, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	if err != nil || secs < 1 || secs > int(auth.CodeRequestWindow.Seconds()) {
		t.Errorf("Retry-After = %q", rec.Header().Get("Retry-After"))
	}
}

// TestCrossOriginProtection covers who may send a cookie-authenticated
// request that changes something.
func TestCrossOriginProtection(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	protected := (&Server{Log: slog.New(slog.DiscardHandler), TrustedOrigins: []string{"https://app.example"}}).crossOriginProtection(ok)

	for name, tc := range map[string]struct {
		method  string
		headers map[string]string
		want    int
	}{
		"same origin":               {http.MethodDelete, map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "https://api.example"}, http.StatusNoContent},
		"trusted web app origin":    {http.MethodDelete, map[string]string{"Sec-Fetch-Site": "same-site", "Origin": "https://app.example"}, http.StatusNoContent},
		"another site":              {http.MethodDelete, map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://attacker.example"}, http.StatusForbidden},
		"another site, Origin only": {http.MethodPost, map[string]string{"Origin": "https://attacker.example"}, http.StatusForbidden},
		"same site, untrusted":      {http.MethodDelete, map[string]string{"Sec-Fetch-Site": "same-site", "Origin": "https://other.example"}, http.StatusForbidden},
		"non-browser client":        {http.MethodDelete, nil, http.StatusNoContent},
		"API key from another site": {http.MethodDelete, map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://attacker.example", "Authorization": "Bearer k"}, http.StatusNoContent},
		"read from another site":    {http.MethodGet, map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://attacker.example"}, http.StatusNoContent},
	} {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "https://api.example/v1/sessions", nil)
			for k, v := range tc.headers {
				r.Header.Set(k, v)
			}
			if rec := do(t, protected, r); rec.Code != tc.want {
				t.Errorf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body)
			}
		})
	}
}
