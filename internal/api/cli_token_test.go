package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/gremlin-technologies/mailgoose/internal/auth"
)

// TestCLISignup is `mailgoose signup` against the API: sign up, then trade
// the emailed code for a CLI token, which activates the account.
func TestCLISignup(t *testing.T) {
	h := authServer(t).Handler()
	signedUp(t, h)

	rec := do(t, h, jsonRequest(http.MethodPost, "/v1/cli-tokens",
		`{"email":"Pat@Example.com","code":"`+lastCode(t)+`","client_name":"pat-laptop"}`)(t))
	if rec.Code != http.StatusCreated {
		t.Fatalf("cli token: %d %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Set-Cookie") != "" {
		t.Error("a CLI token response set a cookie")
	}
	var tok struct {
		Token   string `json:"token"`
		KeyID   string `json:"key_id"`
		Account struct {
			Status string `json:"status"`
		} `json:"account"`
		User struct {
			Email string `json:"email"`
		} `json:"user"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &tok); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok.Token, "mg_cli_") || tok.KeyID == "" || tok.Account.Status != "active" || tok.User.Email != "pat@example.com" {
		t.Errorf("token = %+v", tok)
	}

	// The token works, and is a full-scope CLI token.
	req := request(http.MethodGet, "/v1/whoami")(t)
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	rec = do(t, h, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"auth_method":"cli_token"`) || !strings.Contains(rec.Body.String(), `"scope":"full"`) {
		t.Errorf("whoami: %d %s", rec.Code, rec.Body)
	}

	// The code is used up.
	rec = do(t, h, jsonRequest(http.MethodPost, "/v1/cli-tokens",
		`{"email":"pat@example.com","code":"`+lastCode(t)+`"}`)(t))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "invalid_code") {
		t.Errorf("second use: %d %s", rec.Code, rec.Body)
	}
}

// TestCLITokenLocksAfterWrongTries: the CLI path counts wrong tries like the
// web path, so it is no way around the lock.
func TestCLITokenLocksAfterWrongTries(t *testing.T) {
	h := authServer(t).Handler()
	signedUp(t, h)
	code := lastCode(t)
	for i := range 5 {
		if rec := do(t, h, cliTokenRequest("000000")(t)); rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("wrong try %d: %d", i+1, rec.Code)
		}
	}
	if code == "000000" {
		t.Skip("the real code happened to be 000000")
	}
	if rec := do(t, h, cliTokenRequest(code)(t)); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("right code after 5 wrong tries: %d %s", rec.Code, rec.Body)
	}
	// The web path is locked too: it is the same code.
	if rec := do(t, h, sessionRequest(code)(t)); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("web session after the lock: %d", rec.Code)
	}
}

// The per-address cap on wrong codes covers the CLI path too, and counts
// wrong codes from both paths together.
func TestCLITokenCountsTowardWrongCodeCap(t *testing.T) {
	h := authServer(t).Handler()
	for i := range auth.MaxWrongCodesPerEmail {
		req := sessionRequest("123456")
		if i%2 == 1 {
			req = cliTokenRequest("123456")
		}
		if rec := do(t, h, req(t)); rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("wrong try %d: %d %s", i+1, rec.Code, rec.Body)
		}
	}
	if rec := do(t, h, cliTokenRequest("123456")(t)); rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" ||
		!strings.Contains(rec.Body.String(), "too_many_wrong_codes") {
		t.Errorf("over the cap: %d %s", rec.Code, rec.Body)
	}
}
