-- +goose Up

-- The send pipeline (CAM-7, CAM-8, CMP-7).

-- Each recipient row is its send record. The dispatcher marks rows
-- enqueued when it puts them in a batch on the send queue; a worker claims
-- a row (sending) before it sends and marks it after (sent), so a batch
-- that's delivered again never sends a row twice. A claim that never
-- finished is left alone, not sent again, and ends failed.
ALTER TABLE campaign_recipients DROP CONSTRAINT campaign_recipients_status_check;
ALTER TABLE campaign_recipients
    ADD CONSTRAINT campaign_recipients_status_check
        CHECK (status IN ('queued', 'sending', 'sent', 'skipped', 'failed')),
    ADD COLUMN enqueued_at timestamptz,
    ADD COLUMN claimed_at  timestamptz,
    ADD COLUMN sent_at     timestamptz,
    ADD COLUMN message_id  text,
    ADD COLUMN error       text;

-- An account's sends in the last day, and what's on its way, for its
-- daily limit.
CREATE INDEX campaign_recipients_sent ON campaign_recipients (account_id, sent_at) WHERE sent_at IS NOT NULL;
CREATE INDEX campaign_recipients_in_flight ON campaign_recipients (account_id)
    WHERE status IN ('queued', 'sending') AND enqueued_at IS NOT NULL;

-- An account's own daily send limit, when it has one (CMP-7: account
-- health, MG-34, raises or lowers it). Null means the configured default.
ALTER TABLE accounts ADD COLUMN daily_send_limit integer CHECK (daily_send_limit >= 0);

-- The global send rate (CAM-7): one row holding the next free send slot.
-- Every worker takes its slot here before each send, so all of them
-- together keep to SEND_RATE_PER_SECOND, using the database's clock.
CREATE TABLE send_pace (
    id      integer     PRIMARY KEY CHECK (id = 1),
    next_at timestamptz NOT NULL
);
INSERT INTO send_pace (id, next_at) VALUES (1, '-infinity');

-- +goose Down
DROP TABLE send_pace;
ALTER TABLE accounts DROP COLUMN daily_send_limit;
DROP INDEX campaign_recipients_in_flight;
DROP INDEX campaign_recipients_sent;
UPDATE campaign_recipients SET status = 'queued' WHERE status = 'sending';
ALTER TABLE campaign_recipients
    DROP COLUMN enqueued_at,
    DROP COLUMN claimed_at,
    DROP COLUMN sent_at,
    DROP COLUMN message_id,
    DROP COLUMN error,
    DROP CONSTRAINT campaign_recipients_status_check,
    ADD CONSTRAINT campaign_recipients_status_check
        CHECK (status IN ('queued', 'sent', 'skipped', 'failed'));
