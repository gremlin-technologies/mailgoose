package billing

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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
