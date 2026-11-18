package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Limits on requests for emailed codes, counted over CodeRequestWindow
// across sign-up and sign-in, and across API tasks (they live in Postgres).
const (
	CodeRequestWindow       = time.Hour
	MaxCodeRequestsPerEmail = 5
	MaxCodeRequestsPerIP    = 20
	codeRequestRetention    = 24 * time.Hour
)

// RateLimitError says a code request went over a limit.
type RateLimitError struct {
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string { return "too many code requests" }

// limitCodeRequests records a code request and refuses it when the address
// or the IP has made too many in the last CodeRequestWindow. It counts the
// address whether or not it has an account, so the answer reveals nothing.
func (s *Service) limitCodeRequests(ctx context.Context, email, ip string, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	key := strings.ToLower(email)
	since := now.Add(-CodeRequestWindow)
	var perEmail, perIP int
	var oldest *time.Time
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE email_lower = $1),
		       count(*) FILTER (WHERE ip = $2 AND $2 <> ''),
		       min(created_at)
		FROM auth_code_requests
		WHERE created_at > $3 AND (email_lower = $1 OR ($2 <> '' AND ip = $2))`,
		key, ip, since).Scan(&perEmail, &perIP, &oldest); err != nil {
		return fmt.Errorf("count code requests: %w", err)
	}
	if perEmail >= MaxCodeRequestsPerEmail || perIP >= MaxCodeRequestsPerIP {
		retry := CodeRequestWindow
		if oldest != nil {
			retry = oldest.Add(CodeRequestWindow).Sub(now)
		}
		return &RateLimitError{RetryAfter: max(retry, time.Second)}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO auth_code_requests (email_lower, ip, created_at) VALUES ($1, $2, $3)`,
		key, ip, now); err != nil {
		return fmt.Errorf("record code request: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM auth_code_requests WHERE created_at < $1`, now.Add(-codeRequestRetention)); err != nil {
		return fmt.Errorf("prune code requests: %w", err)
	}
	return tx.Commit(ctx)
}

// RequestSignIn emails a sign-in code to an address with an account. A
// pending account gets its sign-up code again instead. An address with no
// account gets nothing, and the caller answers the same way in every case.
func (s *Service) RequestSignIn(ctx context.Context, rawEmail, ip string) error {
	email, err := NormalizeEmail(rawEmail)
	if err != nil {
		return err
	}
	now := s.now()
	if err := s.limitCodeRequests(ctx, email, ip, now); err != nil {
		return err
	}

	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		userID, accountID, storedEmail, status string
		lastCode                               *time.Time
	)
	err = tx.QueryRow(ctx, `
		SELECT u.id, u.email, a.id, a.status,
		       (SELECT max(c.created_at) FROM auth_codes c WHERE c.user_id = u.id)
		FROM users u JOIN accounts a ON a.id = u.account_id
		WHERE lower(u.email) = lower($1)
		FOR UPDATE OF u`, email).Scan(&userID, &storedEmail, &accountID, &status, &lastCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("find user: %w", err)
	}
	if lastCode != nil && now.Sub(*lastCode) < ResendInterval {
		return nil
	}
	purpose := PurposeSignin
	if status != "active" {
		purpose = PurposeSignup
	}
	return s.issueCode(ctx, tx, userID, accountID, storedEmail, purpose, now)
}

// Current is the signed-in person behind a session.
type Current struct {
	SessionID string
	ExpiresAt time.Time
	Account   Account
	User      User
}

// ErrNoSession means the token doesn't name a live session.
var ErrNoSession = errors.New("no session")

// SessionByToken looks up a live session from its cookie token.
func (s *Service) SessionByToken(ctx context.Context, token string) (*Current, error) {
	if token == "" {
		return nil, ErrNoSession
	}
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var c Current
	err = tx.QueryRow(ctx, `
		SELECT s.id, s.expires_at,
		       u.id, u.email, u.created_at,
		       a.id, a.name, a.status, a.plan, a.created_at
		FROM sessions s
		JOIN users u ON u.id = s.user_id
		JOIN accounts a ON a.id = u.account_id
		WHERE s.token_hash = $1 AND s.revoked_at IS NULL AND s.expires_at > $2`,
		TokenHash(token), s.now()).Scan(&c.SessionID, &c.ExpiresAt,
		&c.User.ID, &c.User.Email, &c.User.CreatedAt,
		&c.Account.ID, &c.Account.Name, &c.Account.Status, &c.Account.Plan, &c.Account.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoSession
	}
	if err != nil {
		return nil, fmt.Errorf("find session: %w", err)
	}
	return &c, nil
}

// SignOut ends one session.
func (s *Service) SignOut(ctx context.Context, sessionID string) error {
	return s.revoke(ctx, `UPDATE sessions SET revoked_at = $2 WHERE id = $1 AND revoked_at IS NULL`, sessionID)
}

// SignOutEverywhere ends every session of a user.
func (s *Service) SignOutEverywhere(ctx context.Context, userID string) error {
	return s.revoke(ctx, `UPDATE sessions SET revoked_at = $2 WHERE user_id = $1 AND revoked_at IS NULL`, userID)
}

func (s *Service) revoke(ctx context.Context, sql, id string) error {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, sql, id, s.now()); err != nil {
		return fmt.Errorf("revoke sessions: %w", err)
	}
	return tx.Commit(ctx)
}
