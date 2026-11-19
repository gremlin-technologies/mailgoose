-- +goose Up

-- Pictures uploaded for image blocks (DES-5). The file itself is in the
-- image store (S3 behind the CDN, or a local directory) under key, which is
-- "<account id>/<random>.<ext>".
CREATE TABLE images (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id   uuid        NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    key          text        NOT NULL UNIQUE,
    -- The uploaded file's name, for showing; never used to store it.
    file_name    text        NOT NULL DEFAULT '',
    content_type text        NOT NULL CHECK (content_type IN ('image/jpeg', 'image/png', 'image/gif', 'image/webp')),
    bytes        integer     NOT NULL CHECK (bytes > 0),
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX images_account_created ON images (account_id, created_at DESC, id DESC);

-- +goose Down
DROP TABLE images;
