-- +goose Up

-- Which version of the terms each account accepted (MG-68). The versions
-- live in internal/policy. accounts.terms_version is the latest one
-- accepted, next to terms_accepted_at (now the time of that acceptance);
-- terms_acceptances keeps every acceptance, never overwritten.
ALTER TABLE accounts ADD COLUMN terms_version text;

CREATE TABLE terms_acceptances (
    id          bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    account_id  uuid        NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    version     text        NOT NULL,
    accepted_at timestamptz NOT NULL,
    -- How it was accepted: sign-up or the prompt in the web app, the CLI,
    -- the API with a key, or this migration for accounts that signed up
    -- before versions were recorded.
    via         text        NOT NULL CHECK (via IN ('web', 'cli', 'api', 'backfill'))
);

CREATE INDEX terms_acceptances_account ON terms_acceptances (account_id, accepted_at DESC, id DESC);

-- Accounts that signed up before now accepted the version published with the
-- policy pages (MG-57), at the time they signed up.
UPDATE accounts SET terms_version = '2026-11-19' WHERE terms_accepted_at IS NOT NULL;

INSERT INTO terms_acceptances (account_id, version, accepted_at, via)
SELECT id, '2026-11-19', terms_accepted_at, 'backfill'
FROM accounts
WHERE terms_accepted_at IS NOT NULL
ORDER BY terms_accepted_at;

-- +goose Down
DROP TABLE terms_acceptances;
ALTER TABLE accounts DROP COLUMN terms_version;
