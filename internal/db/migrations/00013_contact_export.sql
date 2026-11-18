-- +goose Up

-- CSV export and data requests (CON-7, CMP-9). A contact remembers the
-- import that added it, so an export and a data request can say whether the
-- account confirmed it had permission to mail them. Imports can read status
-- and tags columns, so an export imports back as the same contacts.
ALTER TABLE contacts ADD COLUMN import_id uuid REFERENCES imports (id) ON DELETE SET NULL;
ALTER TABLE imports ADD COLUMN status_column text;
ALTER TABLE imports ADD COLUMN tags_column text;

-- +goose Down
ALTER TABLE imports DROP COLUMN tags_column;
ALTER TABLE imports DROP COLUMN status_column;
ALTER TABLE contacts DROP COLUMN import_id;
