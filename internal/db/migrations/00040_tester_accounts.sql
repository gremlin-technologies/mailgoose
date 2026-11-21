-- +goose Up

-- Tester accounts (MG-86): people invited to try mailgoose before launch.
-- A tester account sends real campaigns, in full sending, within tight
-- limits: tester_credits sends for its life (from tester_started_at), 100 a
-- day, 500 contacts and a stricter automatic pause. It stops sending at
-- tester_until. All three columns are set together, or none is.
ALTER TABLE accounts
    ADD COLUMN tester_started_at timestamptz,
    ADD COLUMN tester_until      timestamptz,
    ADD COLUMN tester_credits    integer CHECK (tester_credits >= 0),
    ADD CONSTRAINT accounts_tester_shape CHECK (
        (tester_started_at IS NULL) = (tester_until IS NULL)
        AND (tester_started_at IS NULL) = (tester_credits IS NULL));

CREATE INDEX accounts_testers ON accounts (tester_until) WHERE tester_started_at IS NOT NULL;

-- Mail to Amazon SES's mailbox simulator (simulator.amazonses.com) never
-- reaches anyone, and SES leaves it out of the account's bounce and
-- complaint rates. mailgoose leaves it out of its own rates, its pauses and
-- its suppressions too, and a tester's simulator copies use no tester
-- credits. A campaign's copies are marked when the campaign is launched,
-- their events when they're stored, and test sends when they're sent.
ALTER TABLE campaign_recipients ADD COLUMN simulated boolean NOT NULL DEFAULT false;
ALTER TABLE send_events ADD COLUMN simulated boolean NOT NULL DEFAULT false;
ALTER TABLE test_sends ADD COLUMN simulated boolean NOT NULL DEFAULT false;

ALTER TABLE admin_actions DROP CONSTRAINT admin_actions_action_check;
ALTER TABLE admin_actions ADD CONSTRAINT admin_actions_action_check CHECK (action IN (
    'grant_admin', 'revoke_admin', 'pause', 'unpause', 'auto_pause',
    'stop_campaign', 'approve_import', 'grant_credits', 'throttle',
    'suspend', 'unsuspend', 'auto_suspend',
    'approve_signup', 'reject_signup',
    'allow_full_sending', 'set_test_mode',
    'grant_tester', 'revoke_tester'));

-- +goose Down
DELETE FROM admin_actions WHERE action IN ('grant_tester', 'revoke_tester');
ALTER TABLE admin_actions DROP CONSTRAINT admin_actions_action_check;
ALTER TABLE admin_actions ADD CONSTRAINT admin_actions_action_check CHECK (action IN (
    'grant_admin', 'revoke_admin', 'pause', 'unpause', 'auto_pause',
    'stop_campaign', 'approve_import', 'grant_credits', 'throttle',
    'suspend', 'unsuspend', 'auto_suspend',
    'approve_signup', 'reject_signup',
    'allow_full_sending', 'set_test_mode'));
ALTER TABLE test_sends DROP COLUMN simulated;
ALTER TABLE send_events DROP COLUMN simulated;
ALTER TABLE campaign_recipients DROP COLUMN simulated;
DROP INDEX accounts_testers;
ALTER TABLE accounts
    DROP CONSTRAINT accounts_tester_shape,
    DROP COLUMN tester_credits,
    DROP COLUMN tester_until,
    DROP COLUMN tester_started_at;
