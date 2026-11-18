package billing

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestCreateCheckoutSession(t *testing.T) {
	var form url.Values
	var user, idem string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/checkout/sessions" {
			http.NotFound(w, r)
			return
		}
		user, _, _ = r.BasicAuth()
		idem = r.Header.Get("Idempotency-Key")
		b, _ := io.ReadAll(r.Body)
		form, _ = url.ParseQuery(string(b))
		_, _ = io.WriteString(w, `{"id":"cs_test_1","url":"https://checkout.stripe.com/c/pay/cs_test_1","expires_at":1795521600}`)
	}))
	defer srv.Close()

	c := &Client{SecretKey: "sk_test_abc", BaseURL: srv.URL}
	sess, err := c.CreateCheckoutSession(context.Background(), SessionRequest{
		AccountID: "acc_1", Email: "pat@bakery.example", Pack: DefaultPacks[0], SuccessURL: "https://app.example/credits?checkout=done",
		CancelURL: "https://app.example/credits?checkout=cancelled", IdempotencyKey: "k1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if sess.ID != "cs_test_1" || sess.URL != "https://checkout.stripe.com/c/pay/cs_test_1" || !sess.ExpiresAt.Equal(time.Unix(1795521600, 0)) {
		t.Errorf("session %+v", sess)
	}
	if user != "sk_test_abc" || idem != "k1" {
		t.Errorf("auth %q, idempotency key %q", user, idem)
	}
	want := map[string]string{
		"mode":                                          "payment",
		"client_reference_id":                           "acc_1",
		"line_items[0][quantity]":                       "1",
		"line_items[0][price_data][currency]":           "usd",
		"line_items[0][price_data][unit_amount]":        "2000",
		"line_items[0][price_data][tax_behavior]":       "exclusive",
		"line_items[0][price_data][product_data][name]": "10,000 mailgoose credits",
		"automatic_tax[enabled]":                        "true",
		"billing_address_collection":                    "required",
		"customer_email":                                "pat@bakery.example",
		"payment_intent_data[receipt_email]":            "pat@bakery.example",
		"metadata[account_id]":                          "acc_1",
		"metadata[pack]":                                "10k",
		"metadata[credits]":                             "10000",
		"metadata[price_cents]":                         "2000",
		"success_url":                                   "https://app.example/credits?checkout=done",
		"cancel_url":                                    "https://app.example/credits?checkout=cancelled",
	}
	for k, v := range want {
		if got := form.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

func TestCreateCheckoutSessionErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		stripe bool
	}{
		{"refused", http.StatusBadRequest, `{"error":{"type":"invalid_request_error","message":"Stripe Tax isn't set up"}}`, true},
		{"bad key", http.StatusUnauthorized, `{"error":{"type":"invalid_request_error","message":"Invalid API Key"}}`, true},
		{"down", http.StatusServiceUnavailable, `oops`, true},
		{"no url", http.StatusOK, `{"id":"cs_1","url":""}`, false},
		{"not json", http.StatusOK, `<html>`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(c.status)
				_, _ = io.WriteString(w, c.body)
			}))
			defer srv.Close()
			_, err := (&Client{SecretKey: "sk_test_abc", BaseURL: srv.URL}).CreateCheckoutSession(context.Background(),
				SessionRequest{AccountID: "a", Pack: DefaultPacks[0]})
			var se *StripeError
			if err == nil || errors.As(err, &se) != c.stripe {
				t.Fatalf("err %v", err)
			}
			if c.stripe && se.Status != c.status {
				t.Errorf("status %d", se.Status)
			}
		})
	}
}

func TestGroupThousands(t *testing.T) {
	for n, want := range map[int64]string{1: "1", 999: "999", 1000: "1,000", 10000: "10,000", 1000000: "1,000,000"} {
		if got := groupThousands(n); got != want {
			t.Errorf("%d: %q", n, got)
		}
	}
}

// stripeServer records each request and answers with body.
func stripeServer(t *testing.T, body string) (*Client, *[]*http.Request, *[]url.Values) {
	t.Helper()
	var reqs []*http.Request
	var forms []url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f, _ := url.ParseQuery(string(b))
		reqs, forms = append(reqs, r), append(forms, f)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return &Client{SecretKey: "sk_test_abc", BaseURL: srv.URL}, &reqs, &forms
}

func TestCreateSubscriptionCheckout(t *testing.T) {
	c, reqs, forms := stripeServer(t, `{"id":"cs_sub","url":"https://checkout.stripe.com/c/pay/cs_sub","expires_at":1795521600}`)
	req := SubscriptionRequest{AccountID: "acc_1", Email: "pat@bakery.example", Interval: Yearly, UnitCents: 5000, Units: 2,
		SuccessURL: "https://app.example/billing?checkout=done", CancelURL: "https://app.example/billing?checkout=cancelled",
		IdempotencyKey: "k2"}
	if _, err := c.CreateSubscriptionCheckout(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	r, f := (*reqs)[0], (*forms)[0]
	if r.URL.Path != "/v1/checkout/sessions" || r.Header.Get("Idempotency-Key") != "k2" {
		t.Errorf("%s %s", r.URL.Path, r.Header.Get("Idempotency-Key"))
	}
	want := map[string]string{
		"mode":                                           "subscription",
		"client_reference_id":                            "acc_1",
		"line_items[0][quantity]":                        "2",
		"line_items[0][price_data][currency]":            "usd",
		"line_items[0][price_data][unit_amount]":         "5000",
		"line_items[0][price_data][tax_behavior]":        "exclusive",
		"line_items[0][price_data][recurring][interval]": "year",
		"line_items[0][price_data][product_data][name]":  "mailgoose paid plan",
		"automatic_tax[enabled]":                         "true",
		"billing_address_collection":                     "required",
		"customer_email":                                 "pat@bakery.example",
		"metadata[account_id]":                           "acc_1",
		"subscription_data[metadata][account_id]":        "acc_1",
		"success_url":                                    "https://app.example/billing?checkout=done",
	}
	for k, v := range want {
		if got := f.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if f.Has("payment_intent_data[receipt_email]") || f.Has("customer") {
		t.Errorf("unexpected fields %v", f)
	}

	// A returning customer is reused, not made again.
	req.CustomerID = "cus_9"
	if _, err := c.CreateSubscriptionCheckout(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	f = (*forms)[1]
	if f.Get("customer") != "cus_9" || f.Has("customer_email") || f.Get("customer_update[address]") != "auto" {
		t.Errorf("returning customer %v", f)
	}
}

func TestPortalUnitsAndCancel(t *testing.T) {
	c, reqs, forms := stripeServer(t, `{"id":"bps_1","url":"https://billing.stripe.com/p/session/bps_1"}`)
	ctx := context.Background()
	url, err := c.CreatePortalSession(ctx, "cus_1", "https://app.example/billing")
	if err != nil || url != "https://billing.stripe.com/p/session/bps_1" {
		t.Fatalf("portal %q %v", url, err)
	}
	if err := c.SetSubscriptionUnits(ctx, "sub_1", "si_1", 3, "u1"); err != nil {
		t.Fatal(err)
	}
	if err := c.CancelSubscription(ctx, "sub_1"); err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, r := range *reqs {
		got = append(got, r.Method+" "+r.URL.Path+" "+r.Header.Get("Idempotency-Key"))
	}
	want := []string{"POST /v1/billing_portal/sessions ", "POST /v1/subscriptions/sub_1 u1", "DELETE /v1/subscriptions/sub_1 "}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("requests %v", got)
	}
	if f := (*forms)[0]; f.Get("customer") != "cus_1" || f.Get("return_url") != "https://app.example/billing" {
		t.Errorf("portal form %v", f)
	}
	if f := (*forms)[1]; f.Get("items[0][id]") != "si_1" || f.Get("items[0][quantity]") != "3" || f.Get("proration_behavior") != "none" {
		t.Errorf("units form %v", f)
	}

	// An answer with no URL is Stripe not answering properly.
	c, _, _ = stripeServer(t, `{"id":"bps_1"}`)
	if _, err := c.CreatePortalSession(ctx, "cus_1", ""); !errors.Is(err, ErrStripeUnreachable) {
		t.Errorf("no url: %v", err)
	}
}
