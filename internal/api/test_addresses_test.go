package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/mail"
	"github.com/gremlin-technologies/mailgoose/internal/testmode"
)

// Test addresses (MG-80).

const testAddressBody = `{"email":"qa@example.com"}`

// testAddressIDs holds the id of each test's test address, added by
// addedTestAddress.
var testAddressIDs sync.Map // *testing.T -> string

// addedTestAddress adds qa@example.com with the test's key.
func addedTestAddress(t *testing.T, h http.Handler) {
	t.Helper()
	rec := do(t, h, bearer(jsonRequest(http.MethodPost, "/v1/test-addresses", testAddressBody))(t))
	if rec.Code != http.StatusCreated {
		t.Fatalf("add a test address: %d %s", rec.Code, rec.Body)
	}
	var a struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	testAddressIDs.Store(t, a.ID)
	t.Cleanup(func() { testAddressIDs.Delete(t) })
}

// changedTestAddress is addedTestAddress, then sql on it ($1 is its id).
func changedTestAddress(sql string) func(*testing.T, http.Handler) {
	return func(t *testing.T, h http.Handler) {
		t.Helper()
		addedTestAddress(t, h)
		id, _ := testAddressIDs.Load(t)
		db, _ := testDBs.Load(t)
		if _, err := db.(*pgxpool.Pool).Exec(t.Context(), sql, id); err != nil {
			t.Fatal(err)
		}
	}
}

var (
	// resendableTestAddress's last link went two minutes ago.
	resendableTestAddress = changedTestAddress(`UPDATE test_addresses SET confirm_sent_at = now() - interval '2 minutes' WHERE id = $1`)
	confirmedTestAddress  = changedTestAddress(`UPDATE test_addresses SET confirmed_at = now() WHERE id = $1`)
	expiredTestAddress    = changedTestAddress(`UPDATE test_addresses SET confirm_expires_at = now() - interval '1 day' WHERE id = $1`)
)

// testAddressPath puts the test's test address id in place of ADDR.
func testAddressPath(method, path string) func(*testing.T) *http.Request {
	return func(t *testing.T) *http.Request {
		id := noSuchID
		if v, ok := testAddressIDs.Load(t); ok {
			id = v.(string)
		}
		return request(method, strings.ReplaceAll(path, "ADDR", id))(t)
	}
}

var confirmToken = regexp.MustCompile(`token=(t1[A-Za-z0-9_-]{43})`)

// lastConfirmToken is the token in the test's last confirm link.
func lastConfirmToken(t *testing.T) string {
	t.Helper()
	v, ok := outboxes.Load(t)
	if !ok {
		t.Fatal("no outbox for this test")
	}
	last, ok := v.(*mail.Fake).Last()
	if !ok {
		t.Fatal("no email sent")
	}
	m := confirmToken.FindStringSubmatch(last.Message.Text)
	if m == nil {
		t.Fatalf("no confirm link in %q", last.Message.Text)
	}
	return m[1]
}

// confirmRequest posts the token from the test's last confirm link.
func confirmRequest(t *testing.T) *http.Request {
	return jsonRequest(http.MethodPost, "/v1/test-addresses/confirm", `{"token":"`+lastConfirmToken(t)+`"}`)(t)
}

// unknownConfirm is a well-formed token for no address.
var unknownConfirm = jsonRequest(http.MethodPost, "/v1/test-addresses/confirm", `{"token":"t1`+strings.Repeat("A", 43)+`"}`)

// failingTestAddressMail makes the server's test address emails fail.
func failingTestAddressMail(server func(*testing.T) *Server) func(*testing.T) *Server {
	return func(t *testing.T) *Server {
		s := server(t)
		s.TestAddresses.Mail = &mail.Fake{Err: errors.New("SES is down")}
		return s
	}
}

// testModeTarget is targetAccount, in test mode.
func testModeTarget(t *testing.T, h http.Handler) {
	t.Helper()
	targetAccount(t, h)
	id, _ := targetAccounts.Load(t)
	db, _ := testDBs.Load(t)
	if _, err := db.(*pgxpool.Pool).Exec(t.Context(), `UPDATE accounts SET sending_mode = 'test' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
}

func init() {
	for op, cases := range map[string][]contractCase{
		"listTestAddresses": {
			{name: "listed", server: draftKey(authServer), before: addedTestAddress, req: bearer(request(http.MethodGet, "/v1/test-addresses")), status: http.StatusOK},
			{name: "no credentials", server: authServer, req: request(http.MethodGet, "/v1/test-addresses"), status: http.StatusUnauthorized},
			{name: "rate limited", server: fullKey(rateLimitedAuthServer), before: send(bearer(request(http.MethodGet, "/v1/test-addresses"))),
				req: bearer(request(http.MethodGet, "/v1/test-addresses")), status: http.StatusTooManyRequests},
			{name: "no database", server: serverWith(nil), req: bearer(request(http.MethodGet, "/v1/test-addresses")), status: http.StatusServiceUnavailable},
		},
		"addTestAddress": {
			{name: "added", server: fullKey(authServer), req: bearer(jsonRequest(http.MethodPost, "/v1/test-addresses", testAddressBody)), status: http.StatusCreated},
			{name: "malformed", server: fullKey(authServer), req: bearer(jsonRequest(http.MethodPost, "/v1/test-addresses", `{"email":`)), status: http.StatusBadRequest},
			{name: "no credentials", server: authServer, req: jsonRequest(http.MethodPost, "/v1/test-addresses", testAddressBody), status: http.StatusUnauthorized},
			{name: "there already", server: fullKey(authServer), before: addedTestAddress,
				req: bearer(jsonRequest(http.MethodPost, "/v1/test-addresses", `{"email":"QA@example.com"}`)), status: http.StatusConflict},
			{name: "not an address", server: fullKey(authServer), req: bearer(jsonRequest(http.MethodPost, "/v1/test-addresses", `{"email":"qa"}`)), status: http.StatusUnprocessableEntity},
			{name: "rate limited", server: fullKey(rateLimitedAuthServer), before: send(bearer(request(http.MethodGet, "/v1/test-addresses"))),
				req: bearer(jsonRequest(http.MethodPost, "/v1/test-addresses", testAddressBody)), status: http.StatusTooManyRequests},
			{name: "email failed", server: failingTestAddressMail(fullKey(authServer)), req: bearer(jsonRequest(http.MethodPost, "/v1/test-addresses", testAddressBody)), status: http.StatusBadGateway},
			{name: "no database", server: serverWith(nil), req: bearer(jsonRequest(http.MethodPost, "/v1/test-addresses", testAddressBody)), status: http.StatusServiceUnavailable},
		},
		"confirmTestAddress": {
			{name: "confirmed", server: fullKey(authServer), before: addedTestAddress, req: confirmRequest, status: http.StatusOK},
			{name: "malformed", server: authServer, req: jsonRequest(http.MethodPost, "/v1/test-addresses/confirm", `{"token":`), status: http.StatusBadRequest},
			{name: "not a token", server: authServer, req: jsonRequest(http.MethodPost, "/v1/test-addresses/confirm", `{"token":"nope"}`), status: http.StatusBadRequest},
			{name: "no such link", server: authServer, req: unknownConfirm, status: http.StatusNotFound},
			{name: "expired", server: fullKey(authServer), before: expiredTestAddress, req: confirmRequest, status: http.StatusGone},
			{name: "rate limited", server: rateLimitedAuthServer, before: send(unknownConfirm), req: unknownConfirm, status: http.StatusTooManyRequests},
			{name: "no database", server: serverWith(nil), req: unknownConfirm, status: http.StatusServiceUnavailable},
		},
		"removeTestAddress": {
			{name: "removed", server: fullKey(authServer), before: addedTestAddress, req: bearer(testAddressPath(http.MethodDelete, "/v1/test-addresses/ADDR")), status: http.StatusNoContent},
			{name: "bad id", server: fullKey(authServer), req: bearer(request(http.MethodDelete, "/v1/test-addresses/nope")), status: http.StatusBadRequest},
			{name: "no credentials", server: authServer, req: request(http.MethodDelete, "/v1/test-addresses/"+noSuchID), status: http.StatusUnauthorized},
			{name: "unknown", server: fullKey(authServer), req: bearer(request(http.MethodDelete, "/v1/test-addresses/"+noSuchID)), status: http.StatusNotFound},
			{name: "rate limited", server: fullKey(rateLimitedAuthServer), before: send(bearer(request(http.MethodGet, "/v1/test-addresses"))),
				req: bearer(request(http.MethodDelete, "/v1/test-addresses/"+noSuchID)), status: http.StatusTooManyRequests},
			{name: "no database", server: serverWith(nil), req: bearer(request(http.MethodDelete, "/v1/test-addresses/"+noSuchID)), status: http.StatusServiceUnavailable},
		},
		"resendTestAddress": {
			{name: "resent", server: fullKey(authServer), before: resendableTestAddress, req: bearer(testAddressPath(http.MethodPost, "/v1/test-addresses/ADDR/resend")), status: http.StatusOK},
			{name: "bad id", server: fullKey(authServer), req: bearer(request(http.MethodPost, "/v1/test-addresses/nope/resend")), status: http.StatusBadRequest},
			{name: "no credentials", server: authServer, req: request(http.MethodPost, "/v1/test-addresses/"+noSuchID+"/resend"), status: http.StatusUnauthorized},
			{name: "unknown", server: fullKey(authServer), req: bearer(request(http.MethodPost, "/v1/test-addresses/"+noSuchID+"/resend")), status: http.StatusNotFound},
			{name: "confirmed", server: fullKey(authServer), before: confirmedTestAddress, req: bearer(testAddressPath(http.MethodPost, "/v1/test-addresses/ADDR/resend")), status: http.StatusConflict},
			{name: "too soon", server: fullKey(authServer), before: addedTestAddress, req: bearer(testAddressPath(http.MethodPost, "/v1/test-addresses/ADDR/resend")), status: http.StatusTooManyRequests},
			{name: "email failed", server: fullKey(authServer), before: func(t *testing.T, h http.Handler) {
				resendableTestAddress(t, h)
				v, _ := outboxes.Load(t)
				v.(*mail.Fake).Err = errors.New("SES is down")
			}, req: bearer(testAddressPath(http.MethodPost, "/v1/test-addresses/ADDR/resend")), status: http.StatusBadGateway},
			{name: "no database", server: serverWith(nil), req: bearer(request(http.MethodPost, "/v1/test-addresses/"+noSuchID+"/resend")), status: http.StatusServiceUnavailable},
		},
		"listTestModeAccounts": {
			{name: "found", server: asAdmin(authServer), before: testModeTarget, req: bearer(request(http.MethodGet, "/v1/admin/test-mode")), status: http.StatusOK},
			{name: "bad limit", server: asAdmin(authServer), req: bearer(request(http.MethodGet, "/v1/admin/test-mode?limit=many")), status: http.StatusBadRequest},
			{name: "no credentials", server: authServer, req: request(http.MethodGet, "/v1/admin/test-mode"), status: http.StatusUnauthorized},
			{name: "not an admin", server: fullKey(authServer), req: bearer(request(http.MethodGet, "/v1/admin/test-mode")), status: http.StatusForbidden},
			{name: "rate limited", server: asAdmin(rateLimitedAuthServer), before: send(bearer(request(http.MethodGet, "/v1/campaigns"))), req: bearer(request(http.MethodGet, "/v1/admin/test-mode")), status: http.StatusTooManyRequests},
			{name: "no database", server: serverWith(nil), req: bearer(request(http.MethodGet, "/v1/admin/test-mode")), status: http.StatusServiceUnavailable},
		},
		"allowFullSending": modeSwitchCases("allow-full-sending", testModeTarget, targetAccount),
		"setTestMode":      modeSwitchCases("set-test-mode", targetAccount, testModeTarget),
	} {
		contractCases[op] = cases
	}
}

// modeSwitchCases are the contract cases of an admin switch at path, done
// on the account switchable makes, and refused for the one unchanged makes.
func modeSwitchCases(path string, switchable, unchanged func(*testing.T, http.Handler)) []contractCase {
	p := "/v1/admin/accounts/TARGET/" + path
	return []contractCase{
		{name: "done", server: asAdmin(authServer), before: switchable, req: bearer(adminPath(http.MethodPost, p, adminReasonBody)), status: http.StatusOK},
		{name: "malformed", server: asAdmin(authServer), before: switchable, req: bearer(adminPath(http.MethodPost, p, `{"reason":`)), status: http.StatusBadRequest},
		{name: "unknown", server: asAdmin(authServer), req: bearer(jsonRequest(http.MethodPost, "/v1/admin/accounts/"+noSuchID+"/"+path, adminReasonBody)), status: http.StatusNotFound},
		{name: "unchanged", server: asAdmin(authServer), before: unchanged, req: bearer(adminPath(http.MethodPost, p, adminReasonBody)), status: http.StatusConflict},
		{name: "no reason", server: asAdmin(authServer), before: switchable, req: bearer(adminPath(http.MethodPost, p, `{"reason":" "}`)), status: http.StatusUnprocessableEntity},
		{name: "no credentials", server: authServer, req: adminPath(http.MethodPost, p, adminReasonBody), status: http.StatusUnauthorized},
		{name: "not an admin", server: fullKey(authServer), req: bearer(adminPath(http.MethodPost, p, adminReasonBody)), status: http.StatusForbidden},
		{name: "rate limited", server: asAdmin(rateLimitedAuthServer), before: send(bearer(request(http.MethodGet, "/v1/campaigns"))), req: bearer(adminPath(http.MethodPost, p, adminReasonBody)), status: http.StatusTooManyRequests},
		{name: "no database", server: serverWith(nil), req: bearer(adminPath(http.MethodPost, p, adminReasonBody)), status: http.StatusServiceUnavailable},
	}
}

// TestTestAddressesThroughTheAPI adds, confirms, lists, resends and removes
// test addresses, up to the limit of 5 (MG-80).
func TestTestAddressesThroughTheAPI(t *testing.T) {
	s := fullKey(authServer)(t)
	h := s.Handler()
	add := func(email string) int {
		t.Helper()
		return do(t, h, bearer(jsonRequest(http.MethodPost, "/v1/test-addresses", `{"email":"`+email+`"}`))(t)).Code
	}
	if code := add("QA1@Example.com"); code != http.StatusCreated {
		t.Fatalf("add: %d", code)
	}
	// The link goes to the address, from mailgoose's own sender.
	v, _ := outboxes.Load(t)
	last, _ := v.(*mail.Fake).Last()
	if last.Message.To.Email != "qa1@example.com" || last.Message.Kind != mail.KindSystem ||
		!strings.Contains(last.Message.Text, testmode.ConfirmPath+"?token=") {
		t.Errorf("confirm email = %+v", last.Message)
	}
	rec := do(t, h, confirmRequest(t))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"email":"qa1@example.com"`) {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Body)
	}
	// Again changes nothing.
	if rec := do(t, h, confirmRequest(t)); rec.Code != http.StatusOK {
		t.Errorf("confirm again: %d %s", rec.Code, rec.Body)
	}
	// The owner's own address needs no slot.
	if code := add("key@example.com"); code != http.StatusUnprocessableEntity {
		t.Errorf("owner's address: %d", code)
	}
	for _, e := range []string{"qa2@example.com", "qa3@example.com", "qa4@example.com", "qa5@example.com"} {
		if code := add(e); code != http.StatusCreated {
			t.Fatalf("add %s: %d", e, code)
		}
	}
	rec = do(t, h, bearer(jsonRequest(http.MethodPost, "/v1/test-addresses", `{"email":"qa6@example.com"}`))(t))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "test_address_limit") {
		t.Fatalf("a 6th: %d %s", rec.Code, rec.Body)
	}

	rec = do(t, h, bearer(request(http.MethodGet, "/v1/test-addresses"))(t))
	var list struct {
		Data []struct {
			ID, Email, Status string
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Data) != 5 {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	if list.Data[0].Email != "qa1@example.com" || list.Data[0].Status != "confirmed" || list.Data[1].Status != "pending" {
		t.Errorf("list = %+v", list.Data)
	}

	// Removing one frees its slot.
	if rec := do(t, h, bearer(request(http.MethodDelete, "/v1/test-addresses/"+list.Data[4].ID))(t)); rec.Code != http.StatusNoContent {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body)
	}
	if code := add("qa6@example.com"); code != http.StatusCreated {
		t.Errorf("add once one is removed: %d", code)
	}
}

// TestSendingModeOnTheAccount: the session and the account settings say
// which mode the account is in (MG-80).
func TestSendingModeOnTheAccount(t *testing.T) {
	srv := authServer(t)
	srv.Auth.NewAccountSending = ""
	h := srv.Handler()
	signedIn(t, h)
	rec := do(t, h, withSession(request(http.MethodGet, "/v1/sessions/current"))(t))
	if !strings.Contains(rec.Body.String(), `"sending_mode":"test"`) {
		t.Errorf("session: %s", rec.Body)
	}
	rec = do(t, h, withSession(request(http.MethodGet, "/v1/account"))(t))
	if !strings.Contains(rec.Body.String(), `"sending_mode":"test"`) {
		t.Errorf("account: %s", rec.Body)
	}
}
