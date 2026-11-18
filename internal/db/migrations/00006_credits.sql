-- +goose Up

-- The credit ledger (BIL-1). One credit is one email to one recipient. Every
-- change to an account's credits is a row here, and the balance is the sum of
-- amount: it is never stored anywhere else.
--
-- Credits arrive in lots: a monthly free grant, or a purchase. Each lot
-- expires (a free grant at the next grant, a purchase after 24 months), and
-- credits are used from the lot that expires first. credit_allocations says
-- which lots each debit came from, so a lot's remaining credits are its
-- amount plus its allocations.
CREATE TABLE credit_transactions (
    id         bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    account_id uuid        NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    type       text        NOT NULL,
    amount     bigint      NOT NULL,
    -- Lots only: when unused credits expire.
    expires_at timestamptz,
    -- What the transaction is for, such as a campaign ID for a reservation or
    -- a Stripe session ID for a purchase.
    ref        text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT credit_transactions_type_check
        CHECK (type IN ('grant', 'purchase', 'reserve', 'release', 'spend', 'fee', 'expiry')),
    CONSTRAINT credit_transactions_amount_nonzero CHECK (amount <> 0),
    CONSTRAINT credit_transactions_lot_shape CHECK (
        (type IN ('grant', 'purchase') AND amount > 0 AND expires_at IS NOT NULL)
        OR (type = 'release' AND amount > 0 AND expires_at IS NULL)
        OR (type IN ('reserve', 'spend', 'fee', 'expiry') AND amount < 0 AND expires_at IS NULL)
    )
);

CREATE INDEX credit_transactions_account ON credit_transactions (account_id, id);
CREATE INDEX credit_transactions_ref ON credit_transactions (account_id, ref) WHERE ref <> '';

CREATE TABLE credit_allocations (
    transaction_id bigint NOT NULL REFERENCES credit_transactions (id) ON DELETE CASCADE,
    lot_id         bigint NOT NULL REFERENCES credit_transactions (id) ON DELETE CASCADE,
    amount         bigint NOT NULL,
    PRIMARY KEY (transaction_id, lot_id),
    CONSTRAINT credit_allocations_amount_nonzero CHECK (amount <> 0)
);

CREATE INDEX credit_allocations_lot ON credit_allocations (lot_id);

-- +goose Down
DROP TABLE credit_allocations;
DROP TABLE credit_transactions;
