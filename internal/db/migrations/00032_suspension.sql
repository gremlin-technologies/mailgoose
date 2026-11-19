-- +goose Up

-- Account suspension (MG-65): the step past a pause. A suspended account's
-- API keys, CLI logins and web sessions are revoked, its scheduled and
-- in-flight campaigns are stopped and its forms take no signups. Its owner
-- can still sign in to a read-only page that explains why and offers the
-- data export for 30 days. Credits are left as they are.
ALTER TABLE accounts
    ADD COLUMN suspended_at     timestamptz,
    ADD COLUMN suspended_reason text,
    -- Who suspended it: an admin's address, or "automatic".
    ADD COLUMN suspended_by     text,
    ADD CONSTRAINT accounts_suspension_check CHECK (
        (suspended_at IS NULL) = (suspended_reason IS NULL)
        AND (suspended_at IS NULL) = (suspended_by IS NULL));

CREATE INDEX accounts_suspended ON accounts (suspended_at) WHERE suspended_at IS NOT NULL;

ALTER TABLE admin_actions DROP CONSTRAINT admin_actions_action_check;
ALTER TABLE admin_actions ADD CONSTRAINT admin_actions_action_check CHECK (action IN (
    'grant_admin', 'revoke_admin', 'pause', 'unpause', 'auto_pause',
    'stop_campaign', 'approve_import', 'grant_credits', 'throttle',
    'suspend', 'unsuspend', 'auto_suspend'));

-- +goose Down
DELETE FROM admin_actions WHERE action IN ('suspend', 'unsuspend', 'auto_suspend');
ALTER TABLE admin_actions DROP CONSTRAINT admin_actions_action_check;
ALTER TABLE admin_actions ADD CONSTRAINT admin_actions_action_check CHECK (action IN (
    'grant_admin', 'revoke_admin', 'pause', 'unpause', 'auto_pause',
    'stop_campaign', 'approve_import', 'grant_credits', 'throttle'));
DROP INDEX accounts_suspended;
ALTER TABLE accounts
    DROP CONSTRAINT accounts_suspension_check,
    DROP COLUMN suspended_by,
    DROP COLUMN suspended_reason,
    DROP COLUMN suspended_at;
