-- +goose Up

-- Amazon SES events (MG-33: CMP-4, CMP-5): deliveries, bounces, complaints,
-- rejects and delivery delays, from SES through SNS to the ses-events
-- queue. Each is kept against its send record when there is one, for
-- reports and account health. Nothing here holds an address: the
-- recipient row says who, and goes with the contact when it's erased.
CREATE TABLE send_events (
    id           bigserial   PRIMARY KEY,
    -- A hash of the event as SES sent it, so a redelivered event is stored
    -- (and acted on) once.
    event_key    text        NOT NULL UNIQUE,
    -- Null for mailgoose's own mail (sign-in codes), which no account sent.
    account_id   uuid        REFERENCES accounts (id) ON DELETE CASCADE,
    campaign_id  uuid        REFERENCES campaigns (id) ON DELETE CASCADE,
    recipient_id bigint      REFERENCES campaign_recipients (id) ON DELETE CASCADE,
    message_id   text        NOT NULL,
    -- The mail's kind tag: campaign, confirm, test or system.
    kind         text        NOT NULL,
    type         text        NOT NULL
        CHECK (type IN ('delivery', 'bounce', 'complaint', 'reject', 'delivery_delay')),
    -- bounce: permanent, transient or undetermined, then SES's subtype
    -- ("permanent/General"); complaint: the feedback type; delay: its type.
    subtype      text,
    occurred_at  timestamptz NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX send_events_campaign ON send_events (campaign_id, type) WHERE campaign_id IS NOT NULL;
CREATE INDEX send_events_recipient ON send_events (recipient_id) WHERE recipient_id IS NOT NULL;
CREATE INDEX send_events_account ON send_events (account_id, occurred_at) WHERE account_id IS NOT NULL;

-- SES events name the message by the id SES gave it.
CREATE INDEX campaign_recipients_message ON campaign_recipients (message_id) WHERE message_id IS NOT NULL;

-- Soft bounces in a row; three suppress the contact (CMP-4). A delivery
-- sets it back to 0.
ALTER TABLE contacts ADD COLUMN soft_bounces integer NOT NULL DEFAULT 0 CHECK (soft_bounces >= 0);

-- +goose Down
ALTER TABLE contacts DROP COLUMN soft_bounces;
DROP INDEX campaign_recipients_message;
DROP TABLE send_events;
