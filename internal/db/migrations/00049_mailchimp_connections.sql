-- +goose Up

-- Connecting a Mailchimp account with OAuth (MG-73). One row per account.
-- A connect stores only a hash of its single-use state, for 10 minutes.
-- Mailchimp's tokens don't expire and carry full access, so the token is kept
-- sealed (AES-GCM, with a key from the app's Mailchimp client secret), for at
-- most 24 hours, and deleted once the import is done or on disconnect.
CREATE TABLE mailchimp_connections (
    account_id uuid PRIMARY KEY REFERENCES accounts (id) ON DELETE CASCADE,
    state_hash bytea UNIQUE,
    state_expires_at timestamptz,
    token bytea,
    data_center text,
    account_name text,
    connected_at timestamptz,
    expires_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((state_hash IS NULL) = (state_expires_at IS NULL)),
    CHECK ((token IS NULL) = (expires_at IS NULL)),
    CHECK (token IS NULL OR (data_center IS NOT NULL AND connected_at IS NOT NULL))
);

-- +goose Down
DROP TABLE mailchimp_connections;
