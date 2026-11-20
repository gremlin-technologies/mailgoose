-- +goose Up

-- Test mode (MG-80). An account in test mode sends only to its confirmed
-- test addresses and its owner's own sign-in address: launches, test sends
-- and form signups for anyone else are refused, and a copy for anyone else
-- that reaches the send pipeline is skipped. New accounts start in the mode
-- NEW_ACCOUNTS_TEST_MODE says (test by default); every account made before
-- this migration keeps full sending.
ALTER TABLE accounts
    ADD COLUMN sending_mode text NOT NULL DEFAULT 'full',
    ADD CONSTRAINT accounts_sending_mode_check CHECK (sending_mode IN ('test', 'full'));

CREATE INDEX accounts_test_mode ON accounts (created_at) WHERE sending_mode = 'test';

-- An account's test addresses, at most 5 (pending ones included). One
-- counts once its owner clicks the link emailed to it (confirmed_at). Only
-- the link's hash is kept; the link works for 7 days after it's sent.
CREATE TABLE test_addresses (
    id                 uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id         uuid        NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    email              text        NOT NULL,
    confirm_token_hash bytea,
    confirm_expires_at timestamptz,
    confirm_sent_at    timestamptz,
    confirmed_at       timestamptz,
    created_at         timestamptz NOT NULL
);

CREATE UNIQUE INDEX test_addresses_account_email ON test_addresses (account_id, lower(email));
CREATE UNIQUE INDEX test_addresses_token ON test_addresses (confirm_token_hash) WHERE confirm_token_hash IS NOT NULL;

ALTER TABLE admin_actions DROP CONSTRAINT admin_actions_action_check;
ALTER TABLE admin_actions ADD CONSTRAINT admin_actions_action_check CHECK (action IN (
    'grant_admin', 'revoke_admin', 'pause', 'unpause', 'auto_pause',
    'stop_campaign', 'approve_import', 'grant_credits', 'throttle',
    'suspend', 'unsuspend', 'auto_suspend',
    'approve_signup', 'reject_signup',
    'allow_full_sending', 'set_test_mode'));

-- +goose Down
DELETE FROM admin_actions WHERE action IN ('allow_full_sending', 'set_test_mode');
ALTER TABLE admin_actions DROP CONSTRAINT admin_actions_action_check;
ALTER TABLE admin_actions ADD CONSTRAINT admin_actions_action_check CHECK (action IN (
    'grant_admin', 'revoke_admin', 'pause', 'unpause', 'auto_pause',
    'stop_campaign', 'approve_import', 'grant_credits', 'throttle',
    'suspend', 'unsuspend', 'auto_suspend',
    'approve_signup', 'reject_signup'));
DROP TABLE test_addresses;
DROP INDEX accounts_test_mode;
ALTER TABLE accounts
    DROP CONSTRAINT accounts_sending_mode_check,
    DROP COLUMN sending_mode;
