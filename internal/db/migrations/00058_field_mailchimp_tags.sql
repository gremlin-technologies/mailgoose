-- +goose Up

-- The Mailchimp tags (MMERGE3, COMPANY) of the merge fields that Mailchimp
-- imports put into each contact field, where the import knew them (a
-- connected account's do; an upload's don't), so a later import's emails
-- find the field by its tag (MG-134). A tag names one field: the one the
-- latest import put its merge field into. Not shown in the API.
ALTER TABLE contact_fields ADD COLUMN mailchimp_tags text[] NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE contact_fields DROP COLUMN mailchimp_tags;
