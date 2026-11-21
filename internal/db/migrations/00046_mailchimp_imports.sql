-- +goose Up

-- Mailchimp imports (MG-70). They're imports like a CSV's and run through the
-- same worker, holds and skip report. The stored file is the bundle already
-- merged into one line per address (JSON lines), so the worker never reads a
-- Mailchimp export itself.
ALTER TABLE imports ADD COLUMN kind text NOT NULL DEFAULT 'csv' CHECK (kind IN ('csv', 'mailchimp'));
-- Confirming the same bundle again finds the import already made. A failed
-- import frees its key, so the bundle can be confirmed again.
ALTER TABLE imports ADD COLUMN idempotency_key text;
ALTER TABLE imports ADD COLUMN content_hash bytea;
CREATE UNIQUE INDEX imports_idempotency ON imports (account_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL AND status <> 'failed';
-- What the import found that a CSV import doesn't count: contacts that were
-- already there and only gained tags, those a Mailchimp unsubscribe or clean
-- took off the mailing list, and skipped rows the report doesn't list one by
-- one (rows of a member file that couldn't be read, past the first 1,000).
ALTER TABLE imports ADD COLUMN existing_tagged integer NOT NULL DEFAULT 0;
ALTER TABLE imports ADD COLUMN existing_suppressed integer NOT NULL DEFAULT 0;
ALTER TABLE imports ADD COLUMN unlisted_skipped integer NOT NULL DEFAULT 0;
-- The bundle's summary when it was confirmed: audiences, counts by Mailchimp
-- status, dropped fields, renamed tags and the opt-in signals (MG-77).
ALTER TABLE imports ADD COLUMN mailchimp jsonb;
-- waiting: the subscribed part would take the account past its contact
-- limit, so it waits, with its suppressions already imported, until the plan
-- covers it.
ALTER TABLE imports DROP CONSTRAINT imports_status_check;
ALTER TABLE imports ADD CONSTRAINT imports_status_check
    CHECK (status IN ('queued', 'running', 'waiting', 'done', 'failed'));
DROP INDEX imports_pending;
CREATE INDEX imports_pending ON imports (created_at) WHERE status IN ('queued', 'running', 'waiting');

-- Where a skipped Mailchimp row came from: the export file and its row.
ALTER TABLE import_skips ADD COLUMN source_row text;
ALTER TABLE import_skips DROP CONSTRAINT import_skips_reason_check;
ALTER TABLE import_skips ADD CONSTRAINT import_skips_reason_check CHECK (reason IN (
    'invalid_email', 'invalid_row', 'duplicate', 'exists', 'suppressed', 'over_limit', 'not_subscribed', 'archived'));

-- +goose Down
DELETE FROM imports WHERE kind = 'mailchimp';
ALTER TABLE import_skips DROP CONSTRAINT import_skips_reason_check;
ALTER TABLE import_skips ADD CONSTRAINT import_skips_reason_check CHECK (reason IN (
    'invalid_email', 'invalid_row', 'duplicate', 'exists', 'suppressed', 'over_limit'));
ALTER TABLE import_skips DROP COLUMN source_row;
DROP INDEX imports_pending;
CREATE INDEX imports_pending ON imports (created_at) WHERE status IN ('queued', 'running');
ALTER TABLE imports DROP CONSTRAINT imports_status_check;
ALTER TABLE imports ADD CONSTRAINT imports_status_check CHECK (status IN ('queued', 'running', 'done', 'failed'));
ALTER TABLE imports DROP COLUMN mailchimp;
ALTER TABLE imports DROP COLUMN unlisted_skipped;
ALTER TABLE imports DROP COLUMN existing_suppressed;
ALTER TABLE imports DROP COLUMN existing_tagged;
DROP INDEX imports_idempotency;
ALTER TABLE imports DROP COLUMN content_hash;
ALTER TABLE imports DROP COLUMN idempotency_key;
ALTER TABLE imports DROP COLUMN kind;
