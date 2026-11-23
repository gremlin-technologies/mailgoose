-- +goose Up
-- An import's first row can be a contact rather than a header, as when
-- contacts are pasted from a spreadsheet (MG-125).
ALTER TABLE imports ADD COLUMN has_header boolean NOT NULL DEFAULT true;

-- +goose Down
ALTER TABLE imports DROP COLUMN has_header;
