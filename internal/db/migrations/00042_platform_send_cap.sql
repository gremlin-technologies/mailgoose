-- +goose Up

-- The platform daily send cap (MG-87): campaign copies sent in the last
-- day across every account, so the dispatcher keeps the total under the
-- SES account's daily quota. campaign_recipients_sent leads with
-- account_id, so it can't answer that without reading every sent row.
-- What's on its way platform-wide is read from campaign_recipients_in_flight,
-- whose rows are few.
CREATE INDEX campaign_recipients_sent_at ON campaign_recipients (sent_at) WHERE sent_at IS NOT NULL;

-- +goose Down
DROP INDEX campaign_recipients_sent_at;
