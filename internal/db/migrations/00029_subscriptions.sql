-- +goose Up

-- The paid plan (BIL-4, changed Nov 18): a Stripe subscription at $5 a
-- month or $50 a year for each plan unit (the first 2,500 counted contacts,
-- then each further 10,000). Stripe is the source of truth; these columns
-- copy its latest subscription event for the account.
ALTER TABLE accounts
    ADD COLUMN stripe_customer_id     text,
    ADD COLUMN stripe_subscription_id text,
    ADD COLUMN subscription_item_id   text,
    -- Stripe's subscription status: active, past_due, unpaid, canceled and so on.
    ADD COLUMN subscription_status    text,
    ADD COLUMN billing_interval       text CHECK (billing_interval IN ('month', 'year')),
    ADD COLUMN plan_units             integer CHECK (plan_units > 0),
    ADD COLUMN current_period_end     timestamptz,
    ADD COLUMN cancel_at_period_end   boolean NOT NULL DEFAULT false,
    -- When Stripe made the latest subscription event applied here, so an
    -- older event that arrives late changes nothing.
    ADD COLUMN subscription_synced_at timestamptz,
    -- Why sending is paused, as a code a client can act on:
    -- 'subscription_past_due' when a renewal payment failed (lifted
    -- automatically once paid); NULL for other pauses, whose reason is the text.
    ADD COLUMN sending_paused_code    text;

CREATE UNIQUE INDEX accounts_stripe_subscription ON accounts (stripe_subscription_id) WHERE stripe_subscription_id IS NOT NULL;

-- +goose Down
DROP INDEX accounts_stripe_subscription;
ALTER TABLE accounts
    DROP COLUMN stripe_customer_id,
    DROP COLUMN stripe_subscription_id,
    DROP COLUMN subscription_item_id,
    DROP COLUMN subscription_status,
    DROP COLUMN billing_interval,
    DROP COLUMN plan_units,
    DROP COLUMN current_period_end,
    DROP COLUMN cancel_at_period_end,
    DROP COLUMN subscription_synced_at,
    DROP COLUMN sending_paused_code;
