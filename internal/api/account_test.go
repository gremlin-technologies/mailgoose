package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/domains"
)

type accountBody struct {
	Name           string   `json:"name"`
	FromName       *string  `json:"from_name"`
	FromEmail      *string  `json:"from_email"`
	PostalAddress  *string  `json:"postal_address"`
	TimeZone       string   `json:"time_zone"`
	MissingForSend []string `json:"missing_for_send"`
	SendLimit      struct {
		Daily        int        `json:"daily"`
		Usual        int        `json:"usual"`
		Reason       *string    `json:"reason"`
		LoweredUntil *time.Time `json:"lowered_until"`
	} `json:"send_limit"`
}

func TestAccountThroughTheAPI(t *testing.T) {
	h := authServer(t).Handler()
	signedIn(t, h)
	read := func(req func(*testing.T) *http.Request, want int) accountBody {
		t.Helper()
		rec := do(t, h, withSession(req)(t))
		if rec.Code != want {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		var b accountBody
		_ = json.Unmarshal(rec.Body.Bytes(), &b)
		return b
	}

	a := read(request(http.MethodGet, "/v1/account"), http.StatusOK)
	if a.TimeZone != "UTC" || len(a.MissingForSend) != 1 || a.MissingForSend[0] != "postal_address" {
		t.Errorf("new account = %+v", a)
	}
	if l := a.SendLimit; l.Daily != 1000 || l.Usual != 10000 || l.Reason == nil ||
		!strings.Contains(*l.Reason, "New accounts start at 1,000 a day") || l.LoweredUntil != nil {
		t.Errorf("new account's limit = %+v", l)
	}

	a = read(jsonRequest(http.MethodPatch, "/v1/account", accountUpdate), http.StatusOK)
	if a.Name != "Pat's Bakery" || a.PostalAddress == nil || *a.PostalAddress != "Pat's Bakery\n12 Main St\nSpringfield, IL 62701" ||
		len(a.MissingForSend) != 0 || a.MissingForSend == nil {
		t.Errorf("after update = %+v", a)
	}

	// Only the field sent changes, and null removes it.
	a = read(jsonRequest(http.MethodPatch, "/v1/account", `{"from_email":null}`), http.StatusOK)
	if a.FromEmail != nil || a.FromName == nil || a.TimeZone != "America/Chicago" {
		t.Errorf("after clearing from_email = %+v", a)
	}

	read(jsonRequest(http.MethodPatch, "/v1/account", `{"time_zone":"EST5EDT "}`), http.StatusUnprocessableEntity)
	read(jsonRequest(http.MethodPatch, "/v1/account", `{"plan":"paid"}`), http.StatusBadRequest)
	read(jsonRequest(http.MethodPatch, "/v1/account", `{"name":5}`), http.StatusBadRequest)
}

// A throttled account sees its lower limit, why, and until when (MG-61).
func TestAccountThrottledLimit(t *testing.T) {
	srv := authServer(t)
	h := srv.Handler()
	signedIn(t, h)
	rec := do(t, h, withSession(request(http.MethodGet, "/v1/account"))(t))
	var acct struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &acct)
	until := time.Now().Add(72 * time.Hour).UTC().Truncate(time.Second)
	if _, err := srv.DB.(*pgxpool.Pool).Exec(context.Background(), `
		UPDATE accounts SET daily_send_limit = 50000 WHERE id = $1;`, acct.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.DB.(*pgxpool.Pool).Exec(context.Background(), `
		INSERT INTO account_throttles (account_id, reason, created_at, until) VALUES ($1, 'Lowered automatically: too many complaints.', now(), $2)`,
		acct.ID, until); err != nil {
		t.Fatal(err)
	}
	rec = do(t, h, withSession(request(http.MethodGet, "/v1/account"))(t))
	var a accountBody
	_ = json.Unmarshal(rec.Body.Bytes(), &a)
	l := a.SendLimit
	if rec.Code != http.StatusOK || l.Daily != 1000 || l.LoweredUntil == nil || !l.LoweredUntil.Equal(until) || l.Reason == nil ||
		!strings.HasPrefix(*l.Reason, "Lowered automatically: too many complaints. It's 1,000 a day until") {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
}

func TestDeleteAccountThroughTheAPI(t *testing.T) {
	srv := authServer(t)
	h := srv.Handler()
	signedIn(t, h)
	send := func(req func(*testing.T) *http.Request) *httptest.ResponseRecorder {
		t.Helper()
		return do(t, h, withSession(req)(t))
	}
	if rec := send(jsonRequest(http.MethodPost, "/v1/domains", `{"name":"news.example.com"}`)); rec.Code != http.StatusCreated {
		t.Fatalf("add domain: %d %s", rec.Code, rec.Body)
	}
	var acct accountBody
	if err := json.Unmarshal(send(request(http.MethodGet, "/v1/account")).Body.Bytes(), &acct); err != nil || acct.Name == "" {
		t.Fatalf("account: %+v %v", acct, err)
	}

	if rec := send(request(http.MethodDelete, "/v1/account?confirm="+url.QueryEscape(strings.ToUpper(acct.Name)))); rec.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rec.Body.String(), "confirmation_mismatch") {
		t.Fatalf("wrong name: %d %s", rec.Code, rec.Body)
	}
	rec := send(request(http.MethodDelete, "/v1/account?confirm="+url.QueryEscape(acct.Name)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	cleared := false
	for _, c := range rec.Result().Cookies() {
		cleared = cleared || (c.Name == SessionCookie && c.MaxAge < 0)
	}
	if !cleared {
		t.Error("the session cookie should be cleared")
	}
	if rec := send(request(http.MethodGet, "/v1/account")); rec.Code != http.StatusUnauthorized {
		t.Errorf("after delete: %d %s", rec.Code, rec.Body)
	}
	if ses := srv.Domains.SES.(*domains.FakeSES); len(ses.Deleted) != 1 || ses.Deleted[0] != "news.example.com" {
		t.Errorf("SES identities deleted: %v", ses.Deleted)
	}
}
