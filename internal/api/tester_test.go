package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestAdminTesterFlow: an admin grants, extends and revokes a tester
// account (MG-86), whose state shows in the account settings and the admin
// view; bad requests and accounts not approved are refused.
func TestAdminTesterFlow(t *testing.T) {
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
	if acct := do(http.MethodGet, "/v1/account", "", http.StatusOK); acct["tester"] != nil {
		t.Errorf("tester before = %v", acct["tester"])
	}
	granted := do(http.MethodPost, "/v1/admin/accounts/ACCOUNT/grant-tester", `{"credits":200,"reason":"Invited to test"}`, http.StatusOK)
	st, ok := granted["tester"].(map[string]any)
	if !ok || st["credits"] != float64(200) || st["left"] != float64(200) || st["used"] != float64(0) || st["ended"] != false {
		t.Fatalf("granted = %v", granted["tester"])
	}
	if granted["sending_mode"] != "full" {
		t.Errorf("sending mode %v", granted["sending_mode"])
	}
	acct := do(http.MethodGet, "/v1/account", "", http.StatusOK)
	if st, ok := acct["tester"].(map[string]any); !ok || st["left"] != float64(200) {
		t.Errorf("account tester = %v", acct["tester"])
	}
	ext := do(http.MethodPost, "/v1/admin/accounts/ACCOUNT/grant-tester", `{"until":"2099-01-01T00:00:00Z","reason":"More time"}`,
		http.StatusUnprocessableEntity)
	if e, _ := ext["error"].(map[string]any); e == nil || e["details"].(map[string]any)["field"] != "until" {
		t.Errorf("too far = %v", ext)
	}
	for _, body := range []string{`{"credits":0,"reason":"x"}`, `{"credits":10001,"reason":"x"}`, `{"credits":5,"reason":" "}`} {
		do(http.MethodPost, "/v1/admin/accounts/ACCOUNT/grant-tester", body, http.StatusUnprocessableEntity)
	}

	revoked := do(http.MethodPost, "/v1/admin/accounts/ACCOUNT/revoke-tester", adminReasonBody, http.StatusOK)
	if revoked["tester"] != nil || revoked["sending_mode"] != "test" {
		t.Errorf("revoked = %v, %v", revoked["tester"], revoked["sending_mode"])
	}
	if again := do(http.MethodPost, "/v1/admin/accounts/ACCOUNT/revoke-tester", adminReasonBody, http.StatusConflict); errCode(again) != "not_tester" {
		t.Errorf("revoke again = %v", again)
	}

	db, _ := testDBs.Load(t)
	acc, _ := adminAccounts.Load(t)
	if _, err := db.(*pgxpool.Pool).Exec(t.Context(), `UPDATE accounts SET review = 'waiting', review_at = now() WHERE id = $1`, acc); err != nil {
		t.Fatal(err)
	}
	if got := do(http.MethodPost, "/v1/admin/accounts/ACCOUNT/grant-tester", adminReasonBody, http.StatusConflict); errCode(got) != "signup_not_approved" {
		t.Errorf("waiting = %v", got)
	}
	do(http.MethodPost, "/v1/admin/accounts/"+noSuchID+"/grant-tester", adminReasonBody, http.StatusNotFound)

	log := do(http.MethodGet, "/v1/admin/actions?account_id=ACCOUNT&limit=5", "", http.StatusOK)
	var actions []string
	for _, a := range log["data"].([]any) {
		actions = append(actions, a.(map[string]any)["action"].(string))
	}
	if strings.Join(actions, ",") != "revoke_tester,grant_tester" {
		t.Errorf("actions %v", actions)
	}
}

func errCode(body map[string]any) any {
	e, _ := body["error"].(map[string]any)
	return e["code"]
}

// testerTarget is targetAccount, a tester account.
func testerTarget(t *testing.T, h http.Handler) {
	t.Helper()
	targetAccount(t, h)
	id, _ := targetAccounts.Load(t)
	db, _ := testDBs.Load(t)
	if _, err := db.(*pgxpool.Pool).Exec(t.Context(), `UPDATE accounts SET tester_started_at = now(),
		tester_until = now() + interval '30 days', tester_credits = 500 WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
}

// testerUsedUp is readyCampaign on a tester account with its one credit
// used.
func testerUsedUp(t *testing.T, h http.Handler) {
	t.Helper()
	readyCampaign(t, h)
	id, _ := addedCampaigns.Load(t)
	v, _ := testDBs.Load(t)
	db := v.(*pgxpool.Pool)
	for _, q := range []string{
		`UPDATE accounts SET tester_started_at = now() - interval '1 day', tester_until = now() + interval '30 days',
			tester_credits = 1 WHERE id = (SELECT account_id FROM campaigns WHERE id = $1)`,
		`INSERT INTO test_sends (account_id, sent_at) SELECT account_id, now() - interval '1 hour' FROM campaigns WHERE id = $1`,
	} {
		if _, err := db.Exec(t.Context(), q, id); err != nil {
			t.Fatal(err)
		}
	}
}

// TestTesterTestSendRefused: a tester with no credits left gets 422
// tester_limit with its numbers, and a test to the simulator still goes.
func TestTesterTestSendRefused(t *testing.T) {
	srv := authServer(t)
	h := srv.Handler()
	fullKey(func(*testing.T) *Server { return srv })(t)
	testerUsedUp(t, h)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, bearer(campaignTest(""))(t))
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	e, _ := out["error"].(map[string]any)
	if rec.Code != http.StatusUnprocessableEntity || e["code"] != "tester_limit" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if d, _ := e["details"].(map[string]any); d["left"] != float64(0) || d["credits"] != float64(1) || d["needed"] != float64(1) {
		t.Errorf("details %v", e["details"])
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, bearer(campaignTest(`{"to":["success@simulator.amazonses.com"]}`))(t))
	if rec.Code != http.StatusOK {
		t.Errorf("to the simulator: %d %s", rec.Code, rec.Body)
	}
}
