-- +goose Up

-- CSV imports (CON-5, CON-6). The API stores the uploaded file and the worker
-- imports it in batches, so a big file doesn't hold a request open. The file
-- is deleted once the import ends; the skipped-rows report stays.
CREATE TABLE imports (
    id                   uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id           uuid        NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    file_name            text,
    status               text        NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'running', 'done', 'failed')),
    permission_confirmed boolean     NOT NULL,
    -- The header of the column each field comes from.
    email_column         text        NOT NULL,
    first_name_column    text,
    last_name_column     text,
    tag_ids              uuid[]      NOT NULL DEFAULT '{}',
    csv                  bytea,
    total_rows           integer     NOT NULL,
    processed_rows       integer     NOT NULL DEFAULT 0,
    imported             integer     NOT NULL DEFAULT 0,
    skipped              integer     NOT NULL DEFAULT 0,
    -- Held imports' contacts can't be mailed until an admin approves (MG-34).
    review               text        NOT NULL DEFAULT 'none' CHECK (review IN ('none', 'held', 'approved')),
    hold_reason          text        CHECK (hold_reason IN ('large_first_import', 'over_contact_limit')),
    -- The worker running the import renews its lease after every batch;
    -- another worker takes over an import whose lease ran out.
    lease_token          uuid,
    lease_until          timestamptz,
    attempts             integer     NOT NULL DEFAULT 0,
    error                text,
    created_at           timestamptz NOT NULL DEFAULT now(),
    started_at           timestamptz,
    finished_at          timestamptz
);
CREATE INDEX imports_account_created ON imports (account_id, created_at DESC, id DESC);
CREATE INDEX imports_pending ON imports (created_at) WHERE status IN ('queued', 'running');

-- Every row an import didn't add, and why. email is the cell as written.
CREATE TABLE import_skips (
    import_id  uuid    NOT NULL REFERENCES imports (id) ON DELETE CASCADE,
    row_number integer NOT NULL,
    email      text    NOT NULL,
    reason     text    NOT NULL
        CHECK (reason IN ('invalid_email', 'invalid_row', 'duplicate', 'exists', 'suppressed', 'over_limit')),
    PRIMARY KEY (import_id, row_number)
);

-- Contacts from a held import aren't mailed until the hold is lifted.
ALTER TABLE contacts ADD COLUMN held_import_id uuid REFERENCES imports (id) ON DELETE SET NULL;
CREATE INDEX contacts_held_import ON contacts (held_import_id) WHERE held_import_id IS NOT NULL;

-- +goose Down
ALTER TABLE contacts DROP COLUMN held_import_id;
DROP TABLE import_skips;
DROP TABLE imports;
