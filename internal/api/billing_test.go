package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/billing"
)

const testWebhookSecret = "whsec_api_test"

// fakeStripe makes Checkout sessions without Stripe.
type fakeStripe struct {
	mu  sync.Mutex
	got []billing.SessionRequest
	err error
}

func (f *fakeStripe) CreateCheckoutSession(_ context.Context, r billing.SessionRequest) (*billing.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, r)
	if f.err != nil {
		return nil, f.err
	}
	return &billing.Session{ID: "cs_test_1", URL: "https://checkout.stripe.test/c/pay/cs_test_1",
		ExpiresAt: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)}, nil
}

func checkoutRequest(body string) func(*testing.T) *http.Request {
	return jsonRequest(http.MethodPost, "/v1/credits/checkout", body)
}

// stripeRefuses is a server whose Stripe turns every session down.
func stripeRefuses(next func(*testing.T) *Server) func(*testing.T) *Server {
	return func(t *testing.T) *Server {
		s := next(t)
		s.Billing.Stripe = &fakeStripe{err: &billing.StripeError{Status: 400, Type: "invalid_request_error", Message: "no"}}
		return s
	}
}

// noStripe is a server without Stripe keys.
func noStripe(next func(*testing.T) *Server) func(*testing.T) *Server {
	return func(t *testing.T) *Server {
		s := next(t)
		s.Billing.Stripe = nil
		return s
	}
}

func TestCreditCheckout(t *testing.T) {
	s := fullKey(authServer)(t)
	f := &fakeStripe{}
	s.Billing.Stripe = f
	h := s.Handler()

	req := bearer(checkoutRequest(`{"pack":"50k"}`))(t)
	req.Header.Set("Idempotency-Key", "buy-1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var out struct {
		URL        string `json:"url"`
		Pack       string `json:"pack"`
		Credits    int    `json:"credits"`
		PriceCents int    `json:"price_cents"`
		Currency   string `json:"currency"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.URL != "https://checkout.stripe.test/c/pay/cs_test_1" || out.Pack != "50k" || out.Credits != 50000 ||
		out.PriceCents != 7500 || out.Currency != "usd" {
		t.Errorf("checkout %+v", out)
	}
	if len(f.got) != 1 {
		t.Fatalf("%d sessions", len(f.got))
	}
	got := f.got[0]
	if got.AccountID == "" || got.Email == "" || got.SuccessURL != "https://app.mailgoose.test/credits?checkout=done" ||
		!strings.HasPrefix(got.IdempotencyKey, "mailgoose-checkout:"+got.AccountID+":") {
		t.Errorf("session request %+v", got)
	}

	// The unknown pack's error lists the packs.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, bearer(checkoutRequest(`{"pack":"5k"}`))(t))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"name":"200k"`) {
		t.Errorf("unknown pack: %d %s", rec.Code, rec.Body)
	}
}

func TestStripeWebhook(t *testing.T) {
	s := fullKey(authServer)(t)
	h := s.Handler()
	db, _ := testDBs.Load(t)
	pool := db.(*pgxpool.Pool)
	var acc string
	if err := pool.QueryRow(t.Context(), `SELECT account_id FROM users LIMIT 1`).Scan(&acc); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{
		"id": "evt_1", "type": "checkout.session.completed",
		"data": map[string]any{"object": map[string]any{
			"id": "cs_1", "payment_status": "paid", "client_reference_id": acc, "amount_subtotal": 2000, "currency": "usd",
			"metadata": map[string]string{"account_id": acc, "pack": "10k", "credits": "10000"},
		}},
	})
	post := func(sig string, method string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, StripeWebhookPath, bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if sig != "" {
			r.Header.Set("Stripe-Signature", sig)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	if rec := post("", http.MethodPost); rec.Code != http.StatusBadRequest {
		t.Errorf("unsigned: %d %s", rec.Code, rec.Body)
	}
	if rec := post(billing.Sign(body, "whsec_other", time.Now()), http.MethodPost); rec.Code != http.StatusBadRequest {
		t.Errorf("other secret: %d %s", rec.Code, rec.Body)
	}
	if rec := post("", http.MethodGet); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: %d", rec.Code)
	}
	rec := post(billing.Sign(body, testWebhookSecret, time.Now()), http.MethodPost)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"credited"`) {
		t.Fatalf("signed: %d %s", rec.Code, rec.Body)
	}
	rec = post(billing.Sign(body, testWebhookSecret, time.Now()), http.MethodPost)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"duplicate"`) {
		t.Errorf("again: %d %s", rec.Code, rec.Body)
	}
	var plan string
	var bought int64
	if err := pool.QueryRow(t.Context(), `SELECT plan, (SELECT coalesce(sum(amount), 0) FROM credit_transactions
		WHERE account_id = a.id AND type = 'purchase') FROM accounts a WHERE id = $1`, acc).Scan(&plan, &bought); err != nil {
		t.Fatal(err)
	}
	if plan != "paid" || bought != 10000 {
		t.Errorf("plan %s, bought %d", plan, bought)
	}

	// Without a database it asks Stripe to try again later.
	rec = httptest.NewRecorder()
	newServer(nil).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, StripeWebhookPath, bytes.NewReader(body)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no database: %d", rec.Code)
	}
}
