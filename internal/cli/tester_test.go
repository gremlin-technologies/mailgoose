package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAccountShowsTester: `mailgoose account get` shows a tester account's
// credits left and its end (MG-86).
func TestAccountShowsTester(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"a1","name":"QA","plan":"free","time_zone":"UTC","missing_for_send":[],"created_at":"2026-11-18T12:00:00Z",` +
			`"send_limit":{"daily":100,"usual":10000,"reason":"Tester accounts send to at most 100 people a day.","lowered_until":null},` +
			`"terms":{"accepted_version":"2026-11-19","accepted_at":"2026-11-18T12:00:00Z","current_version":"2026-11-19",` +
			`"current_effective":"2026-11-19","acceptance_needed":false},"sending_mode":"full",` +
			`"tester":{"started_at":"2026-11-18T12:00:00Z","until":"2026-12-18T12:00:00Z","credits":500,"used":120,"left":380,"ended":false}}`))
	}))
	t.Cleanup(srv.Close)
	h := newHarness(t, "")
	h.env[EnvAPIURL] = srv.URL
	h.env[EnvAPIKey] = "mg_test_key"
	if code := h.run("account", "get"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	for _, want := range []string{"Tester:      Tester account: 380 of 500 credits left, until Dec 18, 2026",
		"simulator.amazonses.com use no credits", "Tester accounts send to at most 100 people a day."} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, h.out)
		}
	}
}

// TestAdminTesterCommands: `mailgoose admin tester grant` and `revoke` send
// the end, credits and reason, with defaults, and refuse bad input.
func TestAdminTesterCommands(t *testing.T) {
	bodies := map[string]map[string]any{}
	h := adminStub(t, bodies)
	if code := h.run("admin", "tester", "grant", testAccountID); code != ExitOK {
		t.Fatalf("grant: exit %d: %s", code, h.errOut)
	}
	if !strings.Contains(h.out.String(), "Pat's Bakery ("+testAccountID+") is a tester account.") {
		t.Errorf("grant output:\n%s", h.out)
	}
	b := bodies["/v1/admin/accounts/"+testAccountID+"/grant-tester"]
	if len(b) != 1 || b["reason"] != "Invited to test mailgoose." {
		t.Errorf("default grant body = %v", b)
	}
	if code := h.run("admin", "tester", "grant", testAccountID, "--until", "2026-12-31", "--credits", "800", "--reason", "Beta"); code != ExitOK {
		t.Fatalf("grant with flags: exit %d: %s", code, h.errOut)
	}
	b = bodies["/v1/admin/accounts/"+testAccountID+"/grant-tester"]
	if b["until"] != "2026-12-31T23:59:59Z" || b["credits"] != float64(800) || b["reason"] != "Beta" {
		t.Errorf("grant body = %v", b)
	}
	if code := h.run("admin", "tester", "revoke", testAccountID); code != ExitOK {
		t.Fatalf("revoke: exit %d: %s", code, h.errOut)
	}
	if !strings.Contains(h.out.String(), "no longer a tester account") {
		t.Errorf("revoke output:\n%s", h.out)
	}
	if b := bodies["/v1/admin/accounts/"+testAccountID+"/revoke-tester"]; b["reason"] != "Testing finished." {
		t.Errorf("revoke body = %v", b)
	}
	for _, args := range [][]string{
		{"admin", "tester", "grant", testAccountID, "--until", "soon"},
		{"admin", "tester", "grant", "pats-bakery"},
		{"admin", "tester", "grant", testAccountID, "--reason", " "},
		{"admin", "tester", "revoke"},
	} {
		if code := h.run(args...); code != ExitUsage {
			t.Errorf("%v: exit %d", args, code)
		}
	}
}
