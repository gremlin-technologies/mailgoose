-- +goose Up

-- Account settings (AUTH-5). The default From name and address prefill new
-- campaigns. The postal address goes in every email's footer, as anti-spam
-- law requires; it may be empty until the first send, and launch is blocked
-- while it is. The time zone is an IANA name, used to show and schedule
-- times.
ALTER TABLE accounts
    ADD COLUMN from_name      text,
    ADD COLUMN from_email     text,
    ADD COLUMN postal_address text,
    ADD COLUMN time_zone      text NOT NULL DEFAULT 'UTC';

-- +goose Down
ALTER TABLE accounts
    DROP COLUMN time_zone,
    DROP COLUMN postal_address,
    DROP COLUMN from_email,
    DROP COLUMN from_name;
