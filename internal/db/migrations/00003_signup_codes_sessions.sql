-- +goose Up

-- Sign up (AUTH-1). An account stays pending until its first emailed code is
-- used. Every account starts on the free plan.
ALTER TABLE accounts
    ADD COLUMN status            text NOT NULL DEFAULT 'pending',
    ADD COLUMN plan              text NOT NULL DEFAULT 'free',
    ADD COLUMN terms_accepted_at timestamptz,
    ADD COLUMN activated_at      timestamptz,
    ADD CONSTRAINT accounts_status_check CHECK (status IN ('pending', 'active')),
    ADD CONSTRAINT accounts_plan_check CHECK (plan IN ('free', 'paid'));

-- Emailed six-digit codes. Only an Argon2id hash of each code is kept. A code
-- works once, expires after 10 minutes, and stops working after 5 wrong tries.
CREATE TABLE auth_codes (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    purpose    text        NOT NULL,
    code_salt  bytea       NOT NULL,
    code_hash  bytea       NOT NULL,
    attempts   integer     NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    used_at    timestamptz,
    CONSTRAINT auth_codes_purpose_check CHECK (purpose IN ('signup', 'signin'))
);

CREATE INDEX auth_codes_user_created ON auth_codes (user_id, created_at DESC);

-- Web sessions. The cookie holds a random token; only its SHA-256 is stored.
CREATE TABLE sessions (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash bytea       NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    user_agent text        NOT NULL DEFAULT '',
    ip         text        NOT NULL DEFAULT ''
);

CREATE INDEX sessions_user ON sessions (user_id);

-- +goose Down
DROP TABLE sessions;
DROP TABLE auth_codes;
ALTER TABLE accounts
    DROP CONSTRAINT accounts_plan_check,
    DROP CONSTRAINT accounts_status_check,
    DROP COLUMN activated_at,
    DROP COLUMN terms_accepted_at,
    DROP COLUMN plan,
    DROP COLUMN status;
