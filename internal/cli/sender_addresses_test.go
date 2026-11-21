package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

const senderAddressID = "6a1c2b4a-5d6e-4f70-8a9b-0c1d2e3f4a5b"

// TestSenderAddressCommands drives `mailgoose sender-addresses` against a
// stub (MG-85).
func TestSenderAddressCommands(t *testing.T) {
	type call struct{ method, path, auth, body string }
	var calls []call
	addr := `{"id":"` + senderAddressID + `","email":"pat@example.com","status":"pending","confirm_sent_at":"2026-11-21T10:00:00Z",` +
		`"confirm_expires_at":"2099-11-28T10:00:00Z","confirmed_at":null,"created_at":"2026-11-21T10:00:00Z"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		calls = append(calls, call{r.Method, r.URL.Path, r.Header.Get("Authorization"), string(raw)})
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sender-addresses":
			_, _ = w.Write([]byte(`{"data":[` + addr + `,{"id":"8b1c2b4a-5d6e-4f70-8a9b-0c1d2e3f4a5b","email":"hello@example.com","status":"confirmed",` +
				`"confirm_sent_at":null,"confirm_expires_at":null,"confirmed_at":"2026-11-19T10:00:00Z","created_at":"2026-11-19T09:00:00Z"}],"next_cursor":null}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sender-addresses":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(addr))
		case r.URL.Path == "/v1/sender-addresses/confirm":
			_, _ = w.Write([]byte(`{"email":"pat@example.com","account_name":"Pat's Bakery","confirmed_at":"2026-11-21T11:00:00Z"}`))
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
		{[]string{"sender-addresses", "list"}, []string{"pat@example.com", "pending (link sent", "hello@example.com", "confirmed 2026-11-19"},
			call{http.MethodGet, "/v1/sender-addresses", "Bearer mg_test_abc", ""}},
		{[]string{"sender-address", "add", "pat@example.com"}, []string{"Added pat@example.com", "confirm link"},
			call{http.MethodPost, "/v1/sender-addresses", "Bearer mg_test_abc", `{"email":"pat@example.com"}`}},
		{[]string{"sender-addresses", "resend", "PAT@example.com"}, []string{"Sent pat@example.com a new confirm link"},
			call{http.MethodPost, "/v1/sender-addresses/" + senderAddressID + "/resend", "Bearer mg_test_abc", ""}},
		{[]string{"sender-addresses", "remove", senderAddressID}, []string{"Removed sender address"},
			call{http.MethodDelete, "/v1/sender-addresses/" + senderAddressID, "Bearer mg_test_abc", ""}},
		{[]string{"sender-addresses", "confirm", "s1abc"}, []string{"Confirmed pat@example.com as a sender address of Pat's Bakery"},
			call{http.MethodPost, "/v1/sender-addresses/confirm", "", `{"token":"s1abc"}`}},
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

	if code := h.run("sender-addresses", "remove", "nobody@example.com"); code != ExitNotFound {
		t.Errorf("remove unknown: exit %d", code)
	}
	if code := h.run("sender-addresses", "list", "--json"); code != ExitOK {
		t.Fatalf("list --json: exit %d", code)
	}
	var page senderAddressPage
	if err := json.Unmarshal(h.out.Bytes(), &page); err != nil || len(page.Data) != 2 {
		t.Errorf("list --json = %v, %v", page, err)
	}
}

// TestSharedSendingNudges: with shared sending on, an account with no
// domain of its own tests and launches from the shared domain, and the CLI
// says so with how to add the domain; `account get` shows the shared From
// (MG-85).
func TestSharedSendingNudges(t *testing.T) {
	h := liveAPIShared(t, "shared.test.example")
	h.app.Stdin = strings.NewReader(strings.Replace(springDoc, "news@bakery.example", "pat@example.com", 1))
	if code := h.run("email", "create", "--name", "Spring", "--file", "-", "--json"); code != ExitOK {
		t.Fatalf("email: exit %d %s", code, h.errOut)
	}
	emailID := h.jsonOut(t)["id"].(string)
	if code := h.run("campaign", "create", "--name", "Spring", "--email", emailID, "--all", "--json"); code != ExitOK {
		t.Fatalf("create: exit %d %s", code, h.errOut)
	}
	id := h.jsonOut(t)["id"].(string)
	if code := h.run("account", "update", "--postal-address", "12 Main St"); code != ExitOK {
		t.Fatalf("postal: exit %d %s", code, h.errOut)
	}

	if code := h.run("account", "get"); code != ExitOK || !strings.Contains(h.out.String(), "Shared From: test@shared.test.example") {
		t.Errorf("account get: exit %d %s", code, h.out)
	}

	nudge := "Sending from test@shared.test.example, mailgoose's shared domain, with replies to pat@example.com.\n" +
		"Add your own domain for better deliverability and trust: mailgoose domain add example.com"
	if code := h.run("campaign", "test", id); code != ExitOK {
		t.Fatalf("test: exit %d %s", code, h.errOut)
	}
	if !strings.Contains(h.out.String(), nudge) {
		t.Errorf("test lacks the nudge:\n%s", h.out)
	}

	if code := h.run("contact", "add", "sam@example.org"); code != ExitOK {
		t.Fatalf("contact: exit %d %s", code, h.errOut)
	}
	v, _ := liveDBs.Load(t)
	if _, err := v.(*pgxpool.Pool).Exec(t.Context(), `
		INSERT INTO credit_transactions (account_id, type, amount, expires_at)
		SELECT id, 'purchase', 100, now() + interval '1 year' FROM accounts`); err != nil {
		t.Fatal(err)
	}
	if code := h.run("campaign", "launch", id); code != ExitNeedsConfirmation {
		t.Fatalf("summary: exit %d %s", code, h.errOut)
	}
	if !strings.Contains(h.out.String(), nudge) || !strings.Contains(h.out.String(), "Reply-To:") {
		t.Errorf("summary lacks the nudge:\n%s", h.out)
	}
}

// TestNoNudgeWithoutSharedSending: shared sending off, a From with no
// verified domain is refused as before, with no word of sender addresses.
func TestNoNudgeWithoutSharedSending(t *testing.T) {
	h := liveAPI(t)
	h.app.Stdin = strings.NewReader(strings.Replace(springDoc, "news@bakery.example", "pat@example.com", 1))
	if code := h.run("email", "create", "--name", "Spring", "--file", "-", "--json"); code != ExitOK {
		t.Fatalf("email: exit %d %s", code, h.errOut)
	}
	emailID := h.jsonOut(t)["id"].(string)
	if code := h.run("campaign", "create", "--name", "Spring", "--email", emailID, "--all", "--json"); code != ExitOK {
		t.Fatalf("create: exit %d %s", code, h.errOut)
	}
	id := h.jsonOut(t)["id"].(string)
	if code := h.run("campaign", "test", id); code != ExitError || strings.Contains(h.errOut.String(), "sender-addresses") ||
		!strings.Contains(h.errOut.String(), "mailgoose domain add example.com") {
		t.Errorf("test: exit %d %s", code, h.errOut)
	}
	if code := h.run("account", "get"); code != ExitOK || strings.Contains(h.out.String(), "Shared From") {
		t.Errorf("account get: exit %d %s", code, h.out)
	}
}
