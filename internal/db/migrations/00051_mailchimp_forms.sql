-- +goose Up

-- Draft signup forms from a Mailchimp import (MG-76). Mailchimp's embedded
-- form code posts to Mailchimp, so confirming an import gives each imported
-- audience a ready replacement. Those forms start as drafts, taking no
-- signups until the customer opens and saves them; saving any form clears
-- draft. mailchimp_audience is the audience a form was made for, so running
-- the import again finds the form it made rather than making a second.
ALTER TABLE forms ADD COLUMN draft boolean NOT NULL DEFAULT false;
ALTER TABLE forms ADD COLUMN mailchimp_audience text;
CREATE UNIQUE INDEX forms_mailchimp_audience ON forms (account_id, lower(mailchimp_audience))
    WHERE mailchimp_audience IS NOT NULL;

-- The forms a confirmed bundle made or found, as the API shows them:
-- [{"id", "name", "audience", "new"}].
ALTER TABLE mailchimp_bundles ADD COLUMN forms jsonb NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE mailchimp_bundles DROP COLUMN forms;
DROP INDEX forms_mailchimp_audience;
ALTER TABLE forms DROP COLUMN mailchimp_audience;
ALTER TABLE forms DROP COLUMN draft;
