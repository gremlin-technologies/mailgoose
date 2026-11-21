-- +goose Up

-- Send throughput (MG-91): one row for each batch that sent at least one
-- copy, with when its send loop began and ended and what it did, so
-- `mailgoose admin deliverability` can show the last day's copies a second
-- over the time batches were actually sending, against
-- SEND_RATE_PER_SECOND. The worker prunes rows older than 7 days as it
-- writes. SES mailbox simulator copies count: they're real SES calls.
CREATE TABLE send_batch_timings (
    id          bigserial   PRIMARY KEY,
    account_id  uuid        NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    campaign_id uuid        NOT NULL REFERENCES campaigns (id) ON DELETE CASCADE,
    started_at  timestamptz NOT NULL,
    finished_at timestamptz NOT NULL,
    batch_size  integer     NOT NULL,
    sent        integer     NOT NULL,
    skipped     integer     NOT NULL,
    failed      integer     NOT NULL,
    CHECK (finished_at >= started_at)
);
CREATE INDEX send_batch_timings_finished_at ON send_batch_timings (finished_at);
CREATE INDEX send_batch_timings_campaign ON send_batch_timings (campaign_id);

-- +goose Down
DROP TABLE send_batch_timings;
