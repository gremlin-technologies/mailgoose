package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSessionCookie(t *testing.T) {
	h := authServer(t).Handler()
	signedUp(t, h)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, sessionRequest("")(t))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %v", cookies)
	}
	c := cookies[0]
	if c.Name != SessionCookie || c.Value == "" || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/" {
		t.Errorf("cookie = %+v", c)
	}
	if d := time.Duration(c.MaxAge) * time.Second; d < 29*24*time.Hour || d > 30*24*time.Hour {
		t.Errorf("Max-Age = %v, want about 30 days", d)
	}
}

// Signing up with an address that has an active account answers exactly as
// for a new address.
func TestSignupDoesNotRevealExistingAccounts(t *testing.T) {
	s := authServer(t)
	h := s.Handler()
	signedUp(t, h)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, sessionRequest("")(t))
	if rec.Code != http.StatusCreated {
		t.Fatalf("sign in: %d %s", rec.Code, rec.Body)
	}

	first := httptest.NewRecorder()
	h.ServeHTTP(first, jsonRequest(http.MethodPost, "/v1/signup", `{"email":"new@example.com","account_name":"New","accepted_terms":true}`)(t))
	again := httptest.NewRecorder()
	h.ServeHTTP(again, jsonRequest(http.MethodPost, "/v1/signup", signupBody)(t))
	if first.Code != again.Code || first.Body.String() != again.Body.String() {
		t.Errorf("new address: %d %s\nactive address: %d %s", first.Code, first.Body, again.Code, again.Body)
	}
}
