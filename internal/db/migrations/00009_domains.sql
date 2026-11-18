-- +goose Up

-- Customer sending domains (DOM-1 to DOM-4). Each is an SES identity with
-- Easy DKIM and a custom MAIL FROM subdomain. A domain stays pending while
-- the person adds the DNS records; the API re-checks it every 5 minutes for
-- 72 hours (check_until), then marks it failed. A domain name belongs to one
-- account at a time, because SES has one identity per name.
CREATE TABLE domains (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id      uuid        NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    name            text        NOT NULL UNIQUE,
    mail_from       text        NOT NULL,
    status          text        NOT NULL DEFAULT 'pending',
    dkim_tokens     text[]      NOT NULL DEFAULT '{}',
    -- Whether _dmarc had a record when the domain was added; if not, a
    -- DMARC record is suggested.
    had_dmarc       boolean     NOT NULL DEFAULT false,
    -- The last check: one entry per DNS record, and what SES reported.
    checks          jsonb,
    ses_status      text,
    created_at      timestamptz NOT NULL,
    last_checked_at timestamptz,
    next_check_at   timestamptz,
    check_until     timestamptz NOT NULL,
    verified_at     timestamptz,
    failed_at       timestamptz,
    CONSTRAINT domains_status_check CHECK (status IN ('pending', 'verified', 'failed'))
);

CREATE INDEX domains_account ON domains (account_id, created_at, id);
CREATE INDEX domains_due ON domains (next_check_at) WHERE status = 'pending';

-- +goose Down
DROP TABLE domains;
