package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/credits"
)

// TestCreditsThroughTheAPI signs up and in, so the account is activated and
// gets its first free grant, then reads the balance and history.
func TestCreditsThroughTheAPI(t *testing.T) {
	s := authServer(t)
	h := s.Handler()
	signedIn(t, h)

	rec := do(t, h, withSession(request(http.MethodGet, "/v1/credits"))(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("getCredits: %d %s", rec.Code, rec.Body)
	}
	var sum struct {
		Balance     int64   `json:"balance"`
		Plan        string  `json:"plan"`
		NextGrantAt *string `json:"next_grant_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &sum); err != nil {
		t.Fatal(err)
	}
	if sum.Balance != credits.FreeMonthlyGrant || sum.Plan != "free" || sum.NextGrantAt == nil {
		t.Errorf("summary = %+v", sum)
	}

	// Three spends make four transactions; read them two at a time.
	var accountID string
	pool := s.DB.(*pgxpool.Pool)
	if err := pool.QueryRow(context.Background(), `SELECT id FROM accounts`).Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := s.Credits.Debit(context.Background(), accountID, credits.TypeSpend, 1, ""); err != nil {
			t.Fatal(err)
		}
	}
	type page struct {
		Data []struct {
			ID     string `json:"id"`
			Type   string `json:"type"`
			Amount int64  `json:"amount"`
		} `json:"data"`
		NextCursor *string `json:"next_cursor"`
	}
	var types []string
	path := "/v1/credits/transactions?limit=2"
	for {
		rec := do(t, h, withSession(request(http.MethodGet, path))(t))
		if rec.Code != http.StatusOK {
			t.Fatalf("list: %d %s", rec.Code, rec.Body)
		}
		var p page
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		for _, d := range p.Data {
			types = append(types, d.Type)
		}
		if p.NextCursor == nil {
			break
		}
		path = "/v1/credits/transactions?limit=2&cursor=" + *p.NextCursor
	}
	if len(types) != 4 || types[0] != "spend" || types[3] != "grant" {
		t.Errorf("history = %v", types)
	}
}
