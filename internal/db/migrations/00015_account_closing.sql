-- +goose Up

-- Deleting an account (AUTH-6) removes everything in it except the billing
-- records the law requires: the account row stays, closed and with its name
-- and settings cleared, so the credit ledger still has an owner.
ALTER TABLE accounts
    DROP CONSTRAINT accounts_status_check,
    ADD CONSTRAINT accounts_status_check CHECK (status IN ('pending', 'active', 'closed')),
    ADD COLUMN closed_at timestamptz;

-- +goose Down
DELETE FROM accounts WHERE status = 'closed';
ALTER TABLE accounts
    DROP COLUMN closed_at,
    DROP CONSTRAINT accounts_status_check,
    ADD CONSTRAINT accounts_status_check CHECK (status IN ('pending', 'active'));
