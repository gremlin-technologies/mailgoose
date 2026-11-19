package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// adminAccounts holds the account of each test's admin, made by asAdmin.
var adminAccounts sync.Map // *testing.T -> account id

// heldImports holds the import heldImport added.
var heldImports sync.Map // *testing.T -> import id

// asAdmin wraps a server constructor: it also makes an active account whose
// person is a mailgoose admin, signed in with a CLI token kept for bearer.
func asAdmin(server func(*testing.T) *Server) func(*testing.T) *Server {
	return signedInPerson(server, "staff@example.com", true)
}

// asCustomer is asAdmin for a person who isn't an admin.
func asCustomer(server func(*testing.T) *Server) func(*testing.T) *Server {
	return signedInPerson(server, "pat@example.com", false)
}

func signedInPerson(server func(*testing.T) *Server, email string, isAdmin bool) func(*testing.T) *Server {
	return func(t *testing.T) *Server {
		t.Helper()
		s := server(t)
		ctx := context.Background()
		pool := s.DB.(*pgxpool.Pool)
		var accountID, userID string
		if err := pool.QueryRow(ctx, `INSERT INTO accounts (name, status) VALUES ('Staff', 'active') RETURNING id`).Scan(&accountID); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `INSERT INTO users (account_id, email, admin) VALUES ($1, $2, $3) RETURNING id`,
			accountID, email, isAdmin).Scan(&userID); err != nil {
			t.Fatal(err)
		}
		k, err := s.Auth.CreateCLIToken(ctx, accountID, userID, "cli")
		if err != nil {
			t.Fatal(err)
		}
		testKeys.Store(t, testKey{id: k.ID, secret: k.Secret})
		adminAccounts.Store(t, accountID)
		t.Cleanup(func() { testKeys.Delete(t); adminAccounts.Delete(t) })
		return s
	}
}

// heldImport adds an import of the admin's account waiting for approval,
// with one contact held by it.
func heldImport(t *testing.T, _ http.Handler) {
	t.Helper()
	acc, _ := adminAccounts.Load(t)
	db, _ := testDBs.Load(t)
	pool := db.(*pgxpool.Pool)
	var id string
	if err := pool.QueryRow(t.Context(), `
		INSERT INTO imports (account_id, permission_confirmed, email_column, csv, total_rows, review, hold_reason, status)
		VALUES ($1, true, 'email', 'email', 3000, 'held', 'large_first_import', 'done') RETURNING id`, acc).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO contacts (account_id, email, status, source, held_import_id, import_id)
		VALUES ($1, 'held@example.com', 'subscribed', 'import', $2, $2)`, acc, id); err != nil {
		t.Fatal(err)
	}
	heldImports.Store(t, id)
	t.Cleanup(func() { heldImports.Delete(t) })
}

// approvedImport is heldImport, already let through.
func approvedImport(t *testing.T, h http.Handler) {
	t.Helper()
	heldImport(t, h)
	id, _ := heldImports.Load(t)
	db, _ := testDBs.Load(t)
	if _, err := db.(*pgxpool.Pool).Exec(t.Context(), `UPDATE imports SET review = 'approved' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
}

// adminPath is a request to path with ACCOUNT, CAMPAIGN, IMPORT and TARGET
// replaced by the test's admin account, campaign, held import and customer
// account (targetAccount), or ids that match nothing.
func adminPath(method, path, body string) func(*testing.T) *http.Request {
	return func(t *testing.T) *http.Request {
		for word, m := range map[string]*sync.Map{"ACCOUNT": &adminAccounts, "CAMPAIGN": &addedCampaigns, "IMPORT": &heldImports,
			"TARGET": &targetAccounts} {
			id := noSuchID
			if v, ok := m.Load(t); ok {
				id = v.(string)
			}
			path = strings.ReplaceAll(path, word, id)
		}
		if body != "" {
			return jsonRequest(method, path, body)(t)
		}
		return httptest.NewRequest(method, path, nil)
	}
}

const adminReasonBody = `{"reason":"Checked with the owner by email"}`

func TestAdminRefusals(t *testing.T) {
	cases := []struct {
		name   string
		server func(*testing.T) *Server
		code   string
	}{
		{"customer", asCustomer(authServer), "admin_only"},
		{"customer's API key", fullKey(authServer), "admin_only"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := c.server(t).Handler()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, bearer(request(http.MethodGet, "/v1/admin/accounts?q=example"))(t))
			if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), c.code) {
				t.Errorf("%d %s", rec.Code, rec.Body)
			}
		})
	}
	// Even an admin's own API key is refused: only a person's sign-in counts.
	t.Run("admin with an API key", func(t *testing.T) {
		s := authServer(t)
		k := newTestKey(t, s, "key@example.com", "full")
		db, _ := testDBs.Load(t)
		if _, err := db.(*pgxpool.Pool).Exec(t.Context(), `UPDATE users SET admin = true WHERE email = 'key@example.com'`); err != nil {
			t.Fatal(err)
		}
		testKeys.Store(t, k)
		t.Cleanup(func() { testKeys.Delete(t) })
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, bearer(request(http.MethodGet, "/v1/admin/accounts?q=example"))(t))
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "mailgoose login") {
			t.Errorf("%d %s", rec.Code, rec.Body)
		}
	})
}

func TestAdminPauseFlow(t *testing.T) {
	h := asAdmin(authServer)(t).Handler()
	do := func(method, path, body string, want int) map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, bearer(adminPath(method, path, body))(t))
		if rec.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body)
		}
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	found := do(http.MethodGet, "/v1/admin/accounts?q=staff@", "", http.StatusOK)
	if data := found["data"].([]any); len(data) != 1 {
		t.Fatalf("found %v", found)
	}
	acct := do(http.MethodGet, "/v1/admin/accounts/ACCOUNT", "", http.StatusOK)
	if l, ok := acct["send_limit"].(map[string]any); !ok || l["daily"] != float64(1000) || l["usual"] != float64(10000) {
		t.Errorf("send_limit = %v", acct["send_limit"])
	}
	if th, ok := acct["throttles"].([]any); !ok || len(th) != 0 {
		t.Errorf("throttles = %v", acct["throttles"])
	}
	paused := do(http.MethodPost, "/v1/admin/accounts/ACCOUNT/pause", `{"reason":"Phishing reports"}`, http.StatusOK)
	if paused["sending_paused_reason"] != "Phishing reports" || paused["sending_paused_at"] == nil {
		t.Errorf("paused = %v", paused)
	}
	unpaused := do(http.MethodPost, "/v1/admin/accounts/ACCOUNT/unpause", adminReasonBody, http.StatusOK)
	if unpaused["sending_paused_at"] != nil {
		t.Errorf("unpaused = %v", unpaused)
	}
	granted := do(http.MethodPost, "/v1/admin/accounts/ACCOUNT/credits", `{"amount":250,"reason":"Goodwill"}`, http.StatusOK)
	if granted["amount"] != float64(250) {
		t.Errorf("granted = %v", granted)
	}
	// Every action is in the log, newest first, with who and why; two to a
	// page.
	page := do(http.MethodGet, "/v1/admin/actions?account_id=ACCOUNT&limit=2", "", http.StatusOK)
	data := page["data"].([]any)
	if len(data) != 2 || page["next_cursor"] == nil {
		t.Fatalf("page = %v", page)
	}
	first := data[0].(map[string]any)
	if first["action"] != "grant_credits" || first["actor"] != "staff@example.com" || first["reason"] != "Goodwill" {
		t.Errorf("first = %v", first)
	}
	rest := do(http.MethodGet, "/v1/admin/actions?account_id=ACCOUNT&limit=2&cursor="+page["next_cursor"].(string), "", http.StatusOK)
	if data := rest["data"].([]any); len(data) != 1 || data[0].(map[string]any)["action"] != "pause" || rest["next_cursor"] != nil {
		t.Errorf("rest = %v", rest)
	}
}
