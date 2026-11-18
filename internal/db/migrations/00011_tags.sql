-- +goose Up

-- Tags (CON-4): basic labels on contacts. Names are unique per account,
-- ignoring case. Deleting a tag or a contact removes their links.
CREATE TABLE tags (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id uuid        NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    name       text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX tags_account_name ON tags (account_id, lower(name));

CREATE TABLE contact_tags (
    contact_id uuid        NOT NULL REFERENCES contacts (id) ON DELETE CASCADE,
    tag_id     uuid        NOT NULL REFERENCES tags (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (contact_id, tag_id)
);
CREATE INDEX contact_tags_tag ON contact_tags (tag_id, contact_id);

-- +goose Down
DROP TABLE contact_tags;
DROP TABLE tags;
