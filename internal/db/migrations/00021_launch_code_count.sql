-- +goose Up

-- A launch code remembers how many contacts its summary counted, so the
-- confirm can refuse a launch whose audience has grown well past what the
-- person agreed to (MG-30 review follow-up).
ALTER TABLE campaign_launch_codes
    ADD COLUMN recipient_count integer NOT NULL DEFAULT 0;

-- +goose Down

ALTER TABLE campaign_launch_codes DROP COLUMN recipient_count;
