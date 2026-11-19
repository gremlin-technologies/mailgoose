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

	"github.com/gremlin-technologies/mailgoose/internal/admin"
	"github.com/gremlin-technologies/mailgoose/internal/auth"
)

// targetAccounts holds the customer account targetAccount made.
var targetAccounts sync.Map // *testing.T -> account id

// targetAccount adds an active customer account, for an admin to act on
// in place of the admin's own.
func targetAccount(t *testing.T, _ http.Handler) {
	t.Helper()
	db, _ := testDBs.Load(t)
	pool := db.(*pgxpool.Pool)
	var id string
	if err := pool.QueryRow(t.Context(), `INSERT INTO accounts (name, status) VALUES ('Target', 'active') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO users (account_id, email) VALUES ($1, 'target@example.com')`, id); err != nil {
		t.Fatal(err)
	}
	targetAccounts.Store(t, id)
	t.Cleanup(func() { targetAccounts.Delete(t) })
}

// suspendedTarget is targetAccount, suspended.
func suspendedTarget(t *testing.T, h http.Handler) {
	t.Helper()
	targetAccount(t, h)
	id, _ := targetAccounts.Load(t)
	db, _ := testDBs.Load(t)
	if _, err := db.(*pgxpool.Pool).Exec(t.Context(), `UPDATE accounts SET suspended_at = now(),
		suspended_reason = 'Test.', suspended_by = 'test' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
}

// waitingTarget is targetAccount, waiting for its sign-up review (MG-66).
func waitingTarget(t *testing.T, h http.Handler) {
	t.Helper()
	targetAccount(t, h)
	id, _ := targetAccounts.Load(t)
	db, _ := testDBs.Load(t)
	if _, err := db.(*pgxpool.Pool).Exec(t.Context(), `UPDATE accounts SET review = 'waiting', review_at = now(),
		review_flags = '{ip_burst}' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
}

// suspendedSignup is signedUp for an account that is then suspended.
func suspendedSignup(t *testing.T, h http.Handler) {
	t.Helper()
	signedUp(t, h)
	db, _ := testDBs.Load(t)
	if _, err := db.(*pgxpool.Pool).Exec(t.Context(), `UPDATE accounts SET suspended_at = now(),
		suspended_reason = 'Test.', suspended_by = 'test'`); err != nil {
		t.Fatal(err)
	}
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var e struct {
		Error struct{ Code string } `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &e)
	return e.Error.Code
}

// TestSuspendedAccount follows an account through a suspension (MG-65): its
// key stops working, a new web sign-in gets the read-only view and the
// export for 30 days, a CLI login is refused, and unsuspending lets it act
// again while its key stays revoked.
func TestSuspendedAccount(t *testing.T) {
	srv := authServer(t)
	now := time.Now()
	srv.Now = func() time.Time { return now }
	authNow := now
	srv.Auth.Now = func() time.Time { return authNow }
	h := srv.Handler()
	signedIn(t, h)

	rec := do(t, h, withSession(jsonRequest(http.MethodPost, "/v1/api-keys", createKeyBody))(t))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create key: %d %s", rec.Code, rec.Body)
	}
	var created struct{ Secret string }
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	withKey := func(req func(*testing.T) *http.Request) *http.Request {
		r := req(t)
		r.Header.Set("Authorization", "Bearer "+created.Secret)
		return r
	}

	ctx := context.Background()
	pool := srv.DB.(*pgxpool.Pool)
	var accountID string
	if err := pool.QueryRow(ctx, `SELECT account_id FROM users WHERE email = 'pat@example.com'`).Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	srv.Admin.Now = func() time.Time { return now }
	if _, err := srv.Admin.Suspend(ctx, admin.Server, accountID, "Sent to a bought list."); err != nil {
		t.Fatal(err)
	}

	if rec := do(t, h, withKey(request(http.MethodGet, "/v1/contacts"))); rec.Code != http.StatusUnauthorized {
		t.Errorf("revoked key: %d, want 401", rec.Code)
	}
	if rec := do(t, h, withSession(request(http.MethodGet, "/v1/sessions/current"))(t)); rec.Code != http.StatusUnauthorized {
		t.Errorf("old session: %d, want 401", rec.Code)
	}

	// Signing in again works, into the read-only view.
	authNow = authNow.Add(auth.ResendInterval)
	if rec := do(t, h, jsonRequest(http.MethodPost, "/v1/sign-in", signInBody)(t)); rec.Code != http.StatusAccepted {
		t.Fatalf("sign-in request: %d", rec.Code)
	}
	signedInAgain := do(t, h, sessionRequest("")(t))
	if signedInAgain.Code != http.StatusCreated {
		t.Fatalf("sign in while suspended: %d %s", signedInAgain.Code, signedInAgain.Body)
	}
	session := func(req func(*testing.T) *http.Request) *http.Request {
		r := req(t)
		for _, c := range signedInAgain.Result().Cookies() {
			r.AddCookie(c)
		}
		return r
	}
	rec = do(t, h, session(request(http.MethodGet, "/v1/sessions/current")))
	if rec.Code != http.StatusOK {
		t.Fatalf("current session: %d %s", rec.Code, rec.Body)
	}
	var cur struct {
		Account struct {
			Suspension *struct {
				Reason      string    `json:"reason"`
				ExportUntil time.Time `json:"export_until"`
			} `json:"suspension"`
		} `json:"account"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &cur)
	if s := cur.Account.Suspension; s == nil || s.Reason != "Sent to a bought list." ||
		!s.ExportUntil.Equal(now.Add(30*24*time.Hour).UTC().Truncate(time.Microsecond)) {
		t.Errorf("suspension = %+v", s)
	}
	for _, path := range []string{"/v1/contacts", "/v1/contacts/export", "/v1/campaigns", "/v1/emails"} {
		if rec := do(t, h, session(request(http.MethodGet, path))); rec.Code != http.StatusOK {
			t.Errorf("GET %s while suspended: %d %s", path, rec.Code, rec.Body)
		}
	}
	rec = do(t, h, session(jsonRequest(http.MethodPost, "/v1/contacts", `{"email":"new@example.com"}`)))
	if rec.Code != http.StatusForbidden || errorCode(t, rec) != "account_suspended" {
		t.Errorf("add contact while suspended: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, h, session(jsonRequest(http.MethodPost, "/v1/api-keys", createKeyBody)))
	if rec.Code != http.StatusForbidden {
		t.Errorf("new key while suspended: %d", rec.Code)
	}

	// No CLI login.
	authNow = authNow.Add(auth.ResendInterval)
	if rec := do(t, h, jsonRequest(http.MethodPost, "/v1/sign-in", signInBody)(t)); rec.Code != http.StatusAccepted {
		t.Fatalf("sign-in request: %d", rec.Code)
	}
	rec = do(t, h, cliTokenRequest("")(t))
	if rec.Code != http.StatusForbidden || errorCode(t, rec) != "account_suspended" {
		t.Errorf("CLI token while suspended: %d %s", rec.Code, rec.Body)
	}

	// After 30 days the export ends; the account page still says why.
	now = now.Add(31 * 24 * time.Hour)
	if rec := do(t, h, session(request(http.MethodGet, "/v1/contacts"))); rec.Code != http.StatusForbidden {
		t.Errorf("export after 30 days: %d, want 403", rec.Code)
	}
	if rec := do(t, h, session(request(http.MethodGet, "/v1/account"))); rec.Code != http.StatusOK {
		t.Errorf("account after 30 days: %d", rec.Code)
	}

	if _, err := srv.Admin.Unsuspend(ctx, admin.Server, accountID, "Appeal accepted."); err != nil {
		t.Fatal(err)
	}
	rec = do(t, h, session(jsonRequest(http.MethodPost, "/v1/contacts", `{"email":"new@example.com"}`)))
	if rec.Code != http.StatusCreated {
		t.Errorf("add contact after unsuspend: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, withKey(request(http.MethodGet, "/v1/contacts"))); rec.Code != http.StatusUnauthorized {
		t.Errorf("key after unsuspend: %d, want 401 (keys stay revoked)", rec.Code)
	}
	var actions []string
	rows, _ := pool.Query(ctx, `SELECT action || ':' || actor FROM admin_actions WHERE account_id = $1 ORDER BY id`, accountID)
	for rows.Next() {
		var a string
		_ = rows.Scan(&a)
		actions = append(actions, a)
	}
	rows.Close()
	if strings.Join(actions, ",") != "suspend:mailgoosed,unsuspend:mailgoosed" {
		t.Errorf("logged %v", actions)
	}
	if rec := do(t, h, session(request(http.MethodGet, "/v1/sessions/current"))); strings.Contains(rec.Body.String(), "export_until") {
		t.Errorf("still shows a suspension: %s", rec.Body)
	}
}

// TestWaitingForReview: with SIGNUP_REVIEW=all a new account says it's
// waiting, can build but not launch, and stops waiting once approved;
// rejecting suspends it (MG-66).
func TestWaitingForReview(t *testing.T) {
	srv := authServer(t)
	srv.Auth.SignupReview = auth.ReviewAll
	h := srv.Handler()
	signedIn(t, h)
	inReview := func() bool {
		t.Helper()
		rec := do(t, h, withSession(request(http.MethodGet, "/v1/sessions/current"))(t))
		var cur struct {
			Account struct {
				InReview *bool `json:"in_review"`
			} `json:"account"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &cur); err != nil || cur.Account.InReview == nil {
			t.Fatalf("current session: %d %s", rec.Code, rec.Body)
		}
		return *cur.Account.InReview
	}
	if !inReview() {
		t.Fatal("a new account isn't waiting with SIGNUP_REVIEW=all")
	}
	if rec := do(t, h, withSession(jsonRequest(http.MethodPost, "/v1/contacts", `{"email":"ann@example.com"}`))(t)); rec.Code != http.StatusCreated {
		t.Errorf("add a contact while waiting: %d %s", rec.Code, rec.Body)
	}

	ctx := context.Background()
	pool := srv.DB.(*pgxpool.Pool)
	var accountID string
	if err := pool.QueryRow(ctx, `SELECT account_id FROM users WHERE email = 'pat@example.com'`).Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Admin.ApproveSignup(ctx, admin.Server, accountID, "Looks real."); err != nil {
		t.Fatal(err)
	}
	if inReview() {
		t.Error("still waiting once approved")
	}

	// Rejecting another suspends it.
	var other string
	if err := pool.QueryRow(ctx, `INSERT INTO accounts (name, status, review, review_at) VALUES ('Spam', 'active', 'waiting', now()) RETURNING id`).
		Scan(&other); err != nil {
		t.Fatal(err)
	}
	acc, err := srv.Admin.RejectSignup(ctx, admin.Server, other, "Throwaway address.")
	if err != nil || acc.SuspendedAt == nil {
		t.Fatalf("reject: %+v %v", acc, err)
	}
}
