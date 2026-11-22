-- +goose Up

-- Saved segments (MG-114): a named set of contact conditions, all of which
-- must match, worked out each time the segment is used. The conditions are
-- kept as the API gives them, ids included, so a deleted tag or campaign
-- still shows which condition broke.
CREATE TABLE segments (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    name text NOT NULL,
    conditions jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);

CREATE UNIQUE INDEX segments_account_name ON segments (account_id, lower(name));

-- +goose Down
DROP TABLE segments;
