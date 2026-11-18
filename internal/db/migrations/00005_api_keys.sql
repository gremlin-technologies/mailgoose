-- +goose Up

-- API keys and CLI tokens (AUTH-4, API-2). Both are bearer secrets; only a
-- SHA-256 of each is kept, which is enough for a random 160-bit secret. API
-- keys belong to the account and are managed in the web app and CLI; CLI
-- tokens come from `mailgoose login` (MG-10) and belong to the person who
-- signed in. A draft-scope secret can do everything except launch campaigns,
-- change billing and manage keys.
CREATE TABLE api_keys (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id   uuid        NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    created_by   uuid        REFERENCES users (id) ON DELETE SET NULL,
    kind         text        NOT NULL,
    name         text        NOT NULL,
    scope        text        NOT NULL,
    secret_hash  bytea       NOT NULL UNIQUE,
    secret_hint  text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    revoked_at   timestamptz,
    CONSTRAINT api_keys_kind_check CHECK (kind IN ('api_key', 'cli_token')),
    CONSTRAINT api_keys_scope_check CHECK (scope IN ('full', 'draft'))
);

CREATE INDEX api_keys_account_created ON api_keys (account_id, created_at, id)
    WHERE revoked_at IS NULL;

-- +goose Down
DROP TABLE api_keys;
