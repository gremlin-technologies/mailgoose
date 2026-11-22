-- +goose Up

-- What an admin reads to judge a Mailchimp import (MG-77): where it came
-- from, its audiences with their members by Mailchimp status, and the
-- opt-in times. Kept from the preview on confirm, which clears the preview.
ALTER TABLE mailchimp_bundles ADD COLUMN signals jsonb;

-- +goose Down
ALTER TABLE mailchimp_bundles DROP COLUMN signals;
