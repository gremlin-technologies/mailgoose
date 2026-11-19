-- +goose Up

-- Early-warning throttles (MG-61). A campaign of 500 or more whose
-- complaints or hard bounces pass the early-warning thresholds (below the
-- ones that pause) drops its account to the new-account daily limit until
-- `until`, seven days on. Another such campaign meanwhile adds a row, so
-- the limit lifts seven days after the last one; the third within 30 days
-- pauses the account instead. Rows are the history: they're never updated.
CREATE TABLE account_throttles (
    id          bigserial   PRIMARY KEY,
    account_id  uuid        NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    campaign_id uuid        REFERENCES campaigns (id) ON DELETE SET NULL,
    -- What the account's owner is told, as for a pause.
    reason      text        NOT NULL CHECK (reason <> ''),
    -- The campaign's copies sent, hard bounces and complaints.
    rates       jsonb       NOT NULL DEFAULT '{}',
    created_at  timestamptz NOT NULL DEFAULT now(),
    until       timestamptz NOT NULL CHECK (until > created_at)
);
CREATE INDEX account_throttles_account ON account_throttles (account_id, created_at DESC);

-- When a campaign throttled its account: once per campaign, like
-- auto_paused_at.
ALTER TABLE campaigns ADD COLUMN throttled_at timestamptz;

-- Throttles are logged with the other automatic actions.
ALTER TABLE admin_actions DROP CONSTRAINT admin_actions_action_check;
ALTER TABLE admin_actions ADD CONSTRAINT admin_actions_action_check CHECK (action IN (
    'grant_admin', 'revoke_admin', 'pause', 'unpause', 'auto_pause',
    'stop_campaign', 'approve_import', 'grant_credits', 'throttle'));

-- +goose Down
DELETE FROM admin_actions WHERE action = 'throttle';
ALTER TABLE admin_actions DROP CONSTRAINT admin_actions_action_check;
ALTER TABLE admin_actions ADD CONSTRAINT admin_actions_action_check CHECK (action IN (
    'grant_admin', 'revoke_admin', 'pause', 'unpause', 'auto_pause',
    'stop_campaign', 'approve_import', 'grant_credits'));
ALTER TABLE campaigns DROP COLUMN throttled_at;
DROP TABLE account_throttles;
