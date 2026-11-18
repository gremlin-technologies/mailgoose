-- +goose Up

-- Unsubscribe links (CMP-1). Each account has a key that signs its
-- recipients' unsubscribe tokens, made the first time it's needed.
CREATE TABLE unsubscribe_keys (
    account_id uuid        PRIMARY KEY REFERENCES accounts (id) ON DELETE CASCADE,
    key        bytea       NOT NULL CHECK (length(key) = 32),
    created_at timestamptz NOT NULL DEFAULT now()
);

-- One row each time a contact unsubscribes through a link or a one-click
-- header, with the campaign the link came from. Rows outlive the contact
-- and the campaign, so a report's unsubscribe count stays right after
-- either is deleted.
CREATE TABLE unsubscribes (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id  uuid        NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    contact_id  uuid        REFERENCES contacts (id) ON DELETE SET NULL,
    campaign_id uuid        REFERENCES campaigns (id) ON DELETE SET NULL,
    source      text        NOT NULL CHECK (source IN ('link', 'one_click')),
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX unsubscribes_campaign ON unsubscribes (campaign_id) WHERE campaign_id IS NOT NULL;
CREATE INDEX unsubscribes_contact ON unsubscribes (contact_id) WHERE contact_id IS NOT NULL;
CREATE INDEX unsubscribes_account_created ON unsubscribes (account_id, created_at DESC);

-- +goose Down
DROP TABLE unsubscribes;
DROP TABLE unsubscribe_keys;
