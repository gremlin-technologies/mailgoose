package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// subscription is the part of a Stripe subscription object billing reads.
type subscription struct {
	ID                string            `json:"id"`
	Customer          string            `json:"customer"`
	Status            string            `json:"status"`
	CancelAtPeriodEnd bool              `json:"cancel_at_period_end"`
	CurrentPeriodEnd  int64             `json:"current_period_end"`
	Metadata          map[string]string `json:"metadata"`
	Items             struct {
		Data []struct {
			ID               string `json:"id"`
			Quantity         int    `json:"quantity"`
			CurrentPeriodEnd int64  `json:"current_period_end"`
			Price            struct {
				Currency  string `json:"currency"`
				Recurring *struct {
					Interval string `json:"interval"`
				} `json:"recurring"`
			} `json:"price"`
		} `json:"data"`
	} `json:"items"`
}

// applySubscription copies a subscription event onto its account (BIL-4,
// BIL-5): a live subscription (active, or past due while Stripe retries the
// card) makes the account paid; a past-due one pauses sending until the
// payment goes through; a canceled or expired one makes the account free
// again. The account comes from the subscription's metadata, which the
// plan's checkout sets. An event older than the last one applied changes
// nothing.
func (s *Service) applySubscription(ctx context.Context, tx pgx.Tx, e *event, sub *subscription, c *webhookCall) (Outcome, error) {
	at := time.Unix(e.Created, 0).UTC()
	now := s.now()
	accountID := sub.Metadata["account_id"]
	if accountID == "" {
		err := tx.QueryRow(ctx, `SELECT id FROM accounts WHERE stripe_subscription_id = $1`, sub.ID).Scan(&accountID)
		if errors.Is(err, pgx.ErrNoRows) {
			s.log().ErrorContext(ctx, "a Stripe subscription names no mailgoose account", "subscription_id", sub.ID, "event_id", e.ID)
			return OutcomeMismatch, nil
		}
		if err != nil {
			return "", err
		}
	}
	// Not an account id at all: there's nothing to apply it to, and the
	// lookup below would fail on the cast and have Stripe retry for days.
	if id, err := uuid.Parse(accountID); err != nil || id.String() != accountID {
		s.log().ErrorContext(ctx, "a Stripe subscription names an account that doesn't exist",
			"subscription_id", sub.ID, "event_id", e.ID)
		return OutcomeMismatch, nil
	}
	var status string
	var current, currentStatus *string
	var synced, pausedAt *time.Time
	var pausedCode *string
	err := tx.QueryRow(ctx, `SELECT status, stripe_subscription_id, subscription_status, subscription_synced_at,
			sending_paused_at, sending_paused_code
		FROM accounts WHERE id = $1 FOR UPDATE`, accountID).
		Scan(&status, &current, &currentStatus, &synced, &pausedAt, &pausedCode)
	if errors.Is(err, pgx.ErrNoRows) {
		s.log().ErrorContext(ctx, "a Stripe subscription names an account that doesn't exist",
			"subscription_id", sub.ID, "event_id", e.ID)
		return OutcomeMismatch, nil
	}
	if err != nil {
		return "", err
	}
	// Stripe doesn't promise to deliver events in order, and an event's
	// time is in whole seconds, so two changes in one second can arrive
	// either way round. Applying the subscription as Stripe had it when
	// this event was handled (read before the transaction, see
	// HandleWebhook) makes the order irrelevant; the read's own time orders
	// it against other reads.
	fresh := c.fresh
	if fresh != nil && fresh.ID != sub.ID {
		fresh = nil
	}
	if fresh != nil {
		sub, at = fresh, c.readAt.UTC()
	}
	live := liveStatuses[sub.Status]
	if deref(current) != "" && deref(current) != sub.ID {
		if !live {
			// An old subscription ending changes nothing about the current one.
			return OutcomeStale, nil
		}
		if liveStatuses[deref(currentStatus)] {
			s.log().ErrorContext(ctx, "an account has two live subscriptions; cancel one in Stripe",
				"account_id", accountID, "subscription_id", sub.ID, "current_subscription_id", deref(current))
			c.alert("an account has two paid plan subscriptions",
				fmt.Sprintf("Account %s has two live subscriptions: %s (the one mailgoose uses) and %s.\n\nCancel and refund %s in Stripe.",
					accountID, deref(current), sub.ID, sub.ID))
			return OutcomeMismatch, nil
		}
	} else if synced != nil && (at.Before(*synced) ||
		fresh == nil && at.Equal(*synced) && sub.Status == "incomplete" && liveStatuses[deref(currentStatus)]) {
		// An older event, or an older read of Stripe, changes nothing. Without
		// Stripe's current state, in the same second a new subscription's
		// first, incomplete state doesn't replace the live one its payment
		// led to.
		return OutcomeStale, nil
	}
	if status != "active" {
		// A deleted account's subscription was cancelled when it closed;
		// record what Stripe says, and nothing else.
		if live {
			s.log().ErrorContext(ctx, "a closed account has a live subscription; cancel it in Stripe",
				"account_id", accountID, "subscription_id", sub.ID)
			c.alert("a closed account has a paid plan subscription",
				fmt.Sprintf("Account %s is closed, but its subscription %s is %s in Stripe.\n\nCancel it in Stripe.",
					accountID, sub.ID, sub.Status))
		}
		_, err := tx.Exec(ctx, `UPDATE accounts SET subscription_status = $2, subscription_synced_at = $3 WHERE id = $1`,
			accountID, sub.Status, at)
		return OutcomeSubscription, err
	}

	var itemID, interval *string
	var units *int
	periodEnd := sub.CurrentPeriodEnd
	if len(sub.Items.Data) > 0 {
		it := sub.Items.Data[0]
		itemID, units = &it.ID, &it.Quantity
		if it.Price.Recurring != nil && (it.Price.Recurring.Interval == Monthly || it.Price.Recurring.Interval == Yearly) {
			interval = &it.Price.Recurring.Interval
		}
		if periodEnd == 0 {
			periodEnd = it.CurrentPeriodEnd
		}
		if *units <= 0 {
			units = nil
		}
	}
	var end *time.Time
	if periodEnd > 0 {
		t := time.Unix(periodEnd, 0).UTC()
		end = &t
	}
	plan := "free"
	if live {
		plan = "paid"
	}
	if _, err := tx.Exec(ctx, `UPDATE accounts SET
			plan = $2, paid_since = CASE WHEN $2 = 'paid' THEN coalesce(paid_since, $3) ELSE paid_since END,
			stripe_customer_id = coalesce(nullif($4, ''), stripe_customer_id), stripe_subscription_id = $5,
			subscription_item_id = $6, subscription_status = $7, billing_interval = coalesce($8, billing_interval),
			plan_units = $9, current_period_end = $10, cancel_at_period_end = $11, subscription_synced_at = $12,
			updated_at = $3
		WHERE id = $1`,
		accountID, plan, now, sub.Customer, sub.ID, itemID, sub.Status, interval, units, end, sub.CancelAtPeriodEnd, at); err != nil {
		return "", err
	}

	feePaused := pausedAt != nil && deref(pausedCode) == SubscriptionPastDue
	switch {
	case overdueStatuses[sub.Status] && pausedAt == nil:
		if err := PauseOverdue(ctx, tx, accountID, sub.Status, now); err != nil {
			return "", err
		}
		s.log().WarnContext(ctx, "subscription payment past due; sending paused", "account_id", accountID)
	case !overdueStatuses[sub.Status] && feePaused:
		reason := "The subscription payment went through."
		if !live {
			reason = "The subscription ended; the account is on the free plan."
		}
		if _, err := tx.Exec(ctx, `UPDATE accounts SET sending_paused_at = NULL, sending_paused_reason = NULL, sending_paused_code = NULL,
			updated_at = $2 WHERE id = $1`, accountID, now); err != nil {
			return "", err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO admin_actions (actor, action, account_id, reason, created_at)
			VALUES ('automatic', 'unpause', $1, $2, $3)`, accountID, reason, now); err != nil {
			return "", err
		}
	}
	return OutcomeSubscription, nil
}

// Overdue says whether a subscription status means its payment is past
// due, which pauses sending.
func Overdue(status string) bool { return overdueStatuses[status] }

// PauseOverdue pauses sending while the subscription's payment is past due,
// in tx, and logs it as automatic. Admin unpause calls it too, when the pause
// it lifts was hiding a payment that's still past due.
func PauseOverdue(ctx context.Context, tx pgx.Tx, accountID, status string, now time.Time) error {
	reason := "The paid plan's payment didn't go through. Update the card and sending resumes once it does."
	if _, err := tx.Exec(ctx, `UPDATE accounts SET sending_paused_at = $2, sending_paused_reason = $3, sending_paused_code = $4,
		updated_at = $2 WHERE id = $1`, accountID, now, reason, SubscriptionPastDue); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO admin_actions (actor, action, account_id, reason, details, created_at)
		VALUES ('automatic', 'auto_pause', $1, $2, $3, $4)`,
		accountID, reason, map[string]any{"code": SubscriptionPastDue, "subscription_status": status}, now)
	return err
}

// currentSubscription reads a subscription from Stripe as it is now. It
// returns nil, and the event's copy is used, when there's no Stripe client
// or Stripe doesn't know the subscription. Any other failure is returned,
// so the webhook answers 503 and Stripe sends the event again.
func (s *Service) currentSubscription(ctx context.Context, id string) (*subscription, error) {
	if s.Stripe == nil {
		return nil, nil
	}
	body, err := s.Stripe.GetSubscription(ctx, id)
	var se *StripeError
	if errors.As(err, &se) && se.Status == http.StatusNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read subscription %s from Stripe: %w", id, err)
	}
	var sub subscription
	if err := json.Unmarshal(body, &sub); err != nil || sub.ID != id {
		return nil, fmt.Errorf("%w: subscription %s", ErrStripeUnreachable, id)
	}
	return &sub, nil
}
