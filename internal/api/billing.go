package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/gremlin-technologies/mailgoose/internal/api/apigen"
	"github.com/gremlin-technologies/mailgoose/internal/billing"
)

// StripeWebhookPath is where Stripe sends its webhook. It's outside the
// spec and the API's sign-in: Stripe's signature is the only authority.
const StripeWebhookPath = "/hooks/stripe"

// maxWebhookBody is the largest webhook read; Checkout events are a few KB.
const maxWebhookBody = 256 << 10

// CreateCreditCheckout is POST /v1/credits/checkout (BIL-3, BIL-6).
func (s *Server) CreateCreditCheckout(w http.ResponseWriter, r *http.Request) {
	c := s.requireFull(w, r)
	if c == nil {
		return
	}
	var req apigen.CreditCheckoutRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, err)
		return
	}
	if s.Billing == nil {
		WriteError(w, &Error{Status: http.StatusServiceUnavailable, Code: "service_unavailable",
			Message: "Buying credits needs a database, and none is configured."})
		return
	}
	// A retry with the same Idempotency-Key gets the same Checkout page
	// from Stripe too.
	key := ""
	if k := r.Header.Get("Idempotency-Key"); k != "" {
		key = "mailgoose-checkout:" + c.AccountID + ":" + k
	}
	sess, pack, err := s.Billing.Checkout(r.Context(), c.AccountID, req.Pack, key)
	var unknown *billing.UnknownPackError
	var stripeErr *billing.StripeError
	switch {
	case errors.As(err, &unknown):
		packs := make([]map[string]any, len(unknown.Packs))
		for i, p := range unknown.Packs {
			packs[i] = map[string]any{"name": p.Name, "credits": p.Credits, "price_cents": p.PriceCents}
		}
		WriteError(w, &Error{Status: http.StatusUnprocessableEntity, Code: "unknown_pack",
			Message: fmt.Sprintf("There's no %q pack.", req.Pack), Hint: "The packs are listed in details.packs.",
			Details: map[string]any{"packs": packs}})
		return
	case errors.Is(err, billing.ErrNotConfigured):
		WriteError(w, &Error{Status: http.StatusServiceUnavailable, Code: "service_unavailable",
			Message: "Buying credits isn't set up on this server yet."})
		return
	case errors.As(err, &stripeErr) && stripeErr.Status < 500 && stripeErr.Status != http.StatusTooManyRequests:
		// Our request was refused: a setup problem, not the person's.
		s.Log.ErrorContext(r.Context(), "stripe refused a checkout session", "err", err)
		WriteError(w, &Error{Status: http.StatusBadGateway, Code: "payment_provider_error",
			Message: "Stripe couldn't make the checkout page.", Hint: "Try again shortly; if it keeps failing, contact support."})
		return
	case err != nil:
		s.Log.WarnContext(r.Context(), "checkout session failed", "err", err)
		WriteError(w, &Error{Status: http.StatusBadGateway, Code: "payment_provider_error",
			Message: "Stripe couldn't make the checkout page.", Hint: "Try again shortly."})
		return
	}
	writeJSON(w, http.StatusCreated, apigen.CreditCheckout{
		Url: sess.URL, Pack: pack.Name, Credits: int(pack.Credits), PriceCents: int(pack.PriceCents),
		Currency: apigen.CreditCheckoutCurrencyUsd, ExpiresAt: sess.ExpiresAt,
	})
}

// stripeWebhook takes Stripe's webhook (BIL-3). A bad signature is a 400;
// a database failure a 503, so Stripe sends it again later.
func (s *Server) stripeWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		WriteError(w, &Error{Status: http.StatusMethodNotAllowed, Code: "method_not_allowed", Message: "Use POST."})
		return
	}
	if s.Billing == nil {
		WriteError(w, &Error{Status: http.StatusServiceUnavailable, Code: "service_unavailable", Message: "Billing isn't set up."})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
	if err != nil {
		WriteError(w, BadRequest("invalid_body", "The body is too large or cut short."))
		return
	}
	out, err := s.Billing.HandleWebhook(r.Context(), body, r.Header.Get("Stripe-Signature"))
	switch {
	case errors.Is(err, billing.ErrBadSignature):
		WriteError(w, BadRequest("invalid_signature", "The Stripe-Signature header doesn't check out."))
		return
	case err != nil:
		s.Log.ErrorContext(r.Context(), "stripe webhook failed", "err", err)
		WriteError(w, &Error{Status: http.StatusServiceUnavailable, Code: "service_unavailable", Message: "Try again later."})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"outcome": string(out)})
}
