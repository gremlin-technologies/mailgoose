-- +goose Up
-- When an account first bought credits and became paid (BIL-5); the
-- monthly fee (MG-40) is due on this date's anniversaries.
ALTER TABLE accounts ADD COLUMN paid_since timestamptz;

-- Stripe webhook events already handled, so a redelivered event changes
-- nothing. Not account data: it holds only Stripe's ids.
CREATE TABLE stripe_events (
    id          text        PRIMARY KEY,
    type        text        NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now()
);

-- A Checkout session adds its credits once, even if Stripe sends both
-- checkout.session.completed and async_payment_succeeded for it at once.
CREATE UNIQUE INDEX credit_transactions_purchase_ref ON credit_transactions (ref) WHERE type = 'purchase' AND ref <> '';

-- +goose Down
DROP INDEX credit_transactions_purchase_ref;
DROP TABLE stripe_events;
ALTER TABLE accounts DROP COLUMN paid_since;
