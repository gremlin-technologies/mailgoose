-- +goose Up

-- Sliced first sends (MG-62). A new account's campaign goes first to a
-- slice of its recipients, chosen at random at launch; the rest waits until
-- the slice's bounces and complaints have had time to come in.
--
-- slice_state is where the campaign is:
--   sending_slice  the slice is going out
--   slice_hold     the slice is out; the rest waits until slice_release_at
--   held           the slice went over the early-warning limits: the rest
--                  waits until slice_release_at, when the throttle lifts
--   released       the rest was let go
--   stopped        the slice went over the pause limits: the rest never sent
-- It's null for a campaign launched without a slice.
ALTER TABLE campaigns
    ADD COLUMN slice_size integer CHECK (slice_size > 0),
    ADD COLUMN slice_state text CHECK (slice_state IN ('sending_slice', 'slice_hold', 'held', 'released', 'stopped')),
    ADD COLUMN slice_release_at timestamptz,
    ADD CONSTRAINT campaigns_slice_check CHECK ((slice_size IS NULL) = (slice_state IS NULL));

-- The recipients in a campaign's slice.
ALTER TABLE campaign_recipients ADD COLUMN in_slice boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE campaign_recipients DROP COLUMN in_slice;
ALTER TABLE campaigns DROP CONSTRAINT campaigns_slice_check;
ALTER TABLE campaigns DROP COLUMN slice_release_at, DROP COLUMN slice_state, DROP COLUMN slice_size;
