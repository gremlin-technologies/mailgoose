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
// The account, the pack and its credits and price ride in the metadata for
// the webhook.
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
	f.Set("metadata[price_cents]", strconv.FormatInt(r.Pack.PriceCents, 10))
	f.Set("payment_intent_data[metadata][account_id]", r.AccountID)
	f.Set("payment_intent_data[metadata][pack]", r.Pack.Name)
	return f
}

// subscriptionForm is the Checkout session that starts the paid plan: a
// card subscription billed each month or year, Units plan units at the
// interval's price, with sales tax worked out by Stripe Tax on top. The
// account rides in the subscription's metadata, so every subscription event
// names it.
func subscriptionForm(r SubscriptionRequest) url.Values {
	f := url.Values{}
	f.Set("mode", "subscription")
	f.Set("success_url", r.SuccessURL)
	f.Set("cancel_url", r.CancelURL)
	f.Set("client_reference_id", r.AccountID)
	f.Set("line_items[0][quantity]", strconv.Itoa(r.Units))
	f.Set("line_items[0][price_data][currency]", "usd")
	f.Set("line_items[0][price_data][unit_amount]", strconv.FormatInt(r.UnitCents, 10))
	f.Set("line_items[0][price_data][tax_behavior]", "exclusive")
	f.Set("line_items[0][price_data][recurring][interval]", r.Interval)
	f.Set("line_items[0][price_data][product_data][name]", "mailgoose paid plan")
	f.Set("automatic_tax[enabled]", "true")
	f.Set("billing_address_collection", "required")
	switch {
	case r.CustomerID != "":
		// Coming back after cancelling: the same Stripe customer, so
		// invoices stay together. Tax needs the address Checkout collects.
		f.Set("customer", r.CustomerID)
		f.Set("customer_update[address]", "auto")
		f.Set("customer_update[name]", "auto")
	case r.Email != "":
		f.Set("customer_email", r.Email)
	}
	f.Set("metadata[account_id]", r.AccountID)
	f.Set("subscription_data[metadata][account_id]", r.AccountID)
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
	return c.checkout(ctx, checkoutForm(r), r.IdempotencyKey)
}

// CreateSubscriptionCheckout makes a Checkout session that starts the paid
// plan's subscription.
func (c *Client) CreateSubscriptionCheckout(ctx context.Context, r SubscriptionRequest) (*Session, error) {
	return c.checkout(ctx, subscriptionForm(r), r.IdempotencyKey)
}

func (c *Client) checkout(ctx context.Context, form url.Values, idempotencyKey string) (*Session, error) {
	body, err := c.call(ctx, http.MethodPost, "/v1/checkout/sessions", form, idempotencyKey)
	if err != nil {
		return nil, err
	}
	var out struct {
		ID        string `json:"id"`
		URL       string `json:"url"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.ID == "" || !strings.HasPrefix(out.URL, "https://") {
		return nil, fmt.Errorf("%w: unexpected checkout session answer", ErrStripeUnreachable)
	}
	return &Session{ID: out.ID, URL: out.URL, ExpiresAt: time.Unix(out.ExpiresAt, 0).UTC()}, nil
}

// CreatePortalSession makes a Stripe customer portal page, where a
// subscriber changes their card, sees invoices or cancels, and returns
// its URL.
func (c *Client) CreatePortalSession(ctx context.Context, customerID, returnURL string) (string, error) {
	f := url.Values{}
	f.Set("customer", customerID)
	f.Set("return_url", returnURL)
	body, err := c.call(ctx, http.MethodPost, "/v1/billing_portal/sessions", f, "")
	if err != nil {
		return "", err
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &out); err != nil || !strings.HasPrefix(out.URL, "https://") {
		return "", fmt.Errorf("%w: unexpected portal session answer", ErrStripeUnreachable)
	}
	return out.URL, nil
}

// SetSubscriptionUnits sets the plan's quantity from the next renewal on:
// no proration, so a list that grows mid-period pays for it at renewal.
func (c *Client) SetSubscriptionUnits(ctx context.Context, subscriptionID, itemID string, units int, idempotencyKey string) error {
	f := url.Values{}
	f.Set("items[0][id]", itemID)
	f.Set("items[0][quantity]", strconv.Itoa(units))
	f.Set("proration_behavior", "none")
	_, err := c.call(ctx, http.MethodPost, "/v1/subscriptions/"+url.PathEscape(subscriptionID), f, idempotencyKey)
	return err
}

// CancelSubscription ends a subscription now, for a deleted account.
func (c *Client) CancelSubscription(ctx context.Context, subscriptionID string) error {
	_, err := c.call(ctx, http.MethodDelete, "/v1/subscriptions/"+url.PathEscape(subscriptionID), nil, "")
	return err
}

// call sends one request to Stripe and returns the body of a 200 answer.
func (c *Client) call(ctx context.Context, method, path string, form url.Values, idempotencyKey string) ([]byte, error) {
	base := c.BaseURL
	if base == "" {
		base = "https://api.stripe.com"
	}
	var payload io.Reader
	if form != nil {
		payload = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+path, payload)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.SecretKey, "")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStripeUnreachable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStripeUnreachable, err)
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
	return body, nil
}
