package auth

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Scopes an API key or CLI token can have (AUTH-4).
const (
	// ScopeFull can do everything.
	ScopeFull = "full"
	// ScopeDraft can do everything except launch campaigns, change billing
	// and manage keys.
	ScopeDraft = "draft"
)

// Kinds of bearer secret.
const (
	KindAPIKey   = "api_key"
	KindCLIToken = "cli_token"
)

const (
	// MaxKeysPerAccount caps active API keys per account.
	MaxKeysPerAccount = 50
	maxKeyNameLen     = 100
	// lastUsedEvery is how stale last_used_at may get before a request
	// updates it, so a busy key isn't a write on every request.
	lastUsedEvery = time.Minute
)

// Secret prefixes. Live keys come from production; everything else (staging,
// local) makes test keys, so a key's environment shows at a glance.
const (
	prefixLive = "mg_live_"
	prefixTest = "mg_test_"
	prefixCLI  = "mg_cli_"
)

// Key is an API key as listed. The secret itself is never stored.
type Key struct {
	ID    string
	Name  string
	Scope string
	// Hint is the secret's prefix and last four characters, such as
	// "mg_live_…k3x9", so people can tell keys apart.
	Hint       string
	CreatedAt  time.Time
	LastUsedAt *time.Time
}

// NewKey is a key just created, with its secret, which is shown only now.
type NewKey struct {
	Key
	Secret string
}

// Principal is whoever a bearer secret belongs to.
type Principal struct {
	KeyID string
	Kind  string
	Scope string
	// UserID is the person who created the key or signed in the CLI; empty
	// if they were removed.
	UserID  string
	Account Account
}

// ErrInvalidKey covers an unknown, malformed or revoked secret.
var ErrInvalidKey = errors.New("invalid API key or token")

// ErrKeyNotFound is a key that doesn't exist, is revoked, or belongs to
// another account.
var ErrKeyNotFound = errors.New("API key not found")

// CreateKey makes an API key for accountID. createdBy is the user making it.
func (s *Service) CreateKey(ctx context.Context, accountID, createdBy, name, scope string) (*NewKey, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > maxKeyNameLen {
		return nil, &ValidationError{Code: "invalid_name", Field: "name", Message: fmt.Sprintf("Give the key a name of 1 to %d characters.", maxKeyNameLen)}
	}
	if scope != ScopeFull && scope != ScopeDraft {
		return nil, &ValidationError{Code: "invalid_scope", Field: "scope", Message: "Scope must be full or draft."}
	}
	prefix := prefixTest
	if s.LiveKeys {
		prefix = prefixLive
	}

	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Lock the account row so two creates can't both pass the cap.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM accounts WHERE id = $1 FOR UPDATE`, accountID); err != nil {
		return nil, fmt.Errorf("lock account: %w", err)
	}
	var active int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM api_keys WHERE account_id = $1 AND kind = $2 AND revoked_at IS NULL`,
		accountID, KindAPIKey).Scan(&active); err != nil {
		return nil, fmt.Errorf("count keys: %w", err)
	}
	if active >= MaxKeysPerAccount {
		return nil, &ValidationError{Code: "too_many_keys", Message: fmt.Sprintf("An account can have %d API keys. Revoke one you don't use first.", MaxKeysPerAccount)}
	}
	k, err := s.insertSecret(ctx, tx, accountID, createdBy, KindAPIKey, name, scope, prefix)
	if err != nil {
		return nil, err
	}
	return k, tx.Commit(ctx)
}

// CreateCLIToken makes a full-scope bearer token for the CLI, belonging to
// userID. `mailgoose login` (MG-10) is the only caller.
func (s *Service) CreateCLIToken(ctx context.Context, accountID, userID, name string) (*NewKey, error) {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	k, err := s.insertSecret(ctx, tx, accountID, userID, KindCLIToken, truncate(strings.TrimSpace(name), maxKeyNameLen), ScopeFull, prefixCLI)
	if err != nil {
		return nil, err
	}
	return k, tx.Commit(ctx)
}

func (s *Service) insertSecret(ctx context.Context, tx pgx.Tx, accountID, userID, kind, name, scope, prefix string) (*NewKey, error) {
	secret, err := newSecret(prefix)
	if err != nil {
		return nil, err
	}
	k := &NewKey{Secret: secret, Key: Key{Name: name, Scope: scope, Hint: hint(secret)}}
	var createdBy *string
	if userID != "" {
		createdBy = &userID
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO api_keys (account_id, created_by, kind, name, scope, secret_hash, secret_hint, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, created_at`,
		accountID, createdBy, kind, name, scope, TokenHash(secret), k.Hint, s.now()).Scan(&k.ID, &k.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("insert key: %w", err)
	}
	return k, nil
}

// ListKeys returns up to limit active API keys of accountID, oldest first,
// after the key with ID afterID ("" for the first page).
func (s *Service) ListKeys(ctx context.Context, accountID, afterID string, limit int) ([]Key, error) {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var after *string
	if afterID != "" {
		after = &afterID
	}
	rows, err := tx.Query(ctx, `
		SELECT k.id, k.name, k.scope, k.secret_hint, k.created_at, k.last_used_at
		FROM api_keys k
		WHERE k.account_id = $1 AND k.kind = $2 AND k.revoked_at IS NULL
		  AND ($3::uuid IS NULL OR (k.created_at, k.id) > (
		      SELECT a.created_at, a.id FROM api_keys a WHERE a.id = $3::uuid AND a.account_id = $1))
		ORDER BY k.created_at, k.id
		LIMIT $4`, accountID, KindAPIKey, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list keys: %w", err)
	}
	keys, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Key, error) {
		var k Key
		err := row.Scan(&k.ID, &k.Name, &k.Scope, &k.Hint, &k.CreatedAt, &k.LastUsedAt)
		return k, err
	})
	if err != nil {
		return nil, fmt.Errorf("list keys: %w", err)
	}
	return keys, nil
}

// RevokeKey ends an API key of accountID. It stops working on the next request.
func (s *Service) RevokeKey(ctx context.Context, accountID, keyID string) error {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
		UPDATE api_keys SET revoked_at = $3
		WHERE id = $1 AND account_id = $2 AND kind = 'api_key' AND revoked_at IS NULL`,
		keyID, accountID, s.now())
	if err != nil {
		return fmt.Errorf("revoke key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrKeyNotFound
	}
	return tx.Commit(ctx)
}

// Authenticate finds who a bearer secret belongs to, and notes that it was
// used.
func (s *Service) Authenticate(ctx context.Context, secret string) (*Principal, error) {
	if !strings.HasPrefix(secret, prefixLive) && !strings.HasPrefix(secret, prefixTest) && !strings.HasPrefix(secret, prefixCLI) {
		return nil, ErrInvalidKey
	}
	now := s.now()
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var (
		p               Principal
		userID          *string
		lastUsed        *time.Time
		suspendedAt     *time.Time
		suspendedReason *string
	)
	err = tx.QueryRow(ctx, `
		SELECT k.id, k.kind, k.scope, k.created_by, k.last_used_at,
		       a.id, a.name, a.status, a.plan, a.created_at, a.suspended_at, a.suspended_reason, coalesce(a.review = 'waiting', false),
		       a.sending_mode
		FROM api_keys k JOIN accounts a ON a.id = k.account_id
		WHERE k.secret_hash = $1 AND k.revoked_at IS NULL`, TokenHash(secret)).
		Scan(&p.KeyID, &p.Kind, &p.Scope, &userID, &lastUsed,
			&p.Account.ID, &p.Account.Name, &p.Account.Status, &p.Account.Plan, &p.Account.CreatedAt,
			&suspendedAt, &suspendedReason, &p.Account.InReview, &p.Account.SendingMode)
	p.Account.Suspension = suspension(suspendedAt, suspendedReason)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInvalidKey
	}
	if err != nil {
		return nil, fmt.Errorf("find key: %w", err)
	}
	if userID != nil {
		p.UserID = *userID
	}
	if lastUsed == nil || now.Sub(*lastUsed) >= lastUsedEvery {
		if _, err := tx.Exec(ctx, `UPDATE api_keys SET last_used_at = $2 WHERE id = $1`, p.KeyID, now); err != nil {
			return nil, fmt.Errorf("note key use: %w", err)
		}
	}
	return &p, tx.Commit(ctx)
}

// newSecret is prefix plus 160 random bits in lowercase base32.
func newSecret(prefix string) (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)), nil
}

func hint(secret string) string {
	i := strings.LastIndexByte(secret, '_') + 1
	return secret[:i] + "…" + secret[len(secret)-4:]
}
