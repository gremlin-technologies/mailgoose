-- +goose Up

-- Custom contact fields (MG-130): text fields an account defines beside
-- email, first name, last name and tags. The key is worked out from the
-- name when the field is made and never changes, so a rename keeps every
-- value. Names are unique ignoring case, keys exactly. Fields list in the
-- order they were made.
CREATE TABLE contact_fields (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id uuid        NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    key        text        NOT NULL CHECK (key ~ '^[a-z][a-z0-9_]*$'),
    name       text        NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE UNIQUE INDEX contact_fields_account_key ON contact_fields (account_id, key);
CREATE UNIQUE INDEX contact_fields_account_name ON contact_fields (account_id, lower(name));
CREATE INDEX contact_fields_account_created ON contact_fields (account_id, created_at, id);

-- A contact's values, keyed by field key; a field with no value has no
-- entry. Deleting a field takes its key off every contact in the same
-- transaction.
ALTER TABLE contacts ADD COLUMN fields jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(fields) = 'object');

-- +goose Down
ALTER TABLE contacts DROP COLUMN fields;
DROP TABLE contact_fields;
