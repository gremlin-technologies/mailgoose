// Package auth signs people up and in with emailed one-time codes and keeps
// their web sessions.
//
// Sign up (AUTH-1) creates a pending account and user and emails a six-digit
// code. Using the code activates the account and starts a session. Signing up
// with an address that already has an active account answers the same way and
// emails a sign-in code instead, so the response never says whether an
// account exists.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/argon2"

	mgmail "github.com/gremlin-technologies/mailgoose/internal/mail"
)

const (
	// CodeTTL is how long an emailed code works.
	CodeTTL = 10 * time.Minute
	// MaxCodeAttempts is how many wrong tries a code takes before it stops working.
	MaxCodeAttempts = 5
	// ResendInterval is the shortest gap between two codes to one address.
	// Repeats inside it get the same answer and no new email.
	ResendInterval = time.Minute
	// SessionTTL is how long a web session lasts.
	SessionTTL = 30 * 24 * time.Hour

	maxEmailLen       = 254
	maxAccountNameLen = 100
)

// Purposes of an emailed code.
const (
	PurposeSignup = "signup"
	PurposeSignin = "signin"
)

// Service signs people up and in.
type Service struct {
	DB   Beginner
	Mail mgmail.Sender
	// From is mailgoose's own sending address for codes.
	From mgmail.Address
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	Log *slog.Logger
}

// Beginner starts transactions; *pgxpool.Pool is one.
type Beginner interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}

// ValidationError is a problem with the input, safe to show to the caller.
type ValidationError struct {
	Code    string
	Field   string
	Message string
}

func (e *ValidationError) Error() string { return e.Message }

// ErrInvalidCode covers a wrong, expired, used or locked code, and an address
// with no code; callers can't tell them apart.
var ErrInvalidCode = errors.New("invalid code")

// SignUpInput is what a person gives to sign up.
type SignUpInput struct {
	Email         string
	AccountName   string
	AcceptedTerms bool
}

// Account is an account as the API shows it.
type Account struct {
	ID        string
	Name      string
	Status    string
	Plan      string
	CreatedAt time.Time
}

// User is a person who signs in.
type User struct {
	ID        string
	Email     string
	CreatedAt time.Time
}

// Session is a started web session. Token goes in the cookie and is never
// stored.
type Session struct {
	Token     string
	ExpiresAt time.Time
	Account   Account
	User      User
}

// SessionMeta describes the client starting a session.
type SessionMeta struct {
	UserAgent string
	IP        string
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.New(slog.DiscardHandler)
}

// NormalizeEmail checks an address and trims it. It must be a bare address,
// with no display name.
func NormalizeEmail(raw string) (string, error) {
	email := strings.TrimSpace(raw)
	invalid := &ValidationError{Code: "invalid_email", Field: "email", Message: "Enter a valid email address, such as pat@example.com."}
	if email == "" || len(email) > maxEmailLen || strings.ContainsAny(email, "<> \t\r\n") {
		return "", invalid
	}
	a, err := mail.ParseAddress(email)
	if err != nil || a.Address != email || a.Name != "" || !strings.Contains(email[strings.LastIndex(email, "@")+1:], ".") {
		return "", invalid
	}
	return email, nil
}

func (in SignUpInput) validate() (email, name string, err error) {
	if !in.AcceptedTerms {
		return "", "", &ValidationError{Code: "terms_not_accepted", Field: "accepted_terms",
			Message: "Accept the terms of service and the anti-spam policy to sign up."}
	}
	email, err = NormalizeEmail(in.Email)
	if err != nil {
		return "", "", err
	}
	name = strings.TrimSpace(in.AccountName)
	if name == "" || utf8.RuneCountInString(name) > maxAccountNameLen || strings.ContainsAny(name, "\r\n") {
		return "", "", &ValidationError{Code: "invalid_account_name", Field: "account_name",
			Message: fmt.Sprintf("Account name must be 1 to %d characters on one line.", maxAccountNameLen)}
	}
	return email, name, nil
}

// SignUp creates a pending account and emails a code, or, for an address
// that already has an active account, emails a sign-in code. It returns nil
// in every case the caller should answer "check your email".
func (s *Service) SignUp(ctx context.Context, in SignUpInput) error {
	email, name, err := in.validate()
	if err != nil {
		return err
	}
	now := s.now()

	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		userID, accountID, status string
		lastCode                  *time.Time
		// storedEmail is the address as first signed up; codes go there,
		// whatever case this request used.
		storedEmail = email
	)
	err = tx.QueryRow(ctx, `
		SELECT u.id, u.email, a.id, a.status,
		       (SELECT max(c.created_at) FROM auth_codes c WHERE c.user_id = u.id)
		FROM users u JOIN accounts a ON a.id = u.account_id
		WHERE lower(u.email) = lower($1)
		FOR UPDATE OF u, a`, email).Scan(&userID, &storedEmail, &accountID, &status, &lastCode)
	purpose := PurposeSignup
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if err := tx.QueryRow(ctx, `
			INSERT INTO accounts (name, status, plan, terms_accepted_at)
			VALUES ($1, 'pending', 'free', $2) RETURNING id`, name, now).Scan(&accountID); err != nil {
			return fmt.Errorf("create account: %w", err)
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO users (account_id, email) VALUES ($1, $2) RETURNING id`, accountID, email).Scan(&userID); err != nil {
			if isUniqueViolation(err) {
				// Another sign-up for the same address won the race; it
				// sends the code.
				return nil
			}
			return fmt.Errorf("create user: %w", err)
		}
	case err != nil:
		return fmt.Errorf("find user: %w", err)
	default:
		if lastCode != nil && now.Sub(*lastCode) < ResendInterval {
			return nil
		}
		if status == "active" {
			purpose = PurposeSignin
		} else {
			// Still pending: the latest sign-up's name and acceptance win.
			if _, err := tx.Exec(ctx, `UPDATE accounts SET name = $2, terms_accepted_at = $3, updated_at = $3 WHERE id = $1`,
				accountID, name, now); err != nil {
				return fmt.Errorf("update account: %w", err)
			}
		}
	}

	code, err := newCode()
	if err != nil {
		return err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	// A new code replaces any earlier one.
	if _, err := tx.Exec(ctx, `DELETE FROM auth_codes WHERE user_id = $1 AND used_at IS NULL`, userID); err != nil {
		return fmt.Errorf("clear codes: %w", err)
	}
	var codeID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO auth_codes (user_id, purpose, code_salt, code_hash, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		userID, purpose, salt, hashCode(code, salt), now, now.Add(CodeTTL)).Scan(&codeID); err != nil {
		return fmt.Errorf("store code: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	if _, err := s.Mail.Send(ctx, codeMessage(s.From, storedEmail, code, purpose, accountID)); err != nil {
		// Drop the unsent code so a retry isn't held back by ResendInterval.
		if _, derr := s.deleteCode(context.WithoutCancel(ctx), codeID); derr != nil {
			s.log().ErrorContext(ctx, "drop unsent code", "err", derr)
		}
		return fmt.Errorf("send code: %w", err)
	}
	return nil
}

func (s *Service) deleteCode(ctx context.Context, id string) (int64, error) {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `DELETE FROM auth_codes WHERE id = $1`, id)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), tx.Commit(ctx)
}

// Verify uses an emailed code. On success it activates a pending account and
// starts a session.
func (s *Service) Verify(ctx context.Context, rawEmail, code string, meta SessionMeta) (*Session, error) {
	email, err := NormalizeEmail(rawEmail)
	if err != nil {
		return nil, err
	}
	code = strings.TrimSpace(code)
	if !validCodeFormat(code) {
		return nil, ErrInvalidCode
	}
	now := s.now()

	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		codeID     string
		salt, hash []byte
		attempts   int
		sess       Session
	)
	err = tx.QueryRow(ctx, `
		SELECT c.id, c.code_salt, c.code_hash, c.attempts,
		       u.id, u.email, u.created_at,
		       a.id, a.name, a.status, a.plan, a.created_at
		FROM auth_codes c
		JOIN users u ON u.id = c.user_id
		JOIN accounts a ON a.id = u.account_id
		WHERE lower(u.email) = lower($1) AND c.used_at IS NULL AND c.expires_at > $2
		ORDER BY c.created_at DESC
		LIMIT 1
		FOR UPDATE OF c`, email, now).Scan(&codeID, &salt, &hash, &attempts,
		&sess.User.ID, &sess.User.Email, &sess.User.CreatedAt,
		&sess.Account.ID, &sess.Account.Name, &sess.Account.Status, &sess.Account.Plan, &sess.Account.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// Spend the same time as a real check so the answer's timing doesn't
		// say whether the address has a code.
		hashCode(code, make([]byte, 16))
		return nil, ErrInvalidCode
	}
	if err != nil {
		return nil, fmt.Errorf("find code: %w", err)
	}
	if attempts >= MaxCodeAttempts {
		hashCode(code, salt)
		return nil, ErrInvalidCode
	}
	if subtle.ConstantTimeCompare(hashCode(code, salt), hash) != 1 {
		if _, err := tx.Exec(ctx, `UPDATE auth_codes SET attempts = attempts + 1 WHERE id = $1`, codeID); err != nil {
			return nil, fmt.Errorf("count attempt: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return nil, ErrInvalidCode
	}

	if _, err := tx.Exec(ctx, `UPDATE auth_codes SET used_at = $2 WHERE id = $1`, codeID, now); err != nil {
		return nil, fmt.Errorf("use code: %w", err)
	}
	if sess.Account.Status == "pending" {
		if _, err := tx.Exec(ctx, `UPDATE accounts SET status = 'active', activated_at = $2, updated_at = $2 WHERE id = $1`,
			sess.Account.ID, now); err != nil {
			return nil, fmt.Errorf("activate account: %w", err)
		}
		sess.Account.Status = "active"
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET last_login_at = $2, updated_at = $2 WHERE id = $1`, sess.User.ID, now); err != nil {
		return nil, fmt.Errorf("record sign-in: %w", err)
	}

	token, err := newToken()
	if err != nil {
		return nil, err
	}
	sess.Token = token
	sess.ExpiresAt = now.Add(SessionTTL)
	if _, err := tx.Exec(ctx, `
		INSERT INTO sessions (user_id, token_hash, created_at, expires_at, user_agent, ip)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		sess.User.ID, TokenHash(token), now, sess.ExpiresAt, truncate(meta.UserAgent, 512), truncate(meta.IP, 64)); err != nil {
		return nil, fmt.Errorf("start session: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &sess, nil
}

// TokenHash is how a session token is stored.
func TokenHash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// Argon2id settings for codes: OWASP's minimum (19 MiB, 2 passes). A code
// has only a million values, so a fast hash would fall to brute force if the
// table ever leaked; this makes each guess expensive.
const (
	argonTime    = 2
	argonMemory  = 19 * 1024
	argonThreads = 1
	argonKeyLen  = 32
)

func hashCode(code string, salt []byte) []byte {
	return argon2.IDKey([]byte(code), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
}

func newCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func validCodeFormat(code string) bool {
	if len(code) != 6 {
		return false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
