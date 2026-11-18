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
			"postal_address": nil, "time_zone": "UTC", "missing_for_send": []string{"postal_address"}, "created_at": "2026-11-18T12:00:00Z"}
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
	for _, want := range []string{"Pat's Bakery (free plan)", "From email:  (not set)", "can't launch until you set: postal_address"} {
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
