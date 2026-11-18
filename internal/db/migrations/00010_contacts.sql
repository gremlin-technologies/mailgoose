-- +goose Up

-- Contacts (CON-1 to CON-3, CON-8): one audience per account. Email is
-- stored lower case and is unique per account. Only subscribed contacts
-- receive campaigns.
CREATE TABLE contacts (
    id                uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id        uuid        NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    email             text        NOT NULL CHECK (email = lower(email)),
    first_name        text,
    last_name         text,
    status            text        NOT NULL
        CHECK (status IN ('unconfirmed', 'subscribed', 'unsubscribed', 'bounced', 'complained')),
    source            text        NOT NULL CHECK (source IN ('form', 'import', 'api', 'manual')),
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    status_changed_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (account_id, email)
);

-- Lists are newest first, keyset on (created_at, id).
CREATE INDEX contacts_account_created ON contacts (account_id, created_at DESC, id DESC);
CREATE INDEX contacts_account_status ON contacts (account_id, status);

-- What's left of a deleted contact that unsubscribed, bounced or complained:
-- a hash of the address, so re-adding it keeps that status and it isn't
-- mailed again by mistake. The address itself is gone.
CREATE TABLE suppressions (
    account_id uuid        NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    email_hash text        NOT NULL,
    reason     text        NOT NULL CHECK (reason IN ('unsubscribed', 'bounced', 'complained')),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, email_hash)
);

-- +goose Down
DROP TABLE suppressions;
DROP TABLE contacts;
