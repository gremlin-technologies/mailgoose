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

	"github.com/google/uuid"
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
	// OutcomeNoAccount is a paid session for an account that doesn't exist
	// or is closed: nothing is added, and it's logged for a refund.
	OutcomeNoAccount Outcome = "no_account"
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
	pack, ok := s.sessionPack(cs)
	if !ok || accountID == "" || accountID != cs.ClientReferenceID ||
		cs.AmountSubtotal != pack.PriceCents || !strings.EqualFold(cs.Currency, "usd") {
		s.log().ErrorContext(ctx, "a paid Stripe session doesn't match a credit pack; no credits added",
			"session_id", cs.ID, "event_id", e.ID, "pack", cs.Metadata["pack"], "amount_subtotal", cs.AmountSubtotal)
		return OutcomeMismatch, nil
	}
	// The account must still be there and open. Answering 200 stops Stripe
	// retrying something that can't succeed; the log says to refund it.
	open := false
	if id, err := uuid.Parse(accountID); err == nil && id.String() == accountID {
		var status string
		err := tx.QueryRow(ctx, `SELECT status FROM accounts WHERE id = $1 FOR UPDATE`, accountID).Scan(&status)
		switch {
		case err == nil:
			open = status != "closed"
		case !errors.Is(err, pgx.ErrNoRows):
			return "", err
		}
	}
	if !open {
		s.log().ErrorContext(ctx, "a paid Stripe session is for an account that doesn't exist or is closed; no credits added, refund it",
			"session_id", cs.ID, "event_id", e.ID, "account_id", accountID, "amount_subtotal", cs.AmountSubtotal)
		return OutcomeNoAccount, nil
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

// sessionPack is the pack a session was opened for. A session carries its
// pack's price and credits in its metadata, set by mailgoose when it was
// opened; those are what the customer agreed to, so a price change in
// CREDIT_PACKS while a checkout is open doesn't strand the payment. Only
// mailgoose's own key can set metadata, and the webhook is signed. A
// session opened before prices were stamped is checked against the pack as
// configured now.
func (s *Service) sessionPack(cs checkoutSession) (Pack, bool) {
	name := cs.Metadata["pack"]
	credits, err := strconv.ParseInt(cs.Metadata["credits"], 10, 64)
	if err != nil || credits <= 0 || name == "" {
		return Pack{}, false
	}
	if stamped := cs.Metadata["price_cents"]; stamped != "" {
		price, err := strconv.ParseInt(stamped, 10, 64)
		if err != nil || price <= 0 {
			return Pack{}, false
		}
		return Pack{Name: name, Credits: credits, PriceCents: price}, true
	}
	pack, err := s.Pack(name)
	if err != nil || pack.Credits != credits {
		return Pack{}, false
	}
	return pack, true
}
