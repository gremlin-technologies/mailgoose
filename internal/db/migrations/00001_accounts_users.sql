-- +goose Up

-- An account is one customer: the unit that owns contacts, emails, campaigns,
-- credits and sending domains.
CREATE TABLE accounts (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- A person who signs in. Sign-in is by emailed one-time code, so there is no
-- password column.
CREATE TABLE users (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id    uuid        NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    email         text        NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    last_login_at timestamptz,
    CONSTRAINT users_email_not_blank CHECK (email <> '')
);

-- One sign-in per email address, whatever its case.
CREATE UNIQUE INDEX users_email_key ON users (lower(email));

-- One person per account for now. Dropping this index is all it takes to allow
-- team members later.
CREATE UNIQUE INDEX users_one_per_account ON users (account_id);

-- +goose Down
DROP TABLE users;
DROP TABLE accounts;
