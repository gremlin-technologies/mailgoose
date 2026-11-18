-- +goose Up

-- Form signups (FRM-3 to FRM-6, CMP-3).

-- Each form counts its signups (accepted submissions) and confirmations.
ALTER TABLE forms
    ADD COLUMN signup_count  integer NOT NULL DEFAULT 0,
    ADD COLUMN confirm_count integer NOT NULL DEFAULT 0;

-- A consent record for each signup through a form: when, from which IP,
-- the form and the wording it showed, and when the person confirmed. A
-- signup that needs confirming holds the hash of its confirm link's token
-- until it's used or runs out. Deleting the contact deletes its records;
-- deleting the form keeps them, with the wording.
CREATE TABLE consents (
    id                 uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id         uuid        NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    contact_id         uuid        NOT NULL REFERENCES contacts (id) ON DELETE CASCADE,
    form_id            uuid        REFERENCES forms (id) ON DELETE SET NULL,
    source             text        NOT NULL DEFAULT 'form' CHECK (source IN ('form')),
    ip                 text        NOT NULL,
    wording            jsonb       NOT NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    confirm_token_hash bytea       UNIQUE,
    confirm_expires_at timestamptz,
    confirm_sent_at    timestamptz,
    confirmed_at       timestamptz
);
CREATE INDEX consents_contact ON consents (contact_id, created_at DESC);
CREATE INDEX consents_form ON consents (form_id);

-- +goose Down
DROP TABLE consents;
ALTER TABLE forms
    DROP COLUMN signup_count,
    DROP COLUMN confirm_count;
