package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// accountStub answers getAccount and updateAccount, echoing each update's
// fields into the account it returns, and records the update bodies.
func accountStub(t *testing.T) (*harness, *[]map[string]any) {
	t.Helper()
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		acct := map[string]any{"id": "a1", "name": "Pat's Bakery", "plan": "free", "from_name": nil, "from_email": nil,
			"postal_address": nil, "time_zone": "UTC", "missing_for_send": []string{"postal_address"}, "created_at": "2026-11-18T12:00:00Z",
			"send_limit": map[string]any{"daily": 1000, "usual": 10000, "reason": "New accounts start at 1,000 a day.", "lowered_until": nil},
			"terms": map[string]any{"accepted_version": "2026-11-19", "accepted_at": "2026-11-18T12:00:00Z", "current_version": "2026-11-19",
				"current_effective": "2026-11-19", "acceptance_needed": false}}
		if r.Method == http.MethodPatch {
			var b map[string]any
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &b)
			bodies = append(bodies, b)
			for k, v := range b {
				acct[k] = v
			}
			if acct["postal_address"] != nil {
				acct["missing_for_send"] = []string{}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(acct)
	}))
	t.Cleanup(srv.Close)
	h := newHarness(t, "")
	h.env[EnvAPIURL] = srv.URL
	h.env[EnvAPIKey] = "mg_test_key"
	return h, &bodies
}

func TestAccountGet(t *testing.T) {
	h, _ := accountStub(t)
	if code := h.run("account", "get"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	for _, want := range []string{"Pat's Bakery (free plan)", "From email:  (not set)", "can't launch until you set: postal_address",
		"Send limit:  1,000 a day\n             New accounts start at 1,000 a day.", "Terms:       version 2026-11-19, accepted"} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, h.out)
		}
	}
}

func TestAccountUpdate(t *testing.T) {
	h, bodies := accountStub(t)
	if code := h.run("account", "update", "--from-name", "Pat", "--from-email", "", "--postal-address", `Pat's Bakery\n12 Main St`); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	b := (*bodies)[0]
	if len(b) != 3 || b["from_name"] != "Pat" || b["postal_address"] != "Pat's Bakery\n12 Main St" {
		t.Errorf("body = %v", b)
	}
	if v, ok := b["from_email"]; !ok || v != nil {
		t.Errorf(`--from-email "" should send null, body = %v`, b)
	}
	if !strings.Contains(h.out.String(), "Postal:      Pat's Bakery\n             12 Main St") {
		t.Errorf("output:\n%s", h.out)
	}

	file := filepath.Join(t.TempDir(), "address.txt")
	if err := os.WriteFile(file, []byte("Pat's Bakery\n12 Main St\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := h.run("account", "update", "--postal-address-file", file, "--time-zone", "America/Chicago", "--json"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	b = (*bodies)[1]
	if b["postal_address"] != "Pat's Bakery\n12 Main St" || b["time_zone"] != "America/Chicago" {
		t.Errorf("body = %v", b)
	}
}

func TestAccountUpdateUsage(t *testing.T) {
	h, bodies := accountStub(t)
	for _, args := range [][]string{
		{"account", "update"},
		{"account", "update", "--postal-address", "x", "--postal-address-file", "y"},
		{"account", "update", "--postal-address-file", "/no/such/file"},
	} {
		if code := h.run(args...); code != ExitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, ExitUsage)
		}
	}
	if len(*bodies) != 0 {
		t.Errorf("sent %d requests", len(*bodies))
	}
}

func TestAccountDelete(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.Path+" "+r.URL.Query().Get("confirm"))
		if r.URL.Query().Get("confirm") != "Pat's Bakery" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = io.WriteString(w, `{"error":{"code":"confirmation_mismatch","message":"confirm must be the account's exact name"}}`)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	h := newHarness(t, "")
	h.env[EnvAPIURL] = srv.URL
	h.env[EnvAPIKey] = "mg_test_key"

	if code := h.run("account", "delete"); code != ExitUsage || len(got) != 0 {
		t.Fatalf("without --confirm: exit %d, %d requests", code, len(got))
	}
	if code := h.run("account", "delete", "--confirm", "Pats Bakery"); code == ExitOK || !strings.Contains(h.errOut.String(), "exact name") {
		t.Fatalf("wrong name: exit %d: %s", code, h.errOut)
	}
	if code := h.run("account", "delete", "--confirm", "Pat's Bakery"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if got[1] != "DELETE /v1/account Pat's Bakery" || !strings.Contains(h.out.String(), "Deleted the account Pat's Bakery.") {
		t.Errorf("requests %v, output:\n%s", got, h.out)
	}
}

// termsStub answers getAccount and acceptTerms for an account on an older
// version of the terms, recording what acceptTerms gets.
func termsStub(t *testing.T) (*harness, *[]map[string]any) {
	t.Helper()
	var bodies []map[string]any
	accepted := "2026-01-01"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/v1/account/terms" {
			var b map[string]any
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &b)
			bodies = append(bodies, b)
			if b["version"] != "2026-11-19" {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(`{"error":{"code":"terms_version_not_current","message":"The current terms are version 2026-11-19, so version \"2026-01-01\" can't be accepted.","hint":""}}`))
				return
			}
			accepted = "2026-11-19"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "a1", "name": "Pat's Bakery", "plan": "free", "from_name": nil, "from_email": nil,
			"postal_address": "12 Main St", "time_zone": "UTC", "missing_for_send": []string{}, "created_at": "2026-11-18T12:00:00Z",
			"terms": map[string]any{"accepted_version": accepted, "accepted_at": "2026-11-19T12:00:00Z", "current_version": "2026-11-19",
				"current_effective": "2026-11-19", "acceptance_needed": accepted != "2026-11-19"}})
	}))
	t.Cleanup(srv.Close)
	h := newHarness(t, "")
	h.env[EnvAPIURL] = srv.URL
	h.env[EnvAPIKey] = "mg_test_key"
	return h, &bodies
}

func TestAccountAcceptTerms(t *testing.T) {
	h, bodies := termsStub(t)
	if code := h.run("account", "get"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	for _, want := range []string{"Terms:       version 2026-01-01", "version 2026-11-19 took effect on 2026-11-19",
		"mailgoose account accept-terms --version 2026-11-19"} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("account get lacks %q:\n%s", want, h.out)
		}
	}

	h.out.Reset()
	if code := h.run("account", "accept-terms"); code != ExitUsage {
		t.Errorf("no --version: exit %d", code)
	}
	if code := h.run("account", "accept-terms", "--version", "2026-01-01"); code == ExitOK {
		t.Error("an old version was accepted")
	}
	if !strings.Contains(h.errOut.String(), "current terms are version 2026-11-19") {
		t.Errorf("error output:\n%s", h.errOut)
	}
	if code := h.run("account", "accept-terms", "--version", "2026-11-19"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if !strings.Contains(h.out.String(), "Accepted the terms, version 2026-11-19.") {
		t.Errorf("output:\n%s", h.out)
	}
	if len(*bodies) != 2 || (*bodies)[1]["version"] != "2026-11-19" || len((*bodies)[1]) != 1 {
		t.Errorf("bodies %v", *bodies)
	}

	h.out.Reset()
	if code := h.run("account", "accept-terms", "--version", "2026-11-19", "--json"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	var out struct {
		Terms struct {
			AcceptedVersion  string `json:"accepted_version"`
			AcceptanceNeeded bool   `json:"acceptance_needed"`
		} `json:"terms"`
	}
	if err := json.Unmarshal(h.out.Bytes(), &out); err != nil || out.Terms.AcceptedVersion != "2026-11-19" || out.Terms.AcceptanceNeeded {
		t.Errorf("--json output %s (%v)", h.out, err)
	}
}
