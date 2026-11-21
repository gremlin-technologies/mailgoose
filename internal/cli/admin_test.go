package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testAccountID = "3f1c2b4a-5d6e-4f70-8a9b-0c1d2e3f4a5b"

// adminStub answers the admin endpoints and records each request's body.
func adminStub(t *testing.T, bodies map[string]map[string]any) *harness {
	t.Helper()
	account := `{"id":"` + testAccountID + `","name":"Pat's Bakery","status":"active","plan":"paid","owner_email":"pat@example.com",` +
		`"sending_paused_at":"2026-11-18T10:00:00Z","sending_paused_reason":"Phishing reports",` +
		`"suspended_at":"2026-11-19T08:00:00Z","suspended_reason":"Bought list","suspended_by":"staff@example.com",` +
		`"review":"waiting","review_flags":["disposable_domain","ip_burst"],"review_at":"2026-11-01T09:05:00Z","sending_mode":"test","test_addresses_confirmed":2,"test_addresses_pending":1,"created_at":"2026-11-01T09:00:00Z",` +
		`"daily_send_limit":2000,"send_limit":{"daily":1000,"usual":10000,"reason":"Lowered automatically.","lowered_until":"2026-11-25T10:00:00Z"},` +
		`"throttles":[{"id":3,"campaign_id":"camp-1","reason":"Lowered automatically: 0.2% marked it as spam.","created_at":"2026-11-18T10:00:00Z","until":"2026-11-25T10:00:00Z"}],` +
		`"contacts":1200,"credit_balance":9000,"tester":{"started_at":"2026-11-18T12:00:00Z","until":"2026-12-18T12:00:00Z","credits":500,"used":120,"left":380,"ended":false},"last_30_days":{"sent":1000,"hard_bounces":12,"complaints":4},` +
		`"held_imports":[{"id":"imp-1","file_name":null,"total_rows":3000,"hold_reason":"shared_list","status":"done",` +
		`"list_source":"export","list_source_detail":null,"checks":{"invalid_rows":2,"role_addresses":3,"disposable_addresses":1,"shared_addresses":1800,"shared_account_id":"acc-2"},"created_at":"2026-11-17T09:00:00Z"}],` +
		`"live_campaigns":[{"id":"camp-1","name":"Bread week","status":"sending","send_at":null,"recipients":1000,"rates":{"sent":1000,"hard_bounces":12,"complaints":4}}],` +
		`"terms_version":"2026-11-19","terms_accepted_at":"2026-11-01T09:00:00Z"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var b map[string]any
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &b)
			bodies[r.URL.Path] = b
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/admin/accounts" || r.URL.Path == "/v1/admin/signups" || r.URL.Path == "/v1/admin/test-mode":
			_, _ = w.Write([]byte(`{"data":[` + strings.SplitN(account, `,"daily_send_limit"`, 2)[0] + `}],"next_cursor":null}`))
		case strings.HasSuffix(r.URL.Path, "/credits"):
			_, _ = w.Write([]byte(`{"account_id":"` + testAccountID + `","amount":5000,"expires_at":"2028-11-18T00:00:00Z","credit_balance":14000}`))
		case strings.HasPrefix(r.URL.Path, "/v1/admin/accounts/"):
			_, _ = w.Write([]byte(account))
		case strings.HasPrefix(r.URL.Path, "/v1/admin/campaigns/"):
			_, _ = w.Write([]byte(`{"id":"` + testAccountID + `","name":"Bread week","status":"cancelled"}`))
		case strings.HasPrefix(r.URL.Path, "/v1/admin/imports/"):
			_, _ = w.Write([]byte(`{"id":"` + testAccountID + `","account_id":"a","contacts_released":3000}`))
		case r.URL.Path == "/v1/admin/actions":
			_, _ = w.Write([]byte(`{"data":[{"id":7,"actor":"staff@example.com","action":"pause","account_id":"` + testAccountID +
				`","target_id":null,"reason":"Phishing reports","details":{},"created_at":"2026-11-18T10:00:00Z"}],"next_cursor":"next"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	h := newHarness(t, "")
	h.env[EnvAPIURL] = srv.URL
	h.env[EnvAPIKey] = "mg_test_abc"
	return h
}

func TestAdminCommands(t *testing.T) {
	bodies := map[string]map[string]any{}
	h := adminStub(t, bodies)
	cases := []struct {
		args []string
		want []string
	}{
		{[]string{"admin", "find", "pat"}, []string{testAccountID, "Pat's Bakery", "pat@example.com", "suspended"}},
		{[]string{"admin", "account", testAccountID}, []string{"Suspended:", "by staff@example.com: Bought list", "PAUSED", "Phishing reports", "Daily limit:  1000 (THROTTLED until 2026-11-25", "Throttled:    2026-11-18", "0.2% marked it as spam", "1000 sent, 12 hard bounces (1.20%), 4 complaints (0.40%)", "imp-1", "list source export; 2 invalid, 3 role, 1 disposable; 1800 in other accounts' imports, most in account acc-2", "Bread week", "Terms:        version 2026-11-19, accepted 2026-11-0", "120 of 500 credits used, 380 left, until 2026-12-18"}},
		{[]string{"admin", "pause", testAccountID, "--reason", "Phishing reports"}, []string{"Paused sending for Pat's Bakery"}},
		{[]string{"admin", "unpause", testAccountID, "--reason", "Owner fixed the list"}, []string{"can send again"}},
		{[]string{"admin", "suspend", testAccountID, "--reason", "Bought list"}, []string{"Suspended Pat's Bakery", "owner has been emailed"}},
		{[]string{"admin", "unsuspend", testAccountID, "--reason", "Appeal accepted"}, []string{"no longer suspended", "still paused"}},
		{[]string{"admin", "suspend", testAccountID, "--reason", "Bought list", "--json"}, []string{`"suspended_reason": "Bought list"`}},
		{[]string{"admin", "signups"}, []string{"WAITING SINCE", testAccountID, "pat@example.com", "disposable_domain, ip_burst"}},
		{[]string{"admin", "signups", "--json"}, []string{`"review_flags": [`, `"next_cursor": null`}},
		{[]string{"admin", "account", testAccountID}, []string{"Sign-up:      waiting, flagged disposable_domain, ip_burst"}},
		{[]string{"admin", "approve-signup", testAccountID}, []string{"Approved Pat's Bakery", "still suspended", "admin unsuspend"}},
		{[]string{"admin", "reject-signup", testAccountID, "--reason", "Throwaway address"}, []string{"Rejected Pat's Bakery", "suspended"}},
		{[]string{"admin", "reject-signup", testAccountID, "--reason", "Throwaway address", "--json"}, []string{`"review": "waiting"`}},
		{[]string{"admin", "account", testAccountID}, []string{"Mode:         TEST MODE (sends only to its test addresses: 2 confirmed, 1 pending)"}},
		{[]string{"admin", "test-mode"}, []string{"TEST ADDRESSES", testAccountID, "pat@example.com", "2 confirmed, 1 pending"}},
		{[]string{"admin", "allow-full-sending", testAccountID}, []string{"Allowed Pat's Bakery", "full sending"}},
		{[]string{"admin", "set-test-mode", testAccountID, "--reason", "Internal testing account"}, []string{"Put Pat's Bakery", "in test mode"}},
		{[]string{"admin", "stop-campaign", testAccountID, "--reason", "Phishing", "--yes"}, []string{"Stopped campaign \"Bread week\""}},
		{[]string{"admin", "approve-import", testAccountID, "--reason", "Signup sheet"}, []string{"3000 contacts can be mailed"}},
		{[]string{"admin", "grant-credits", testAccountID, "--amount", "5000", "--reason", "Outage"}, []string{"Granted 5000 credits", "now 14000"}},
		{[]string{"admin", "log", "--account", testAccountID}, []string{"staff@example.com", "pause", "Phishing reports", "--cursor next"}},
	}
	for _, c := range cases {
		if code := h.run(c.args...); code != ExitOK {
			t.Fatalf("%v: exit %d: %s", c.args, code, h.errOut)
		}
		for _, w := range c.want {
			if !strings.Contains(h.out.String(), w) {
				t.Errorf("%v: output lacks %q:\n%s", c.args, w, h.out)
			}
		}
	}
	if b := bodies["/v1/admin/accounts/"+testAccountID+"/credits"]; b["amount"] != float64(5000) || b["reason"] != "Outage" {
		t.Errorf("grant body = %v", b)
	}
	if b := bodies["/v1/admin/accounts/"+testAccountID+"/pause"]; b["reason"] != "Phishing reports" {
		t.Errorf("pause body = %v", b)
	}
	if b := bodies["/v1/admin/accounts/"+testAccountID+"/unsuspend"]; b["reason"] != "Appeal accepted" {
		t.Errorf("unsuspend body = %v", b)
	}
	if b := bodies["/v1/admin/accounts/"+testAccountID+"/approve-signup"]; b["reason"] != "Sign-up approved." {
		t.Errorf("approve body = %v, want the default reason", b)
	}
	if b := bodies["/v1/admin/accounts/"+testAccountID+"/allow-full-sending"]; b["reason"] != "Full sending allowed." {
		t.Errorf("allow-full-sending body = %v, want the default reason", b)
	}
	if b := bodies["/v1/admin/accounts/"+testAccountID+"/set-test-mode"]; b["reason"] != "Internal testing account" {
		t.Errorf("set-test-mode body = %v", b)
	}
	if b := bodies["/v1/admin/accounts/"+testAccountID+"/reject-signup"]; b["reason"] != "Throwaway address" {
		t.Errorf("reject body = %v", b)
	}
}

// TestAdminEndedTester: an ended tester shows as not sending, with its
// end date, in `admin find` and `admin account` (MG-107).
func TestAdminEndedTester(t *testing.T) {
	summary := `{"id":"` + testAccountID + `","name":"Pat's Bakery","status":"active","plan":"free","owner_email":"pat@example.com",` +
		`"sending_paused_at":null,"sending_paused_reason":null,"suspended_at":null,"suspended_reason":null,"suspended_by":null,` +
		`"review":null,"review_flags":[],"review_at":null,"sending_mode":"full","test_addresses_confirmed":0,"test_addresses_pending":0,` +
		`"tester_until":"2026-11-20T12:00:00Z","created_at":"2026-11-01T09:00:00Z"`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/admin/accounts" {
			_, _ = w.Write([]byte(`{"data":[` + summary + `}],"next_cursor":null}`))
			return
		}
		_, _ = w.Write([]byte(summary + `,"daily_send_limit":null,"send_limit":{"daily":100,"usual":100,"reason":"Tester accounts send to at most 100 people a day."},` +
			`"throttles":[],"contacts":12,"credit_balance":900,` +
			`"tester":{"started_at":"2026-10-21T12:00:00Z","until":"2026-11-20T12:00:00Z","credits":500,"used":120,"left":380,"ended":true},` +
			`"last_30_days":{"sent":120,"hard_bounces":0,"complaints":0},"held_imports":[],"live_campaigns":[],` +
			`"terms_version":"2026-11-19","terms_accepted_at":"2026-11-01T09:00:00Z"}`))
	}))
	t.Cleanup(srv.Close)
	h := newHarness(t, "")
	h.env[EnvAPIURL] = srv.URL
	h.env[EnvAPIKey] = "mg_test_abc"
	until := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC).Local()
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"admin", "find", "pat"}, "off: tester ended " + until.Format("2006-01-02")},
		{[]string{"admin", "account", testAccountID}, "Sending:      OFF: the tester period ended " + until.Format("2006-01-02 15:04")},
	} {
		if code := h.run(c.args...); code != ExitOK {
			t.Fatalf("%v: exit %d: %s", c.args, code, h.errOut)
		}
		if !strings.Contains(h.out.String(), c.want) {
			t.Errorf("%v: output lacks %q:\n%s", c.args, c.want, h.out)
		}
		if strings.Contains(h.out.String(), "Sending:      on") {
			t.Errorf("%v: says sending is on:\n%s", c.args, h.out)
		}
	}
}

// TestAdminUnsuspendRejected: unsuspending a rejected sign-up says the
// review is still rejected and points to approve-signup, which then says
// the account can launch (MG-106).
func TestAdminUnsuspendRejected(t *testing.T) {
	review := "rejected"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/approve-signup") {
			review = "approved"
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"` + testAccountID + `","name":"Pat's Bakery","status":"active","plan":"free","owner_email":"pat@example.com",` +
			`"sending_paused_at":null,"sending_paused_reason":null,"suspended_at":null,"suspended_reason":null,"suspended_by":null,` +
			`"review":"` + review + `","review_flags":[],"review_at":"2026-11-01T09:05:00Z","sending_mode":"full",` +
			`"test_addresses_confirmed":0,"test_addresses_pending":0,"created_at":"2026-11-01T09:00:00Z"}`))
	}))
	t.Cleanup(srv.Close)
	h := newHarness(t, "")
	h.env[EnvAPIURL] = srv.URL
	h.env[EnvAPIKey] = "mg_test_abc"
	if code := h.run("admin", "unsuspend", testAccountID, "--reason", "Appeal accepted"); code != ExitOK {
		t.Fatalf("unsuspend: exit %d: %s", code, h.errOut)
	}
	for _, w := range []string{"no longer suspended", "sign-up review is still rejected", "admin approve-signup"} {
		if !strings.Contains(h.out.String(), w) {
			t.Errorf("unsuspend output lacks %q:\n%s", w, h.out)
		}
	}
	if strings.Contains(h.out.String(), "still paused") {
		t.Errorf("unsuspend says paused:\n%s", h.out)
	}
	if code := h.run("admin", "approve-signup", testAccountID); code != ExitOK {
		t.Fatalf("approve: exit %d: %s", code, h.errOut)
	}
	if !strings.Contains(h.out.String(), "It can launch campaigns now.") {
		t.Errorf("approve output:\n%s", h.out)
	}
}

func TestAdminNeedsReason(t *testing.T) {
	h := adminStub(t, map[string]map[string]any{})
	for _, args := range [][]string{
		{"admin", "pause", testAccountID},
		{"admin", "unpause", testAccountID, "--reason", "  "},
		{"admin", "suspend", testAccountID},
		{"admin", "unsuspend", "pats-bakery", "--reason", "x"},
		{"admin", "reject-signup", testAccountID},
		{"admin", "approve-signup", testAccountID, "--reason", " "},
		{"admin", "signups", "extra"},
		{"admin", "stop-campaign", testAccountID, "--yes"},
		{"admin", "stop-campaign", testAccountID, "--reason", "x"},
		{"admin", "approve-import", testAccountID},
		{"admin", "grant-credits", testAccountID, "--reason", "x"},
		{"admin", "account", "pats-bakery"},
		{"admin", "set-test-mode", testAccountID, "--reason", " "},
		{"admin", "allow-full-sending", "pats-bakery"},
		{"admin", "test-mode", "extra"},
	} {
		if code := h.run(args...); code != ExitUsage {
			t.Errorf("%v: exit %d", args, code)
		}
	}
}

// TestAdminHelpPointsToAccountStates checks `mailgoose admin --help` names
// the page on account states (MG-96).
func TestAdminHelpPointsToAccountStates(t *testing.T) {
	h := newHarness(t, "")
	if code := h.run("admin", "--help"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if !strings.Contains(h.out.String(), "docs/account-states.md") {
		t.Errorf("admin --help doesn't point to docs/account-states.md:\n%s", h.out)
	}
}
