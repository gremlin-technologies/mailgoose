-- +goose Up

-- Table sizes, once a day (MG-95): what `mailgoose admin storage` and the
-- daily digest compare today's sizes with, to show how much a table grew
-- over 7 days. The digest job writes one row per table in the schema when it
-- claims its day, sizes from pg_total_relation_size (the table with its
-- indexes and TOAST) and the planner's row estimate (pg_class.reltuples,
-- null for a table never analysed). Rows older than 8 weeks are deleted by
-- the same job. It holds no account data.
CREATE TABLE table_size_snapshots (
    day          date        NOT NULL,
    table_name   text        NOT NULL,
    total_bytes  bigint      NOT NULL CHECK (total_bytes >= 0),
    row_estimate bigint      CHECK (row_estimate >= 0),
    taken_at     timestamptz NOT NULL,
    PRIMARY KEY (day, table_name)
);

-- +goose Down
DROP TABLE table_size_snapshots;
