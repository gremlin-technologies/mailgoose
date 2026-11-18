-- +goose Up

-- Idempotency keys (API-4). A row is claimed when a request with an
-- Idempotency-Key starts and gets its response when it finishes; repeats
-- within 24 hours replay the response. The API ignores rows past their TTL
-- and reuses them, so no cleanup job is needed for correctness; the index on
-- created_at is there for one that trims old rows.
CREATE TABLE idempotency_keys (
    scope            text        NOT NULL,
    key              text        NOT NULL,
    request_hash     bytea       NOT NULL,
    -- Null while the first request is still running.
    status_code      integer,
    response_headers jsonb,
    response_body    bytea,
    created_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (scope, key)
);

CREATE INDEX idempotency_keys_created_at ON idempotency_keys (created_at);

-- +goose Down
DROP TABLE idempotency_keys;
