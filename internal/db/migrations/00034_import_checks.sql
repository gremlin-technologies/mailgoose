-- +goose Up

-- Import list checks and list source (MG-63). Every import says where its
-- list came from; the file's addresses are checked for bad syntax, role
-- addresses and disposable domains, and compared, hashed, with other
-- accounts' imports. Suspicious imports are held for review.

-- Where the customer says the list came from. Imports from before MG-63 have
-- none.
ALTER TABLE imports ADD COLUMN list_source text
    CHECK (list_source IN ('form', 'customers', 'personal', 'export', 'other'));
ALTER TABLE imports ADD COLUMN list_source_detail text;
-- What the checks found when the import was created. shared_addresses is
-- how many of its addresses are in other accounts' imports, and
-- shared_account_id the account with the most of them; only admins see
-- either.
ALTER TABLE imports ADD COLUMN invalid_rows integer NOT NULL DEFAULT 0;
ALTER TABLE imports ADD COLUMN role_addresses integer NOT NULL DEFAULT 0;
ALTER TABLE imports ADD COLUMN disposable_addresses integer NOT NULL DEFAULT 0;
ALTER TABLE imports ADD COLUMN shared_addresses integer NOT NULL DEFAULT 0;
ALTER TABLE imports ADD COLUMN shared_account_id uuid REFERENCES accounts (id) ON DELETE SET NULL;

ALTER TABLE imports DROP CONSTRAINT imports_hold_reason_check;
ALTER TABLE imports ADD CONSTRAINT imports_hold_reason_check CHECK (hold_reason IN (
    'large_first_import', 'over_contact_limit', 'shared_list', 'risky_addresses', 'invalid_addresses'));

-- Each import's distinct addresses, hashed the same way for every account,
-- so a list imported into several accounts can be found without one account
-- ever reading another's contacts. They go with the import (and so with the
-- account).
CREATE TABLE import_address_hashes (
    import_id  uuid  NOT NULL REFERENCES imports (id) ON DELETE CASCADE,
    account_id uuid  NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    hash       bytea NOT NULL,
    PRIMARY KEY (import_id, hash)
);
CREATE INDEX import_address_hashes_hash ON import_address_hashes (hash, account_id);

-- An imported contact's consent record is the list source the import gave:
-- a consents row with source 'import', no form and no IP.
ALTER TABLE consents ALTER COLUMN ip DROP NOT NULL;
ALTER TABLE consents DROP CONSTRAINT consents_source_check;
ALTER TABLE consents ADD CONSTRAINT consents_source_check CHECK (source IN ('form', 'import'));
ALTER TABLE consents ADD COLUMN import_id uuid REFERENCES imports (id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE consents DROP COLUMN import_id;
DELETE FROM consents WHERE source = 'import';
ALTER TABLE consents DROP CONSTRAINT consents_source_check;
ALTER TABLE consents ADD CONSTRAINT consents_source_check CHECK (source IN ('form'));
ALTER TABLE consents ALTER COLUMN ip SET NOT NULL;
DROP TABLE import_address_hashes;
UPDATE imports SET hold_reason = 'large_first_import'
    WHERE hold_reason IN ('shared_list', 'risky_addresses', 'invalid_addresses');
ALTER TABLE imports DROP CONSTRAINT imports_hold_reason_check;
ALTER TABLE imports ADD CONSTRAINT imports_hold_reason_check CHECK (hold_reason IN ('large_first_import', 'over_contact_limit'));
ALTER TABLE imports DROP COLUMN shared_account_id;
ALTER TABLE imports DROP COLUMN shared_addresses;
ALTER TABLE imports DROP COLUMN disposable_addresses;
ALTER TABLE imports DROP COLUMN role_addresses;
ALTER TABLE imports DROP COLUMN invalid_rows;
ALTER TABLE imports DROP COLUMN list_source_detail;
ALTER TABLE imports DROP COLUMN list_source;
