-- +goose Up

-- Campaigns (CAM-1, CAM-9). all_contacts picks every subscribed contact;
-- otherwise the contacts with any of the campaign's tags. send_at null
-- means "when launched". The email is copied into the campaign at launch
-- (MG-30), so a draft whose email is deleted just has none. version goes up
-- with every edit, so a launch confirmation can go stale (CAM-4).
CREATE TABLE campaigns (
    id                uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id        uuid        NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    name              text        NOT NULL,
    email_id          uuid        REFERENCES emails (id) ON DELETE SET NULL,
    all_contacts      boolean     NOT NULL,
    send_at           timestamptz,
    status            text        NOT NULL DEFAULT 'draft'
        CHECK (status IN ('draft', 'scheduled', 'sending', 'sent', 'cancelled', 'paused', 'failed')),
    version           integer     NOT NULL DEFAULT 1,
    status_changed_at timestamptz NOT NULL DEFAULT now(),
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX campaigns_account_created ON campaigns (account_id, created_at DESC, id DESC);
CREATE INDEX campaigns_email ON campaigns (email_id);

-- A deleted tag comes off every campaign; contacts.DeleteTag counts the
-- draft and scheduled ones first.
CREATE TABLE campaign_tags (
    campaign_id uuid NOT NULL REFERENCES campaigns (id) ON DELETE CASCADE,
    tag_id      uuid NOT NULL REFERENCES tags (id) ON DELETE CASCADE,
    PRIMARY KEY (campaign_id, tag_id)
);
CREATE INDEX campaign_tags_tag ON campaign_tags (tag_id);

-- +goose Down
DROP TABLE campaign_tags;
DROP TABLE campaigns;
