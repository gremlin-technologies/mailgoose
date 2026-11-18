-- +goose Up

-- Signup forms (FRM-1, FRM-2). fields is the list the form asks for, in
-- order; email is always one. signing_key signs the timestamp in the
-- form's embed code, so submissions (MG-36) can tell a real embed from a
-- forged post. Deleting a tag takes it off every form.
CREATE TABLE forms (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id      uuid        NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    name            text        NOT NULL,
    fields          jsonb       NOT NULL,
    button_text     text        NOT NULL,
    success_message text        NOT NULL,
    redirect_url    text,
    double_opt_in   boolean     NOT NULL DEFAULT true,
    confirm_subject text        NOT NULL,
    confirm_text    text        NOT NULL,
    signing_key     bytea       NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX forms_account_created ON forms (account_id, created_at DESC, id DESC);

CREATE TABLE form_tags (
    form_id uuid NOT NULL REFERENCES forms (id) ON DELETE CASCADE,
    tag_id  uuid NOT NULL REFERENCES tags (id) ON DELETE CASCADE,
    PRIMARY KEY (form_id, tag_id)
);
CREATE INDEX form_tags_tag ON form_tags (tag_id);

-- +goose Down
DROP TABLE form_tags;
DROP TABLE forms;
