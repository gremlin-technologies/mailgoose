package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/gremlin-technologies/mailgoose/internal/api/apigen"
	"github.com/gremlin-technologies/mailgoose/internal/billing"
	"github.com/gremlin-technologies/mailgoose/internal/contacts"
	"github.com/gremlin-technologies/mailgoose/internal/credits"
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
	case errors.Is(err, billing.ErrPlanRequired):
		WriteError(w, Conflict("plan_required", "Credit packs are for the paid plan.").
			WithHint("Start it with `mailgoose plan upgrade` ($5 a month, or `--yearly` for $50 a year), then buy credits."))
		return
	case s.billingError(w, r, err):
		return
	}
	writeJSON(w, http.StatusCreated, apigen.CreditCheckout{
		Url: sess.URL, Pack: pack.Name, Credits: int(pack.Credits), PriceCents: int(pack.PriceCents),
		Currency: apigen.CreditCheckoutCurrencyUsd, ExpiresAt: sess.ExpiresAt,
	})
}

// billingError writes the response for a billing error that isn't the
// caller's, and reports whether there was one.
func (s *Server) billingError(w http.ResponseWriter, r *http.Request, err error) bool {
	var stripeErr *billing.StripeError
	switch {
	case err == nil:
		return false
	case errors.Is(err, billing.ErrNotConfigured):
		WriteError(w, &Error{Status: http.StatusServiceUnavailable, Code: "service_unavailable",
			Message: "Billing isn't set up on this server yet."})
	case errors.As(err, &stripeErr) && stripeErr.Status < 500 && stripeErr.Status != http.StatusTooManyRequests:
		// Our request was refused: a setup problem, not the person's.
		s.Log.ErrorContext(r.Context(), "stripe refused a request", "err", err)
		WriteError(w, &Error{Status: http.StatusBadGateway, Code: "payment_provider_error",
			Message: "Stripe couldn't make the page.", Hint: "Try again shortly; if it keeps failing, contact support."})
	case errors.As(err, &stripeErr), errors.Is(err, billing.ErrStripeUnreachable):
		s.Log.WarnContext(r.Context(), "stripe request failed", "err", err)
		WriteError(w, &Error{Status: http.StatusBadGateway, Code: "payment_provider_error",
			Message: "Stripe couldn't make the page.", Hint: "Try again shortly."})
	default:
		s.Log.ErrorContext(r.Context(), "billing request failed", "err", err)
		WriteError(w, &Error{Status: http.StatusServiceUnavailable, Code: "service_unavailable",
			Message: "The database didn't answer.", Hint: "Try again in a minute."})
	}
	return true
}

// planCaller is requireFull (or requireCaller, for reads) with billing set up.
func (s *Server) planCaller(w http.ResponseWriter, r *http.Request, full bool) *Caller {
	var c *Caller
	if full {
		c = s.requireFull(w, r)
	} else {
		c = s.requireCaller(w, r)
	}
	if c != nil && s.Billing == nil {
		WriteError(w, &Error{Status: http.StatusServiceUnavailable, Code: "service_unavailable",
			Message: "Billing needs a database, and none is configured."})
		return nil
	}
	return c
}

// GetPlan is GET /v1/plan (BIL-4).
func (s *Server) GetPlan(w http.ResponseWriter, r *http.Request) {
	c := s.planCaller(w, r, false)
	if c == nil {
		return
	}
	p, err := s.Billing.Plan(r.Context(), c.AccountID)
	if s.billingError(w, r, err) {
		return
	}
	out := apigen.Plan{Plan: apigen.PlanPlan(p.Plan), Units: p.Units, CurrentPeriodEnd: p.CurrentPeriodEnd,
		CancelAtPeriodEnd: p.CancelAtPeriodEnd, Contacts: p.Contacts, UnitsNeeded: p.UnitsNeeded}
	if p.Status != "" {
		out.Status = &p.Status
	}
	if p.Interval != "" {
		out.Interval = &p.Interval
	}
	out.Prices.Currency = apigen.PlanPricesCurrencyUsd
	out.Prices.MonthCents, out.Prices.YearCents = int(p.Prices.MonthCents), int(p.Prices.YearCents)

	// The plan's terms, from the server's own settings, so clients word
	// prices and allowances from one place (MG-126).
	out.IncludedContacts, out.ContactsPerUnit = billing.IncludedContacts, billing.ContactsPerUnit
	out.FreePlan.Contacts, out.FreePlan.MonthlyCredits = contacts.FreeContactLimit, credits.FreeMonthlyGrant
	if s.Sending != nil {
		l := s.Sending.Limits
		out.FreePlan.DailySends = l.FreeDaily
		out.PaidSending.StartingDaily, out.PaidSending.MostDaily = min(l.NewDaily, l.Daily), l.Daily
		needs, err := l.NeedsDomain(r.Context(), s.Sending.DB, c.AccountID)
		if s.billingError(w, r, err) {
			return
		}
		out.PaidSending.NeedsDomain = needs
	}
	pack := s.Billing.CheapestPack()
	out.CreditPacks.Cheapest.Name, out.CreditPacks.Cheapest.Credits, out.CreditPacks.Cheapest.PriceCents =
		pack.Name, int(pack.Credits), int(pack.PriceCents)
	out.CreditPacks.Months = credits.LotMonths
	writeJSON(w, http.StatusOK, out)
}

// CreatePlanCheckout is POST /v1/plan/checkout (BIL-4, BIL-5).
func (s *Server) CreatePlanCheckout(w http.ResponseWriter, r *http.Request) {
	c := s.planCaller(w, r, true)
	if c == nil {
		return
	}
	var req apigen.PlanCheckoutRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, err)
		return
	}
	key := ""
	if k := r.Header.Get("Idempotency-Key"); k != "" {
		key = "mailgoose-plan:" + c.AccountID + ":" + k
	}
	sess, offer, err := s.Billing.PlanCheckout(r.Context(), c.AccountID, string(req.Interval), key)
	switch {
	case errors.Is(err, billing.ErrUnknownInterval):
		WriteError(w, BadRequest("invalid_parameter", `interval must be "month" or "year".`))
		return
	case errors.Is(err, billing.ErrAlreadySubscribed):
		WriteError(w, Conflict("already_subscribed", "The account is already on the paid plan.").
			WithHint("`mailgoose plan manage` changes the card or cancels it."))
		return
	case s.billingError(w, r, err):
		return
	}
	writeJSON(w, http.StatusCreated, apigen.PlanCheckout{
		Url: sess.URL, Interval: apigen.PlanCheckoutInterval(offer.Interval), Units: offer.Units,
		UnitPriceCents: int(offer.UnitCents), Currency: apigen.PlanCheckoutCurrencyUsd, ExpiresAt: sess.ExpiresAt,
	})
}

// CreatePlanPortal is POST /v1/plan/portal.
func (s *Server) CreatePlanPortal(w http.ResponseWriter, r *http.Request) {
	c := s.planCaller(w, r, true)
	if c == nil {
		return
	}
	url, err := s.Billing.Portal(r.Context(), c.AccountID)
	switch {
	case errors.Is(err, billing.ErrNotSubscribed):
		WriteError(w, Conflict("not_subscribed", "The account has no subscription to manage.").
			WithHint("`mailgoose plan upgrade` starts the paid plan."))
		return
	case s.billingError(w, r, err):
		return
	}
	writeJSON(w, http.StatusCreated, apigen.PlanPortal{Url: url})
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
