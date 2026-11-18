-- +goose Up

-- CLI device login (AUTH-3). `mailgoose login` starts a request and polls it
-- with the long device code, of which only a SHA-256 is kept; the person
-- approves the short user code in a signed-in browser. A request lasts 10
-- minutes and gives out one CLI token.
CREATE TABLE device_logins (
    id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    device_code_hash bytea       NOT NULL UNIQUE,
    user_code        text        NOT NULL,
    client_name      text        NOT NULL DEFAULT '',
    status           text        NOT NULL DEFAULT 'pending',
    account_id       uuid        REFERENCES accounts (id) ON DELETE CASCADE,
    user_id          uuid        REFERENCES users (id) ON DELETE CASCADE,
    key_id           uuid        REFERENCES api_keys (id) ON DELETE SET NULL,
    created_at       timestamptz NOT NULL,
    expires_at       timestamptz NOT NULL,
    decided_at       timestamptz,
    last_polled_at   timestamptz,
    CONSTRAINT device_logins_status_check CHECK (status IN ('pending', 'approved', 'denied', 'done'))
);

-- A user code names one live request.
CREATE UNIQUE INDEX device_logins_pending_user_code ON device_logins (user_code) WHERE status = 'pending';
CREATE INDEX device_logins_expires_at ON device_logins (expires_at);

-- +goose Down
DROP TABLE device_logins;
