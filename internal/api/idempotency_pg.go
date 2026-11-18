package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// PGQuerier is the part of a pgx pool the Postgres stores use.
type PGQuerier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// PGIdempotencyStore keeps idempotency keys in the idempotency_keys table, so
// every API task sees the same keys.
type PGIdempotencyStore struct {
	DB PGQuerier
}

// Claim implements IdempotencyStore. One statement inserts the claim, or
// takes over a row that's past its TTL (or a stale unfinished claim); when
// neither happens, the existing row is read back.
func (s PGIdempotencyStore) Claim(ctx context.Context, scope, key string, hash []byte, now time.Time) (bool, *IdempotencyRecord, error) {
	var claimed bool
	err := s.DB.QueryRow(ctx, `
INSERT INTO idempotency_keys (scope, key, request_hash, created_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (scope, key) DO UPDATE
SET request_hash = EXCLUDED.request_hash, created_at = EXCLUDED.created_at,
    status_code = NULL, response_headers = NULL, response_body = NULL
WHERE (idempotency_keys.status_code IS NOT NULL AND idempotency_keys.created_at <= $4 - make_interval(secs => $5))
   OR (idempotency_keys.status_code IS NULL AND idempotency_keys.created_at <= $4 - make_interval(secs => $6))
RETURNING true`,
		scope, key, hash, now, IdempotencyTTL.Seconds(), idempotencyStaleClaim.Seconds()).Scan(&claimed)
	if err == nil {
		return true, nil, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, nil, err
	}

	var (
		rec     IdempotencyRecord
		status  *int
		headers []byte
		body    []byte
	)
	err = s.DB.QueryRow(ctx, `
SELECT request_hash, status_code, response_headers, response_body
FROM idempotency_keys WHERE scope = $1 AND key = $2`, scope, key).Scan(&rec.RequestHash, &status, &headers, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		// Released between the two statements; try once more.
		return s.claimOnce(ctx, scope, key, hash, now)
	}
	if err != nil {
		return false, nil, err
	}
	if status != nil {
		resp := &StoredResponse{Status: *status, Body: body, Header: http.Header{}}
		if len(headers) > 0 {
			if err := json.Unmarshal(headers, &resp.Header); err != nil {
				return false, nil, err
			}
		}
		rec.Response = resp
	}
	return false, &rec, nil
}

func (s PGIdempotencyStore) claimOnce(ctx context.Context, scope, key string, hash []byte, now time.Time) (bool, *IdempotencyRecord, error) {
	tag, err := s.DB.Exec(ctx, `
INSERT INTO idempotency_keys (scope, key, request_hash, created_at)
VALUES ($1, $2, $3, $4) ON CONFLICT (scope, key) DO NOTHING`, scope, key, hash, now)
	if err != nil {
		return false, nil, err
	}
	if tag.RowsAffected() == 1 {
		return true, nil, nil
	}
	// Someone else claimed it in between: report it as running.
	return false, &IdempotencyRecord{RequestHash: hash}, nil
}

// Complete implements IdempotencyStore. The claim is identified by its
// created_at, which Claim sets to claimedAt and a takeover moves on by at
// least idempotencyStaleClaim.
func (s PGIdempotencyStore) Complete(ctx context.Context, scope, key string, claimedAt time.Time, resp StoredResponse) error {
	headers, err := json.Marshal(resp.Header)
	if err != nil {
		return err
	}
	tag, err := s.DB.Exec(ctx, `
UPDATE idempotency_keys SET status_code = $4, response_headers = $5, response_body = $6
WHERE scope = $1 AND key = $2 AND created_at = $3 AND status_code IS NULL`,
		scope, key, claimedAt, resp.Status, headers, resp.Body)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrIdempotencyClaimLost
	}
	return nil
}

// Release implements IdempotencyStore.
func (s PGIdempotencyStore) Release(ctx context.Context, scope, key string, claimedAt time.Time) error {
	_, err := s.DB.Exec(ctx, `
DELETE FROM idempotency_keys
WHERE scope = $1 AND key = $2 AND created_at = $3 AND status_code IS NULL`, scope, key, claimedAt)
	return err
}
