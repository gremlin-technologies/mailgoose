package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testAddressID = "7a1c2b4a-5d6e-4f70-8a9b-0c1d2e3f4a5b"

// TestTestAddressCommands drives `mailgoose test-addresses` against a stub
// (MG-80).
func TestTestAddressCommands(t *testing.T) {
	type call struct{ method, path, auth, body string }
	var calls []call
	addr := `{"id":"` + testAddressID + `","email":"qa@example.com","status":"pending","confirm_sent_at":"2026-11-20T10:00:00Z",` +
		`"confirm_expires_at":"2099-11-27T10:00:00Z","confirmed_at":null,"created_at":"2026-11-20T10:00:00Z"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		calls = append(calls, call{r.Method, r.URL.Path, r.Header.Get("Authorization"), string(raw)})
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/test-addresses":
			_, _ = w.Write([]byte(`{"data":[` + addr + `,{"id":"8b1c2b4a-5d6e-4f70-8a9b-0c1d2e3f4a5b","email":"lead@example.com","status":"confirmed",` +
				`"confirm_sent_at":null,"confirm_expires_at":null,"confirmed_at":"2026-11-19T10:00:00Z","created_at":"2026-11-19T09:00:00Z"}],"next_cursor":null}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/test-addresses":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(addr))
		case r.URL.Path == "/v1/test-addresses/confirm":
			_, _ = w.Write([]byte(`{"email":"qa@example.com","account_name":"Pat's Bakery","confirmed_at":"2026-11-20T11:00:00Z"}`))
		case strings.HasSuffix(r.URL.Path, "/resend"):
			_, _ = w.Write([]byte(addr))
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	h := newHarness(t, "")
	h.env[EnvAPIURL] = srv.URL
	h.env[EnvAPIKey] = "mg_test_abc"

	for _, c := range []struct {
		args []string
		want []string
		last call
	}{
		{[]string{"test-addresses", "list"}, []string{"qa@example.com", "pending (link sent", "lead@example.com", "confirmed 2026-11-19"},
			call{http.MethodGet, "/v1/test-addresses", "Bearer mg_test_abc", ""}},
		{[]string{"test-addresses", "add", "qa@example.com"}, []string{"Added qa@example.com", "confirm link"},
			call{http.MethodPost, "/v1/test-addresses", "Bearer mg_test_abc", `{"email":"qa@example.com"}`}},
		{[]string{"test-addresses", "resend", "QA@example.com"}, []string{"Sent qa@example.com a new confirm link"},
			call{http.MethodPost, "/v1/test-addresses/" + testAddressID + "/resend", "Bearer mg_test_abc", ""}},
		{[]string{"test-addresses", "remove", testAddressID}, []string{"Removed test address"},
			call{http.MethodDelete, "/v1/test-addresses/" + testAddressID, "Bearer mg_test_abc", ""}},
		{[]string{"test-addresses", "confirm", "t1abc"}, []string{"Confirmed qa@example.com as a test address of Pat's Bakery"},
			call{http.MethodPost, "/v1/test-addresses/confirm", "", `{"token":"t1abc"}`}},
	} {
		if code := h.run(c.args...); code != ExitOK {
			t.Fatalf("%v: exit %d: %s", c.args, code, h.errOut)
		}
		for _, w := range c.want {
			if !strings.Contains(h.out.String(), w) {
				t.Errorf("%v: output lacks %q:\n%s", c.args, w, h.out)
			}
		}
		if got := calls[len(calls)-1]; got != c.last {
			t.Errorf("%v: last call %+v, want %+v", c.args, got, c.last)
		}
	}

	if code := h.run("test-addresses", "remove", "nobody@example.com"); code != ExitNotFound {
		t.Errorf("remove unknown: exit %d", code)
	}
	if code := h.run("test-addresses", "list", "--json"); code != ExitOK {
		t.Fatalf("list --json: exit %d", code)
	}
	var page testAddressPage
	if err := json.Unmarshal(h.out.Bytes(), &page); err != nil || len(page.Data) != 2 {
		t.Errorf("list --json = %v, %v", page, err)
	}
}
