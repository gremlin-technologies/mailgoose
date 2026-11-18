-- +goose Up
-- A campaign's report, kept once its raw events are deleted (REP-5): opens,
-- clicks, deliveries and bounces are kept 12 months after launch, then
-- summed here and deleted.
CREATE TABLE campaign_reports (
    campaign_id uuid        PRIMARY KEY REFERENCES campaigns (id) ON DELETE CASCADE,
    account_id  uuid        NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    report      jsonb       NOT NULL,
    frozen_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX campaign_reports_account ON campaign_reports (account_id);
CREATE INDEX engagement_events_occurred ON engagement_events (occurred_at);
CREATE INDEX send_events_occurred ON send_events (occurred_at);

-- +goose Down
DROP INDEX send_events_occurred;
DROP INDEX engagement_events_occurred;
DROP TABLE campaign_reports;
