-- +goose Up

-- The daily abuse digest to the ops address (MG-67). One row per UTC day,
-- claimed by whichever worker task gets there first at ABUSE_DIGEST_HOUR, so
-- the digest goes at most once a day however many tasks run or restart. A
-- quiet day's row has items = 0 and sent = false. It holds no account data.
CREATE TABLE ops_digests (
    day        date        PRIMARY KEY,
    checked_at timestamptz NOT NULL,
    -- How many throttles, pauses, suspensions, held imports and waiting
    -- sign-ups it listed.
    items      integer     NOT NULL CHECK (items >= 0),
    sent       boolean     NOT NULL
);

-- +goose Down
DROP TABLE ops_digests;
