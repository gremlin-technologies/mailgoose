-- +goose Up

-- Optional custom domains (MG-85). An account with no verified domain of
-- its own may still send, from mailgoose's shared sending domain
-- (SHARED_SENDING_DOMAIN), when its From address is one it has proved it
-- owns: the owner's own sign-in address, or a confirmed sender address. The
-- message then goes from <sending_slug>@<shared domain>, with replies to
-- their own address.
--
-- sending_slug is the account's local part on the shared domain. It's made
-- from the account's name when the account is created, and never changes
-- after, so renaming an account doesn't move its sending address. Slugs are
-- lower-case letters, digits and hyphens, 3 to 40 characters. A taken or
-- reserved slug, or one too short, gets a short random suffix.
ALTER TABLE accounts ADD COLUMN sending_slug text;

-- +goose StatementBegin
CREATE FUNCTION next_sending_slug(account_name text) RETURNS text
LANGUAGE plpgsql VOLATILE AS $$
DECLARE
    base      text;
    candidate text;
    reserved  text[] := ARRAY['postmaster', 'abuse', 'admin', 'administrator', 'hostmaster', 'webmaster',
        'mailer-daemon', 'root', 'noreply', 'no-reply', 'bounce', 'bounces', 'support', 'security', 'info',
        'mail', 'mailgoose'];
BEGIN
    base := lower(coalesce(account_name, ''));
    base := translate(base, 'àáâãäåāçćčèéêëēęìíîïīłñńòóôõöøōùúûüūýÿžźż',
                            'aaaaaaaccceeeeeeiiiiilnnooooooouuuuuyyzzz');
    base := replace(base, 'ß', 'ss');
    -- Pat's Bakery is pats-bakery, not pat-s-bakery.
    base := regexp_replace(base, '[''’`]', '', 'g');
    base := regexp_replace(base, '[^a-z0-9]+', '-', 'g');
    base := trim(both '-' from base);
    base := trim(both '-' from left(base, 32));
    IF base = '' THEN
        base := 'account';
    END IF;
    candidate := base;
    LOOP
        IF length(candidate) >= 3 AND NOT candidate = ANY (reserved) THEN
            -- Two accounts made at once with the same name wait for each
            -- other here, so the second sees the first's slug.
            PERFORM pg_advisory_xact_lock(hashtextextended('sending_slug:' || current_schema() || ':' || candidate, 0));
            IF NOT EXISTS (SELECT 1 FROM accounts WHERE sending_slug = candidate) THEN
                RETURN candidate;
            END IF;
        END IF;
        candidate := base || '-' || substr(md5(random()::text || clock_timestamp()::text), 1, 4);
    END LOOP;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION accounts_assign_sending_slug() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.sending_slug IS NULL THEN
        NEW.sending_slug := next_sending_slug(NEW.name);
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- Oldest accounts first, so the first account with a name keeps its plain
-- slug.
-- +goose StatementBegin
DO $$
DECLARE
    a record;
BEGIN
    FOR a IN SELECT id, name FROM accounts ORDER BY created_at, id LOOP
        UPDATE accounts SET sending_slug = next_sending_slug(a.name) WHERE id = a.id;
    END LOOP;
END;
$$;
-- +goose StatementEnd

ALTER TABLE accounts
    ALTER COLUMN sending_slug SET NOT NULL,
    ADD CONSTRAINT accounts_sending_slug_check CHECK (sending_slug ~ '^[a-z0-9-]{3,40}$'),
    ADD CONSTRAINT accounts_sending_slug_key UNIQUE (sending_slug);

CREATE TRIGGER accounts_sending_slug BEFORE INSERT ON accounts
    FOR EACH ROW EXECUTE FUNCTION accounts_assign_sending_slug();

-- An account's sender addresses, at most 10 (pending ones included): the
-- addresses it may send from on the shared domain. One counts once its
-- owner clicks the link emailed to it (confirmed_at). Only the link's hash
-- is kept; the link works for 7 days after it's sent. The owner's own
-- sign-in address always counts and isn't stored here.
CREATE TABLE sender_addresses (
    id                 uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id         uuid        NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    email              text        NOT NULL,
    confirm_token_hash bytea,
    confirm_expires_at timestamptz,
    confirm_sent_at    timestamptz,
    confirmed_at       timestamptz,
    created_at         timestamptz NOT NULL
);

CREATE UNIQUE INDEX sender_addresses_account_email ON sender_addresses (account_id, lower(email));
CREATE UNIQUE INDEX sender_addresses_token ON sender_addresses (confirm_token_hash) WHERE confirm_token_hash IS NOT NULL;

-- Every sender address confirm email an account sent, for its daily cap
-- (adds and resends alike). It outlives the address's row, so removing an
-- address and adding it again doesn't reset the cap. Rows older than a day
-- are pruned at the account's next add or resend.
CREATE TABLE sender_address_emails (
    account_id uuid        NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    sent_at    timestamptz NOT NULL
);

CREATE INDEX sender_address_emails_account ON sender_address_emails (account_id, sent_at);

-- +goose Down
DROP TABLE sender_address_emails;
DROP TABLE sender_addresses;
DROP TRIGGER accounts_sending_slug ON accounts;
ALTER TABLE accounts
    DROP CONSTRAINT accounts_sending_slug_key,
    DROP CONSTRAINT accounts_sending_slug_check,
    DROP COLUMN sending_slug;
DROP FUNCTION accounts_assign_sending_slug();
DROP FUNCTION next_sending_slug(text);
