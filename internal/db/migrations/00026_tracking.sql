-- +goose Up
-- Open and click tracking (MG-37, REP-3): on by default, off per campaign.
ALTER TABLE campaigns ADD COLUMN tracking boolean NOT NULL DEFAULT true;

-- Opens and clicks from mailgoose's own pixel and redirects, one row per
-- hit. A click with no open before it adds an open too. Reports (MG-38)
-- count them; nothing here holds an address, IP or user agent.
CREATE TABLE engagement_events (
    id           bigserial   PRIMARY KEY,
    account_id   uuid        NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    campaign_id  uuid        NOT NULL REFERENCES campaigns (id) ON DELETE CASCADE,
    recipient_id bigint      NOT NULL REFERENCES campaign_recipients (id) ON DELETE CASCADE,
    type         text        NOT NULL CHECK (type IN ('open', 'click')),
    -- link is the clicked link's position in the email, from 1, and url
    -- where it went; both null for an open.
    link         integer     CHECK ((type = 'click') = (link IS NOT NULL)),
    url          text,
    occurred_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX engagement_events_campaign ON engagement_events (campaign_id, type);
CREATE INDEX engagement_events_recipient ON engagement_events (recipient_id, type);
CREATE INDEX engagement_events_account ON engagement_events (account_id);

-- +goose Down
DROP TABLE engagement_events;
ALTER TABLE campaigns DROP COLUMN tracking;
