-- +goose Up

-- A daily cap on each form's new signups (MG-92): a form adds at most
-- FORM_DAILY_SIGNUP_LIMIT new contacts (1,000 by default) in any 24 hours,
-- counted here so the cap holds across API tasks. A row is a submission
-- that added a contact. Consent records can't be counted instead: deleting
-- a contact deletes its consents, so a form whose bot signups were deleted
-- would take more at once. Each form's rows older than a day are pruned
-- as it takes new ones, so a form keeps at most a day's worth; they go
-- with the form.
CREATE TABLE form_signups (
    form_id    uuid        NOT NULL REFERENCES forms (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL
);
CREATE INDEX form_signups_form ON form_signups (form_id, created_at);

-- Signups in the last day before this migration count too: the form's new
-- contacts, whose consent record was made with them.
INSERT INTO form_signups (form_id, created_at)
SELECT k.form_id, k.created_at
FROM consents k JOIN contacts c ON c.id = k.contact_id AND c.created_at = k.created_at AND c.source = 'form'
WHERE k.form_id IS NOT NULL AND k.source = 'form' AND k.created_at > now() - interval '1 day';

-- When the owner and ops were last told the form reached its cap, so a
-- day's notices go once whichever API task refuses a signup first.
ALTER TABLE forms ADD COLUMN signup_cap_noticed_at timestamptz;

-- +goose Down
ALTER TABLE forms DROP COLUMN signup_cap_noticed_at;
DROP TABLE form_signups;
