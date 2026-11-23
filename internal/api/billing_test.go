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
	mu       sync.Mutex
	got      []billing.SessionRequest
	subs     []billing.SubscriptionRequest
	portals  []string
	canceled []string
	err      error
}

func (f *fakeStripe) CreateSubscriptionCheckout(_ context.Context, r billing.SubscriptionRequest) (*billing.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subs = append(f.subs, r)
	if f.err != nil {
		return nil, f.err
	}
	return &billing.Session{ID: "cs_test_sub", URL: "https://checkout.stripe.test/c/pay/cs_test_sub",
		ExpiresAt: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)}, nil
}

func (f *fakeStripe) CreatePortalSession(_ context.Context, customerID, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.portals = append(f.portals, customerID)
	if f.err != nil {
		return "", f.err
	}
	return "https://billing.stripe.test/p/session/" + customerID, nil
}

func (f *fakeStripe) SetSubscriptionUnits(context.Context, string, string, int, string) error {
	return f.err
}

func (f *fakeStripe) GetSubscription(context.Context, string) ([]byte, error) {
	return nil, &billing.StripeError{Status: 404}
}

func (f *fakeStripe) CancelSubscription(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.canceled = append(f.canceled, id)
	return f.err
}

// makePaid puts the server's only account on a live monthly subscription.
func makePaid(t *testing.T, s *Server) string {
	t.Helper()
	var acc string
	if err := s.Billing.DB.QueryRow(t.Context(), `UPDATE accounts SET plan = 'paid', paid_since = now(),
		stripe_customer_id = 'cus_1', stripe_subscription_id = 'sub_1', subscription_item_id = 'si_1',
		subscription_status = 'active', billing_interval = 'month', plan_units = 1
		WHERE id = (SELECT account_id FROM users LIMIT 1) RETURNING id`).Scan(&acc); err != nil {
		t.Fatal(err)
	}
	return acc
}

// paidAccount is a server whose account is on the paid plan.
func paidAccount(next func(*testing.T) *Server) func(*testing.T) *Server {
	return func(t *testing.T) *Server {
		s := next(t)
		makePaid(t, s)
		return s
	}
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

	// A free account is sent to the plan first.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, bearer(checkoutRequest(`{"pack":"50k"}`))(t))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"plan_required"`) ||
		!strings.Contains(rec.Body.String(), "mailgoose plan upgrade") {
		t.Fatalf("free account: %d %s", rec.Code, rec.Body)
	}
	makePaid(t, s)

	req := bearer(checkoutRequest(`{"pack":"50k"}`))(t)
	req.Header.Set("Idempotency-Key", "buy-1")
	rec = httptest.NewRecorder()
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
	if plan != "free" || bought != 10000 {
		t.Errorf("plan %s, bought %d (buying credits doesn't change the plan)", plan, bought)
	}

	// A subscription event puts the account on the paid plan.
	sub, _ := json.Marshal(map[string]any{
		"id": "evt_2", "type": "customer.subscription.created", "created": time.Now().Unix(),
		"data": map[string]any{"object": map[string]any{
			"id": "sub_9", "customer": "cus_9", "status": "active", "metadata": map[string]string{"account_id": acc},
			"items": map[string]any{"data": []map[string]any{{"id": "si_9", "quantity": 1,
				"current_period_end": time.Now().Add(30 * 24 * time.Hour).Unix(),
				"price":              map[string]any{"currency": "usd", "recurring": map[string]any{"interval": "year"}}}}},
		}},
	})
	body = sub
	rec = post(billing.Sign(body, testWebhookSecret, time.Now()), http.MethodPost)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"subscription"`) {
		t.Fatalf("subscription: %d %s", rec.Code, rec.Body)
	}
	var interval string
	if err := pool.QueryRow(t.Context(), `SELECT plan, billing_interval FROM accounts WHERE id = $1`, acc).Scan(&plan, &interval); err != nil {
		t.Fatal(err)
	}
	if plan != "paid" || interval != "year" {
		t.Errorf("after subscribing: plan %s, interval %s", plan, interval)
	}

	// Without a database it asks Stripe to try again later.
	rec = httptest.NewRecorder()
	newServer(nil).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, StripeWebhookPath, bytes.NewReader(body)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no database: %d", rec.Code)
	}
}

func planRequest(body string) func(*testing.T) *http.Request {
	return jsonRequest(http.MethodPost, "/v1/plan/checkout", body)
}

func TestPlan(t *testing.T) {
	s := fullKey(authServer)(t)
	f := &fakeStripe{}
	s.Billing.Stripe = f
	h := s.Handler()
	get := func() map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, bearer(request(http.MethodGet, "/v1/plan"))(t))
		if rec.Code != http.StatusOK {
			t.Fatalf("get plan: %d %s", rec.Code, rec.Body)
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	p := get()
	prices, _ := p["prices"].(map[string]any)
	if p["plan"] != "free" || p["units_needed"] != float64(1) || prices["month_cents"] != float64(500) ||
		prices["year_cents"] != float64(5000) || p["status"] != nil {
		t.Errorf("free plan %v", p)
	}
	// The plan's terms come from the server's settings (MG-126).
	terms := func(p map[string]any) string {
		b, _ := json.Marshal(map[string]any{"included": p["included_contacts"], "step": p["contacts_per_unit"],
			"free": p["free_plan"], "paid": p["paid_sending"], "packs": p["credit_packs"]})
		return string(b)
	}
	if got, want := terms(p), `{"free":{"contacts":1000,"daily_sends":0,"monthly_credits":2500},"included":2500,`+
		`"packs":{"cheapest":{"credits":10000,"name":"10k","price_cents":2000},"months":24},`+
		`"paid":{"most_daily":10000,"needs_domain":false,"starting_daily":1000},"step":10000}`; got != want {
		t.Errorf("terms = %s\nwant    %s", got, want)
	}
	// With shared sending on, an account without a verified domain stays at
	// the starting limit; other settings change the terms with them.
	s.Sending.Limits.SharedDomain, s.Sending.Limits.FreeDaily, s.Sending.Limits.Daily = true, 500, 20000
	s.Billing.Packs = []billing.Pack{{Name: "big", Credits: 50_000, PriceCents: 9_000}, {Name: "small", Credits: 5_000, PriceCents: 1_500}}
	p = get()
	free, _ := p["free_plan"].(map[string]any)
	paid, _ := p["paid_sending"].(map[string]any)
	packs, _ := p["credit_packs"].(map[string]any)
	if free["daily_sends"] != float64(500) || paid["needs_domain"] != true || paid["most_daily"] != float64(20000) ||
		packs["cheapest"].(map[string]any)["name"] != "small" {
		t.Errorf("changed terms = %s", terms(p))
	}
	s.Sending.Limits = testLimits
	s.Billing.Packs = billing.DefaultPacks

	// Nothing to manage yet.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, bearer(jsonRequest(http.MethodPost, "/v1/plan/portal", ""))(t))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"not_subscribed"`) {
		t.Errorf("portal before subscribing: %d %s", rec.Code, rec.Body)
	}

	// A yearly checkout.
	req := bearer(planRequest(`{"interval":"year"}`))(t)
	req.Header.Set("Idempotency-Key", "plan-1")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("checkout: %d %s", rec.Code, rec.Body)
	}
	var out struct {
		URL            string `json:"url"`
		Interval       string `json:"interval"`
		Units          int    `json:"units"`
		UnitPriceCents int    `json:"unit_price_cents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.URL == "" || out.Interval != "year" || out.Units != 1 || out.UnitPriceCents != 5000 {
		t.Errorf("checkout %+v", out)
	}
	if len(f.subs) != 1 || f.subs[0].Interval != "year" || f.subs[0].UnitCents != 5000 ||
		f.subs[0].SuccessURL != "https://app.mailgoose.test/billing?checkout=done" ||
		!strings.HasPrefix(f.subs[0].IdempotencyKey, "mailgoose-plan:") {
		t.Errorf("subscription request %+v", f.subs)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, bearer(planRequest(`{"interval":"week"}`))(t))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unknown interval: %d %s", rec.Code, rec.Body)
	}

	// Once subscribed: no second checkout, and the portal opens.
	makePaid(t, s)
	if p := get(); p["plan"] != "paid" || p["status"] != "active" || p["interval"] != "month" || p["units"] != float64(1) {
		t.Errorf("paid plan %v", p)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, bearer(planRequest(`{"interval":"month"}`))(t))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"already_subscribed"`) {
		t.Errorf("second checkout: %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, bearer(jsonRequest(http.MethodPost, "/v1/plan/portal", ""))(t))
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), "cus_1") {
		t.Errorf("portal: %d %s", rec.Code, rec.Body)
	}
}

func TestDeleteAccountCancelsSubscription(t *testing.T) {
	s := fullKey(authServer)(t)
	f := &fakeStripe{err: &billing.StripeError{Status: 500, Type: "api_error", Message: "down"}}
	s.Billing.Stripe = f
	acc := makePaid(t, s)
	h := s.Handler()
	del := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, bearer(request(http.MethodDelete, "/v1/account?confirm=Test"))(t))
		return rec
	}
	// Stripe down: nothing is deleted.
	if rec := del(); rec.Code != http.StatusBadGateway {
		t.Fatalf("stripe down: %d %s", rec.Code, rec.Body)
	}
	var status string
	if err := s.Billing.DB.QueryRow(t.Context(), `SELECT status FROM accounts WHERE id = $1`, acc).Scan(&status); err != nil || status != "active" {
		t.Fatalf("account after a failed delete: %q %v", status, err)
	}
	f.err = nil
	if rec := del(); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if len(f.canceled) != 2 || f.canceled[1] != "sub_1" {
		t.Errorf("canceled %v", f.canceled)
	}
}
