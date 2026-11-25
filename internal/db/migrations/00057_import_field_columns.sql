-- +goose Up

-- An import's contact field columns (MG-131): a JSON array of
-- {"field_id", "column", "created"}, one for each contact field the import
-- fills, "created" when the import made the field. The worker looks each
-- field up again as it writes, so a field deleted meanwhile is skipped.
ALTER TABLE imports ADD COLUMN field_columns jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(field_columns) = 'array');

-- Contacts already in the account whose contact field values the import
-- changed. They're still counted as skipped rows (`exists`), since the
-- import didn't add them.
ALTER TABLE imports ADD COLUMN existing_updated integer NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE imports DROP COLUMN existing_updated;
ALTER TABLE imports DROP COLUMN field_columns;
