package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Device login (AUTH-3, MG-10): the OAuth device flow, for `mailgoose login`.
const (
	// DeviceLoginTTL is how long a request can be approved and collected.
	DeviceLoginTTL = 10 * time.Minute
	// DevicePollInterval is how often the CLI may poll.
	DevicePollInterval = 5 * time.Second
	// userCodeLetters has no vowels, so codes don't spell words, and no
	// letters that look like digits.
	userCodeLetters = "BCDFGHJKLMNPQRSTVWXZ"
	userCodeLen     = 8
	maxClientName   = 100
)

// Device login outcomes for PollDeviceLogin and the approval endpoints.
var (
	ErrDeviceLoginPending  = errors.New("device login not approved yet")
	ErrDeviceLoginSlowDown = errors.New("device login polled too fast")
	ErrDeviceLoginDenied   = errors.New("device login denied")
	// ErrDeviceLoginExpired is an expired, unknown or already used device code.
	ErrDeviceLoginExpired = errors.New("device login expired")
	// ErrDeviceLoginNotFound is a user code with no request awaiting approval.
	ErrDeviceLoginNotFound = errors.New("no device login awaiting approval")
)

// DeviceLoginStart is what `mailgoose login` shows and keeps.
type DeviceLoginStart struct {
	// DeviceCode is the CLI's secret for polling; never shown to the person.
	DeviceCode string
	// UserCode is shown in the terminal and typed or confirmed in the browser,
	// as XXXX-XXXX.
	UserCode  string
	ExpiresAt time.Time
	Interval  time.Duration
}

// DeviceLogin is a request awaiting approval, as the approval page shows it.
type DeviceLogin struct {
	UserCode   string
	ClientName string
	CreatedAt  time.Time
	ExpiresAt  time.Time
}

// DeviceLoginToken is an approved request's CLI token, given out once.
type DeviceLoginToken struct {
	Token   *NewKey
	Account Account
	User    User
}

// StartDeviceLogin starts a request. clientName says where it came from
// (such as the computer's name) and is shown on the approval page.
func (s *Service) StartDeviceLogin(ctx context.Context, clientName string) (*DeviceLoginStart, error) {
	clientName = truncate(strings.Join(strings.Fields(clientName), " "), maxClientName)
	now := s.now()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	deviceCode := base64.RawURLEncoding.EncodeToString(b)
	for range 5 {
		userCode, err := newUserCode()
		if err != nil {
			return nil, err
		}
		err = s.inTx(ctx, func(tx pgx.Tx) error {
			// Old requests are only kept long enough to answer "expired".
			if _, err := tx.Exec(ctx, `DELETE FROM device_logins WHERE expires_at < $1`, now.Add(-24*time.Hour)); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `
				INSERT INTO device_logins (device_code_hash, user_code, client_name, created_at, expires_at)
				VALUES ($1, $2, $3, $4, $5)`,
				TokenHash(deviceCode), userCode, clientName, now, now.Add(DeviceLoginTTL))
			return err
		})
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.ConstraintName == "device_logins_pending_user_code" {
			continue // another live request has this user code; pick another
		}
		if err != nil {
			return nil, fmt.Errorf("start device login: %w", err)
		}
		return &DeviceLoginStart{DeviceCode: deviceCode, UserCode: FormatUserCode(userCode),
			ExpiresAt: now.Add(DeviceLoginTTL), Interval: DevicePollInterval}, nil
	}
	return nil, errors.New("start device login: no free user code")
}

// PendingDeviceLogin returns the request awaiting approval under userCode.
func (s *Service) PendingDeviceLogin(ctx context.Context, userCode string) (*DeviceLogin, error) {
	code, ok := NormalizeUserCode(userCode)
	if !ok {
		return nil, ErrDeviceLoginNotFound
	}
	var d DeviceLogin
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT user_code, client_name, created_at, expires_at FROM device_logins
			WHERE user_code = $1 AND status = 'pending' AND expires_at > $2`,
			code, s.now()).Scan(&d.UserCode, &d.ClientName, &d.CreatedAt, &d.ExpiresAt)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrDeviceLoginNotFound
	}
	if err != nil {
		return nil, err
	}
	d.UserCode = FormatUserCode(d.UserCode)
	return &d, nil
}

// DecideDeviceLogin approves or denies the request under userCode for the
// signed-in person userID of accountID.
func (s *Service) DecideDeviceLogin(ctx context.Context, userCode, accountID, userID string, approve bool) error {
	code, ok := NormalizeUserCode(userCode)
	if !ok {
		return ErrDeviceLoginNotFound
	}
	status := "denied"
	if approve {
		status = "approved"
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		now := s.now()
		tag, err := tx.Exec(ctx, `
			UPDATE device_logins SET status = $2, account_id = $3, user_id = $4, decided_at = $5
			WHERE user_code = $1 AND status = 'pending' AND expires_at > $5`,
			code, status, accountID, userID, now)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrDeviceLoginNotFound
		}
		return nil
	})
}

// PollDeviceLogin is the CLI's poll. It returns the CLI token once the
// request is approved, and only once; otherwise one of the ErrDeviceLogin
// errors.
func (s *Service) PollDeviceLogin(ctx context.Context, deviceCode string) (*DeviceLoginToken, error) {
	var out *DeviceLoginToken
	var outcome error
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		now := s.now()
		var (
			id, status, clientName string
			accountID, userID      *string
			expiresAt              time.Time
			lastPolled             *time.Time
		)
		err := tx.QueryRow(ctx, `
			SELECT id, status, client_name, account_id, user_id, expires_at, last_polled_at
			FROM device_logins WHERE device_code_hash = $1 FOR UPDATE`,
			TokenHash(deviceCode)).Scan(&id, &status, &clientName, &accountID, &userID, &expiresAt, &lastPolled)
		if errors.Is(err, pgx.ErrNoRows) {
			outcome = ErrDeviceLoginExpired
			return nil
		}
		if err != nil {
			return err
		}
		switch {
		case status == "done" || !now.Before(expiresAt):
			outcome = ErrDeviceLoginExpired
			return nil
		case status == "denied":
			outcome = ErrDeviceLoginDenied
			return nil
		case lastPolled != nil && now.Sub(*lastPolled) < DevicePollInterval-time.Second:
			outcome = ErrDeviceLoginSlowDown
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE device_logins SET last_polled_at = $2 WHERE id = $1`, id, now); err != nil {
			return err
		}
		if status == "pending" {
			outcome = ErrDeviceLoginPending
			return nil
		}

		// Approved: give out the token, once.
		t := &DeviceLoginToken{}
		var suspendedAt *time.Time
		var suspendedReason *string
		if err := tx.QueryRow(ctx, `
			SELECT a.id, a.name, a.status, a.plan, a.created_at, a.suspended_at, a.suspended_reason, coalesce(a.review = 'waiting', false),
			       a.sending_mode, u.id, u.email, u.created_at
			FROM accounts a JOIN users u ON u.account_id = a.id
			WHERE a.id = $1 AND u.id = $2`, *accountID, *userID).Scan(
			&t.Account.ID, &t.Account.Name, &t.Account.Status, &t.Account.Plan, &t.Account.CreatedAt,
			&suspendedAt, &suspendedReason, &t.Account.InReview, &t.Account.SendingMode, &t.User.ID, &t.User.Email, &t.User.CreatedAt); err != nil {
			return fmt.Errorf("read approver: %w", err)
		}
		// A suspended account gets no CLI login (MG-65).
		if suspendedAt != nil {
			if _, err := tx.Exec(ctx, `UPDATE device_logins SET status = 'denied' WHERE id = $1`, id); err != nil {
				return err
			}
			outcome = ErrDeviceLoginDenied
			return nil
		}
		name := clientName
		if name == "" {
			name = "CLI login"
		}
		k, err := s.insertSecret(ctx, tx, t.Account.ID, t.User.ID, KindCLIToken, name, ScopeFull, prefixCLI)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE device_logins SET status = 'done', key_id = $2 WHERE id = $1`, id, k.ID); err != nil {
			return err
		}
		t.Token = k
		out = t
		return nil
	})
	if err != nil {
		return nil, err
	}
	if outcome != nil {
		return nil, outcome
	}
	return out, nil
}

// RevokeCLIToken revokes the CLI token keyID of accountID, for `mailgoose
// logout`.
func (s *Service) RevokeCLIToken(ctx context.Context, accountID, keyID string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE api_keys SET revoked_at = $3
			WHERE id = $1 AND account_id = $2 AND kind = 'cli_token' AND revoked_at IS NULL`,
			keyID, accountID, s.now())
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrKeyNotFound
		}
		return nil
	})
}

func newUserCode() (string, error) {
	var b strings.Builder
	n := big.NewInt(int64(len(userCodeLetters)))
	for range userCodeLen {
		i, err := rand.Int(rand.Reader, n)
		if err != nil {
			return "", err
		}
		b.WriteByte(userCodeLetters[i.Int64()])
	}
	return b.String(), nil
}

// NormalizeUserCode accepts a user code typed with any case, spaces or
// dashes, and returns it in stored form.
func NormalizeUserCode(raw string) (string, bool) {
	var b strings.Builder
	for _, r := range strings.ToUpper(raw) {
		switch {
		case r == '-' || r == ' ':
		case strings.ContainsRune(userCodeLetters, r):
			b.WriteRune(r)
		default:
			return "", false
		}
	}
	if b.Len() != userCodeLen {
		return "", false
	}
	return b.String(), true
}

// FormatUserCode shows a stored user code as XXXX-XXXX.
func FormatUserCode(code string) string {
	if len(code) != userCodeLen {
		return code
	}
	return code[:4] + "-" + code[4:]
}

// Identity returns accountID's account and, when userID isn't empty and
// still exists, the person, for whoami.
func (s *Service) Identity(ctx context.Context, accountID, userID string) (*Account, *User, error) {
	var a Account
	var u *User
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var suspendedAt *time.Time
		var suspendedReason *string
		if err := tx.QueryRow(ctx, `SELECT id, name, status, plan, created_at, suspended_at, suspended_reason,
			coalesce(review = 'waiting', false), sending_mode FROM accounts WHERE id = $1`, accountID).
			Scan(&a.ID, &a.Name, &a.Status, &a.Plan, &a.CreatedAt, &suspendedAt, &suspendedReason, &a.InReview, &a.SendingMode); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNoAccount
			}
			return err
		}
		a.Suspension = suspension(suspendedAt, suspendedReason)
		if userID == "" {
			return nil
		}
		var v User
		err := tx.QueryRow(ctx, `SELECT id, email, created_at FROM users WHERE id = $1 AND account_id = $2`, userID, accountID).
			Scan(&v.ID, &v.Email, &v.CreatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		u = &v
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return &a, u, nil
}
