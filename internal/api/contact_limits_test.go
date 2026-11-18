package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/contacts"
)

// atContactLimit fills every account the server's test made so far with
// contacts.FreeContactLimit subscribed contacts.
func atContactLimit(server func(*testing.T) *Server) func(*testing.T) *Server {
	return func(t *testing.T) *Server {
		t.Helper()
		s := server(t)
		if _, err := s.DB.(*pgxpool.Pool).Exec(context.Background(), `
			INSERT INTO contacts (account_id, email, status, source)
			SELECT a.id, 'filler' || g || '@example.com', 'subscribed', 'api'
			FROM accounts a, generate_series(1, $1) g`, contacts.FreeContactLimit); err != nil {
			t.Fatal(err)
		}
		return s
	}
}

func TestCreateContactOverFreeLimit(t *testing.T) {
	h := atContactLimit(fullKey(authServer))(t).Handler()
	rec := do(t, h, bearer(jsonRequest(http.MethodPost, "/v1/contacts", `{"email":"over@example.com"}`))(t))
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Error struct {
			Code    string         `json:"code"`
			Hint    string         `json:"hint"`
			Details map[string]int `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "contact_limit" || body.Error.Details["limit"] != 500 || body.Error.Details["count"] != 500 || body.Error.Hint == "" {
		t.Errorf("body = %s", rec.Body)
	}

	// An unsubscribed contact doesn't count, so it still goes in.
	rec = do(t, h, bearer(jsonRequest(http.MethodPost, "/v1/contacts", `{"email":"gone@example.com","status":"unsubscribed"}`))(t))
	if rec.Code != http.StatusCreated {
		t.Errorf("unsubscribed at the limit: %d %s", rec.Code, rec.Body)
	}
}

// Errors without details leave the field out.
func TestErrorDetailsOmittedWhenEmpty(t *testing.T) {
	h := fullKey(authServer)(t).Handler()
	rec := do(t, h, bearer(jsonRequest(http.MethodPost, "/v1/contacts", `{"email":"pat"}`))(t))
	var body map[string]map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if _, ok := body["error"]["details"]; ok || rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
}
