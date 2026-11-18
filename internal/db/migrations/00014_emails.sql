-- +goose Up

-- Emails (DES-1, DES-9): a block document or HTML, with its settings, kept
-- as the JSON the API shows. version counts saves, for optimistic
-- concurrency: an update names the version it read and fails if it moved.
CREATE TABLE emails (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id uuid        NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    name       text        NOT NULL,
    kind       text        NOT NULL CHECK (kind IN ('blocks', 'html')),
    version    integer     NOT NULL DEFAULT 1,
    document   jsonb       NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX emails_account_created ON emails (account_id, created_at DESC, id DESC);

-- +goose Down
DROP TABLE emails;
