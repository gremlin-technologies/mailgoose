-- +goose Up

-- Launching a campaign (CAM-3 to CAM-5, CAM-8).

-- An account whose sending is paused (MG-34 sets it: account health, or by
-- hand) can't launch.
ALTER TABLE accounts
    ADD COLUMN sending_paused_at     timestamptz,
    ADD COLUMN sending_paused_reason text;

-- A launched campaign keeps a copy of what it sends (launch: the compiled
-- email, From and Reply-To), how many it was launched to, and the credits
-- reserved for them.
ALTER TABLE campaigns
    ADD COLUMN launched_at      timestamptz,
    ADD COLUMN launch           jsonb,
    ADD COLUMN recipient_total  integer,
    ADD COLUMN credits_reserved integer;

-- The confirmation code a launch summary hands out (CAM-4): one per
-- campaign, stored hashed. It's for the campaign and email versions it was
-- issued for, lasts 15 minutes and allows 5 wrong tries.
CREATE TABLE campaign_launch_codes (
    campaign_id      uuid        PRIMARY KEY REFERENCES campaigns (id) ON DELETE CASCADE,
    account_id       uuid        NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    code_hash        bytea       NOT NULL,
    campaign_version integer     NOT NULL,
    email_version    integer     NOT NULL,
    expires_at       timestamptz NOT NULL,
    attempts         integer     NOT NULL DEFAULT 0,
    created_at       timestamptz NOT NULL DEFAULT now()
);

-- Who a launched campaign goes to, fixed at launch, one row each. The send
-- workers (MG-31) move each row from queued exactly once. A contact deleted
-- before its copy goes out leaves a row with no contact, which is skipped
-- and its credit given back.
CREATE TABLE campaign_recipients (
    id          bigserial   PRIMARY KEY,
    account_id  uuid        NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    campaign_id uuid        NOT NULL REFERENCES campaigns (id) ON DELETE CASCADE,
    contact_id  uuid        REFERENCES contacts (id) ON DELETE SET NULL,
    status      text        NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'sent', 'skipped', 'failed')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (campaign_id, contact_id)
);
CREATE INDEX campaign_recipients_queued ON campaign_recipients (campaign_id, id) WHERE status = 'queued';
CREATE INDEX campaign_recipients_contact ON campaign_recipients (contact_id);

-- The send queue's outbox: launch writes a row in its own transaction, and
-- the send pipeline (MG-31) picks it up at run_at (the send time, or the
-- launch for "now"). One per campaign.
CREATE TABLE send_queue (
    id          bigserial   PRIMARY KEY,
    account_id  uuid        NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    campaign_id uuid        NOT NULL UNIQUE REFERENCES campaigns (id) ON DELETE CASCADE,
    run_at      timestamptz NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    claimed_at  timestamptz,
    done_at     timestamptz
);
CREATE INDEX send_queue_due ON send_queue (run_at) WHERE done_at IS NULL;

-- +goose Down
DROP TABLE send_queue;
DROP TABLE campaign_recipients;
DROP TABLE campaign_launch_codes;
ALTER TABLE campaigns
    DROP COLUMN launched_at,
    DROP COLUMN launch,
    DROP COLUMN recipient_total,
    DROP COLUMN credits_reserved;
ALTER TABLE accounts
    DROP COLUMN sending_paused_at,
    DROP COLUMN sending_paused_reason;
