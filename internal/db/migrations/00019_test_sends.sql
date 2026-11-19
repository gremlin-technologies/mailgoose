-- +goose Up

-- Test sends (CAM-2). A campaign keeps its last test: when it went out, and
-- the campaign and email versions it was made from, so launch (MG-30) can
-- refuse a campaign edited since its last test.
ALTER TABLE campaigns
    ADD COLUMN last_test_at               timestamptz,
    ADD COLUMN last_test_version          integer,
    ADD COLUMN last_test_email_version    integer;

-- One row per test message, for the limit of 50 a day per account. Tests
-- use no credits.
CREATE TABLE test_sends (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id  uuid        NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    campaign_id uuid        REFERENCES campaigns (id) ON DELETE SET NULL,
    sent_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX test_sends_account_sent ON test_sends (account_id, sent_at);

-- +goose Down
DROP TABLE test_sends;
ALTER TABLE campaigns
    DROP COLUMN last_test_at,
    DROP COLUMN last_test_version,
    DROP COLUMN last_test_email_version;
