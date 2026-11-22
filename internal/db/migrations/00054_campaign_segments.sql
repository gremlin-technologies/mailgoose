-- +goose Up
-- A campaign can go to a saved segment (MG-115) instead of all contacts or
-- tags. The segment is worked out at launch and its contacts fixed in
-- campaign_recipients, so a launched campaign doesn't need it after: its
-- link is cleared if the segment is deleted. A draft's or scheduled
-- campaign's segment can't be deleted (the app refuses it first).
ALTER TABLE campaigns ADD COLUMN segment_id uuid REFERENCES segments (id) ON DELETE SET NULL;
CREATE INDEX campaigns_segment ON campaigns (segment_id) WHERE segment_id IS NOT NULL;

-- +goose Down
DROP INDEX campaigns_segment;
ALTER TABLE campaigns DROP COLUMN segment_id;
