package billing

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrBadSignature means a webhook's Stripe-Signature doesn't check out:
// missing, from another secret, changed, or too old.
var ErrBadSignature = errors.New("the Stripe signature doesn't check out")

// SignatureTolerance is how old a signed webhook may be, against replays.
const SignatureTolerance = 5 * time.Minute

// VerifySignature checks a Stripe-Signature header ("t=...,v1=...") for
// payload: HMAC-SHA256 of "t.payload" with the endpoint's secret, signed
// within SignatureTolerance of now.
func VerifySignature(payload []byte, header, secret string, now time.Time) error {
	if secret == "" {
		return ErrBadSignature
	}
	var ts int64
	var sigs [][]byte
	for _, part := range strings.Split(header, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch k {
		case "t":
			ts, _ = strconv.ParseInt(v, 10, 64)
		case "v1":
			if b, err := hex.DecodeString(v); err == nil {
				sigs = append(sigs, b)
			}
		}
	}
	if ts == 0 || len(sigs) == 0 {
		return ErrBadSignature
	}
	if age := now.Sub(time.Unix(ts, 0)); age > SignatureTolerance || age < -SignatureTolerance {
		return ErrBadSignature
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte("."))
	mac.Write(payload)
	want := mac.Sum(nil)
	for _, sig := range sigs {
		if hmac.Equal(sig, want) {
			return nil
		}
	}
	return ErrBadSignature
}

// Sign makes a Stripe-Signature header for payload, as Stripe does; for
// tests and local runs.
func Sign(payload []byte, secret string, at time.Time) string {
	ts := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(payload)
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

type event struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Data struct {
		Object checkoutSession `json:"object"`
	} `json:"data"`
}

type checkoutSession struct {
	ID                string            `json:"id"`
	Object            string            `json:"object"`
	PaymentStatus     string            `json:"payment_status"`
	ClientReferenceID string            `json:"client_reference_id"`
	AmountSubtotal    int64             `json:"amount_subtotal"`
	Currency          string            `json:"currency"`
	Metadata          map[string]string `json:"metadata"`
}

// Outcome says what a webhook did, for the log.
type Outcome string

// Outcomes.
const (
	OutcomeCredited  Outcome = "credited"
	OutcomeDuplicate Outcome = "duplicate"
	OutcomeIgnored   Outcome = "ignored"
	OutcomeUnpaid    Outcome = "unpaid"
	OutcomeMismatch  Outcome = "mismatch"
)

// HandleWebhook checks and handles one Stripe webhook. A paid Checkout
// session (checkout.session.completed, or async_payment_succeeded for a
// payment that cleared later) adds its pack's credits and makes the account
// paid. Each event is handled once, by its id; other event types are
// recorded and ignored. A session whose amount doesn't match its pack's
// price adds nothing and is logged as an error, for someone to look at.
func (s *Service) HandleWebhook(ctx context.Context, payload []byte, signature string) (Outcome, error) {
	if err := VerifySignature(payload, signature, s.WebhookSecret, s.now()); err != nil {
		return "", err
	}
	var e event
	if err := json.Unmarshal(payload, &e); err != nil || e.ID == "" || e.Type == "" {
		return "", ErrBadSignature
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `INSERT INTO stripe_events (id, type, received_at) VALUES ($1, $2, $3) ON CONFLICT (id) DO NOTHING`,
		e.ID, e.Type, s.now())
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		return OutcomeDuplicate, nil
	}
	out, err := s.handle(ctx, tx, &e)
	if err != nil {
		return "", err
	}
	return out, tx.Commit(ctx)
}

func (s *Service) handle(ctx context.Context, tx pgx.Tx, e *event) (Outcome, error) {
	if e.Type != "checkout.session.completed" && e.Type != "checkout.session.async_payment_succeeded" {
		return OutcomeIgnored, nil
	}
	cs := e.Data.Object
	if cs.PaymentStatus != "paid" {
		return OutcomeUnpaid, nil
	}
	accountID := cs.Metadata["account_id"]
	pack, err := s.Pack(cs.Metadata["pack"])
	if err != nil || accountID == "" || accountID != cs.ClientReferenceID ||
		cs.AmountSubtotal != pack.PriceCents || !strings.EqualFold(cs.Currency, "usd") ||
		cs.Metadata["credits"] != strconv.FormatInt(pack.Credits, 10) {
		s.log().ErrorContext(ctx, "a paid Stripe session doesn't match a credit pack; no credits added",
			"session_id", cs.ID, "event_id", e.ID, "pack", cs.Metadata["pack"], "amount_subtotal", cs.AmountSubtotal)
		return OutcomeMismatch, nil
	}
	added, err := purchase(ctx, tx, accountID, cs.ID, pack.Credits, s.now())
	if err != nil {
		return "", err
	}
	if !added {
		return OutcomeDuplicate, nil
	}
	s.log().InfoContext(ctx, "credits bought", "account_id", accountID, "pack", pack.Name, "session_id", cs.ID)
	return OutcomeCredited, nil
}
