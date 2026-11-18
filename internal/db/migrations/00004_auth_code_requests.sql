-- +goose Up

-- Every request for an emailed code (sign-up or sign-in), for the limits per
-- address and per IP (AUTH-2). Rows older than a day are deleted as new ones
-- arrive.
CREATE TABLE auth_code_requests (
    id          bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email_lower text        NOT NULL,
    ip          text        NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX auth_code_requests_email ON auth_code_requests (email_lower, created_at);
CREATE INDEX auth_code_requests_ip ON auth_code_requests (ip, created_at);
CREATE INDEX auth_code_requests_created ON auth_code_requests (created_at);

-- Every wrong code entered for an address, across all its codes, for the cap
-- on guesses per address (AUTH-2). Addresses with no account are counted too.
-- Rows older than a day are deleted as new ones arrive.
CREATE TABLE auth_code_failures (
    id          bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email_lower text        NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX auth_code_failures_email ON auth_code_failures (email_lower, created_at);
CREATE INDEX auth_code_failures_created ON auth_code_failures (created_at);

-- +goose Down
DROP TABLE auth_code_failures;
DROP TABLE auth_code_requests;
