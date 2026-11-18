package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client calls Stripe's API for Checkout sessions. It's a few form posts,
// not the whole Stripe SDK.
type Client struct {
	// SecretKey is the account's secret key (sk_test_... or sk_live_...).
	SecretKey string
	// BaseURL is Stripe's API; empty means https://api.stripe.com.
	BaseURL string
	// HTTP is the client to use; nil means one with a 30-second timeout.
	HTTP *http.Client
}

// StripeError is an error Stripe answered with.
type StripeError struct {
	Status  int
	Type    string
	Message string
}

func (e *StripeError) Error() string {
	return fmt.Sprintf("stripe: %d %s: %s", e.Status, e.Type, e.Message)
}

// checkoutForm is the Checkout session for a pack: a one-off card payment
// for the pack at its price in US dollars, sales tax worked out by Stripe
// Tax (on top of the price) and the billing address collected for it.
// The account and pack ride in the metadata for the webhook.
func checkoutForm(r SessionRequest) url.Values {
	f := url.Values{}
	f.Set("mode", "payment")
	f.Set("success_url", r.SuccessURL)
	f.Set("cancel_url", r.CancelURL)
	f.Set("client_reference_id", r.AccountID)
	f.Set("line_items[0][quantity]", "1")
	f.Set("line_items[0][price_data][currency]", "usd")
	f.Set("line_items[0][price_data][unit_amount]", strconv.FormatInt(r.Pack.PriceCents, 10))
	f.Set("line_items[0][price_data][tax_behavior]", "exclusive")
	f.Set("line_items[0][price_data][product_data][name]", fmt.Sprintf("%s mailgoose credits", groupThousands(r.Pack.Credits)))
	f.Set("line_items[0][price_data][product_data][description]", "One credit sends one email to one person. Credits last 24 months.")
	f.Set("automatic_tax[enabled]", "true")
	f.Set("billing_address_collection", "required")
	if r.Email != "" {
		// Prefills the address and has Stripe email a receipt for the payment.
		f.Set("customer_email", r.Email)
		f.Set("payment_intent_data[receipt_email]", r.Email)
	}
	f.Set("metadata[account_id]", r.AccountID)
	f.Set("metadata[pack]", r.Pack.Name)
	f.Set("metadata[credits]", strconv.FormatInt(r.Pack.Credits, 10))
	f.Set("payment_intent_data[metadata][account_id]", r.AccountID)
	f.Set("payment_intent_data[metadata][pack]", r.Pack.Name)
	return f
}

// groupThousands writes 10000 as "10,000".
func groupThousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// CreateCheckoutSession makes a Checkout session for a pack.
func (c *Client) CreateCheckoutSession(ctx context.Context, r SessionRequest) (*Session, error) {
	base := c.BaseURL
	if base == "" {
		base = "https://api.stripe.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/v1/checkout/sessions",
		strings.NewReader(checkoutForm(r).Encode()))
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.SecretKey, "")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if r.IdempotencyKey != "" {
		req.Header.Set("Idempotency-Key", r.IdempotencyKey)
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("stripe: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("stripe: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		return nil, &StripeError{Status: resp.StatusCode, Type: e.Error.Type, Message: e.Error.Message}
	}
	var out struct {
		ID        string `json:"id"`
		URL       string `json:"url"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.ID == "" || !strings.HasPrefix(out.URL, "https://") {
		return nil, fmt.Errorf("stripe: unexpected checkout session answer")
	}
	return &Session{ID: out.ID, URL: out.URL, ExpiresAt: time.Unix(out.ExpiresAt, 0).UTC()}, nil
}
