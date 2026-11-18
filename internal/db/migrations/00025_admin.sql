-- +goose Up

-- Account health, the kill switch and admin tools (MG-34: CMP-6, CMP-7,
-- CMP-8, OPS-6).

-- mailgoose staff who may use `mailgoose admin`. Only `mailgoosed admin
-- grant`, run with database access, makes someone an admin.
ALTER TABLE users ADD COLUMN admin boolean NOT NULL DEFAULT false;

-- When a campaign's bounces or complaints paused its account (CMP-6). A
-- campaign pauses its account once, and an account with such a campaign in
-- the last 30 days keeps the new-account send limit (CMP-7).
ALTER TABLE campaigns ADD COLUMN auto_paused_at timestamptz;
CREATE INDEX campaigns_auto_paused ON campaigns (account_id, auto_paused_at) WHERE auto_paused_at IS NOT NULL;

-- Every admin action, and every automatic pause: who, why and to what.
-- Kept when an account is deleted, like the credit ledger.
CREATE TABLE admin_actions (
    id            bigserial   PRIMARY KEY,
    -- The admin, while they exist; null for automatic pauses and for
    -- `mailgoosed admin` run on the server.
    actor_user_id uuid        REFERENCES users (id) ON DELETE SET NULL,
    -- The admin's address at the time, "mailgoosed" or "automatic".
    actor         text        NOT NULL,
    action        text        NOT NULL CHECK (action IN (
        'grant_admin', 'revoke_admin', 'pause', 'unpause', 'auto_pause',
        'stop_campaign', 'approve_import', 'grant_credits')),
    account_id    uuid        REFERENCES accounts (id) ON DELETE CASCADE,
    -- What it acted on besides the account: a campaign, an import or a user.
    target_id     text,
    reason        text        NOT NULL CHECK (reason <> ''),
    -- What changed, such as the credits granted or the rates that paused.
    details       jsonb       NOT NULL DEFAULT '{}',
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX admin_actions_account ON admin_actions (account_id, id DESC) WHERE account_id IS NOT NULL;
CREATE INDEX admin_actions_created ON admin_actions (id DESC);

-- +goose Down
DROP TABLE admin_actions;
DROP INDEX campaigns_auto_paused;
ALTER TABLE campaigns DROP COLUMN auto_paused_at;
ALTER TABLE users DROP COLUMN admin;
