-- +goose Up

-- Fetches from a connected Mailchimp account (MG-74). A fetch reads the
-- audiences the customer picked through Mailchimp's API, in the API's job,
-- into a bundle like an uploaded export's (mailchimp_bundles.fetched), which
-- is then previewed and confirmed the same way. An account runs one fetch at
-- a time; finished ones are deleted after 7 days.
CREATE TABLE mailchimp_fetches (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id uuid NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    audience_ids text[] NOT NULL,
    segment_ids text[] NOT NULL DEFAULT '{}',
    campaigns integer NOT NULL DEFAULT 10 CHECK (campaigns BETWEEN 0 AND 100),
    status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'done', 'failed')),
    -- How far it has got: the step, the audience being read and the
    -- members read of those the audiences had when it started.
    step text NOT NULL DEFAULT '',
    audience text,
    members_read integer NOT NULL DEFAULT 0,
    members_total integer NOT NULL DEFAULT 0,
    error_code text,
    error_message text,
    bundle_id uuid REFERENCES mailchimp_bundles (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    finished_at timestamptz,
    lease_token uuid,
    lease_until timestamptz,
    attempts integer NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX mailchimp_fetches_one_running ON mailchimp_fetches (account_id) WHERE status IN ('queued', 'running');
CREATE INDEX mailchimp_fetches_pending ON mailchimp_fetches (created_at) WHERE status IN ('queued', 'running');
CREATE INDEX mailchimp_fetches_account ON mailchimp_fetches (account_id, created_at DESC);

-- A bundle read from a connected account rather than uploaded. Confirming
-- one forgets the connection, since the import needs nothing more from
-- Mailchimp.
ALTER TABLE mailchimp_bundles ADD COLUMN fetched boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE mailchimp_bundles DROP COLUMN fetched;
DROP TABLE mailchimp_fetches;
