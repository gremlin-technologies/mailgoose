-- +goose Up

-- Uploaded Mailchimp exports (MG-72). An upload is kept as it came, like a
-- CSV import's file, so the preview can be shown again and the import read
-- from it on confirm. The file is removed once the import has used it, and an
-- unconfirmed upload is deleted after 7 days.
CREATE TABLE mailchimp_bundles (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id uuid NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    file_name text,
    bytes integer NOT NULL,
    data bytea,
    -- How the file is read: the audience name for an export that doesn't say,
    -- and the columns that are group categories.
    audience_name text NOT NULL DEFAULT '',
    group_columns text[] NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    -- Set on confirm. import_id is the contacts import (none when the export
    -- has no contacts); the emails are made by the API's own job.
    confirmed_at timestamptz,
    import_id uuid REFERENCES imports (id) ON DELETE SET NULL,
    campaigns integer NOT NULL DEFAULT 10 CHECK (campaigns BETWEEN 1 AND 100),
    emails_status text NOT NULL DEFAULT 'none'
        CHECK (emails_status IN ('none', 'queued', 'running', 'done', 'failed')),
    -- One entry per email made, in order, so a stopped job carries on where
    -- it was.
    emails jsonb NOT NULL DEFAULT '[]',
    emails_total integer NOT NULL DEFAULT 0,
    emails_error text,
    lease_token uuid,
    lease_until timestamptz,
    attempts integer NOT NULL DEFAULT 0
);
CREATE INDEX mailchimp_bundles_account ON mailchimp_bundles (account_id, created_at DESC);
CREATE INDEX mailchimp_bundles_import ON mailchimp_bundles (import_id) WHERE import_id IS NOT NULL;
CREATE INDEX mailchimp_bundles_pending ON mailchimp_bundles (confirmed_at)
    WHERE emails_status IN ('queued', 'running');
CREATE INDEX mailchimp_bundles_expiry ON mailchimp_bundles (expires_at) WHERE confirmed_at IS NULL;

-- +goose Down
DROP TABLE mailchimp_bundles;
