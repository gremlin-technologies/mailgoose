-- +goose Up
-- cancelled: a Mailchimp import the customer cancelled while it waited for
-- the paid plan (MG-72). What it already imported stays.
ALTER TABLE imports DROP CONSTRAINT imports_status_check;
ALTER TABLE imports ADD CONSTRAINT imports_status_check
    CHECK (status IN ('queued', 'running', 'waiting', 'done', 'failed', 'cancelled'));

-- +goose Down
UPDATE imports SET status = 'failed', error = coalesce(error, 'Cancelled.') WHERE status = 'cancelled';
ALTER TABLE imports DROP CONSTRAINT imports_status_check;
ALTER TABLE imports ADD CONSTRAINT imports_status_check
    CHECK (status IN ('queued', 'running', 'waiting', 'done', 'failed'));
