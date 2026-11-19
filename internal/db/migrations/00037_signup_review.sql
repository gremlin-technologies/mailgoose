-- +goose Up

-- Risky sign-up review (MG-66). A new account may wait for a person to
-- approve it before it can launch campaigns or take form signups: every new
-- account (SIGNUP_REVIEW=all), only flagged ones (flagged), or none (off).
-- review is null for accounts that never needed one, including every account
-- made before this migration.
ALTER TABLE accounts
    ADD COLUMN review       text,
    -- Why it was flagged at sign-up: disposable_domain, ip_burst,
    -- suspended_ip, suspended_domain. Empty when nothing was.
    ADD COLUMN review_flags text[] NOT NULL DEFAULT '{}',
    ADD COLUMN review_at    timestamptz,
    ADD CONSTRAINT accounts_review_check CHECK (review IN ('waiting', 'approved', 'rejected')),
    ADD CONSTRAINT accounts_review_at_check CHECK ((review IS NULL) = (review_at IS NULL));

CREATE INDEX accounts_review_waiting ON accounts (review_at) WHERE review = 'waiting';

-- The IP each account signed up from, kept only for the review's checks and
-- deleted after 90 days.
CREATE TABLE signup_ips (
    account_id uuid        PRIMARY KEY REFERENCES accounts (id) ON DELETE CASCADE,
    ip         text        NOT NULL,
    created_at timestamptz NOT NULL
);

CREATE INDEX signup_ips_ip ON signup_ips (ip, created_at);
CREATE INDEX signup_ips_created ON signup_ips (created_at);

ALTER TABLE admin_actions DROP CONSTRAINT admin_actions_action_check;
ALTER TABLE admin_actions ADD CONSTRAINT admin_actions_action_check CHECK (action IN (
    'grant_admin', 'revoke_admin', 'pause', 'unpause', 'auto_pause',
    'stop_campaign', 'approve_import', 'grant_credits', 'throttle',
    'suspend', 'unsuspend', 'auto_suspend',
    'approve_signup', 'reject_signup'));

-- +goose Down
DELETE FROM admin_actions WHERE action IN ('approve_signup', 'reject_signup');
ALTER TABLE admin_actions DROP CONSTRAINT admin_actions_action_check;
ALTER TABLE admin_actions ADD CONSTRAINT admin_actions_action_check CHECK (action IN (
    'grant_admin', 'revoke_admin', 'pause', 'unpause', 'auto_pause',
    'stop_campaign', 'approve_import', 'grant_credits', 'throttle',
    'suspend', 'unsuspend', 'auto_suspend'));
DROP TABLE signup_ips;
DROP INDEX accounts_review_waiting;
ALTER TABLE accounts
    DROP CONSTRAINT accounts_review_at_check,
    DROP CONSTRAINT accounts_review_check,
    DROP COLUMN review_at,
    DROP COLUMN review_flags,
    DROP COLUMN review;
