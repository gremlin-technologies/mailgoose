// Package testmode keeps an account's test addresses and says who an
// account may send to (MG-80).
//
// An account in test mode sends only to its owner's own sign-in address and
// to its confirmed test addresses: launches, test sends and form signups for
// anyone else are refused, and the send pipeline skips any copy for anyone
// else. An account keeps up to MaxAddresses test addresses, pending ones
// included. Adding one emails it a link; it counts once the link is used.
package testmode

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/auth"
	"github.com/gremlin-technologies/mailgoose/internal/contacts"
	"github.com/gremlin-technologies/mailgoose/internal/mail"
)

const (
	// MaxAddresses is how many test addresses an account keeps, pending
	// ones included.
	MaxAddresses = 5
	// ConfirmTTL is how long a confirm link works after it's sent.
	ConfirmTTL = 7 * 24 * time.Hour
	// ResendGap is the least time between two confirm emails to one
	// address.
	ResendGap = time.Minute
	// ConfirmPath is the web app's page a confirm link opens, with the
	// token in its query: ConfirmPath + "?token=" + token. The page calls
	// the API to confirm, so mail scanners that open links confirm nothing.
	ConfirmPath = "/test-addresses/confirm"
	// tokenPrefix starts every confirm token, so its format can change.
	tokenPrefix = "t1"
)

// Statuses of a test address.
const (
	StatusPending   = "pending"
	StatusConfirmed = "confirmed"
)

var (
	// ErrNotFound is no test address with that id in the account.
	ErrNotFound = errors.New("test address not found")
	// ErrExists is an address the account has already.
	ErrExists = errors.New("the account has that test address already")
	// ErrOwnerAddress is the owner's own sign-in address, which is always
	// allowed and needs no slot.
	ErrOwnerAddress = errors.New("the owner's own address is always allowed")
	// ErrConfirmed is resending the link of an address that's confirmed.
	ErrConfirmed = errors.New("the test address is confirmed already")
	// ErrBadToken is a confirm token that isn't one, or was cut short.
	ErrBadToken = errors.New("confirm link not valid")
	// ErrTokenNotFound is a confirm token for no address: it was removed,
	// or a newer link replaced this one.
	ErrTokenNotFound = errors.New("no test address waits for that link")
	// ErrExpired is a confirm link older than ConfirmTTL.
	ErrExpired = errors.New("confirm link expired")
)

// LimitError is an account that has MaxAddresses test addresses already.
type LimitError struct{ Count int }

func (e *LimitError) Error() string {
	return fmt.Sprintf("the account has %d test addresses, the most it can have", e.Count)
}

// InvalidError is an address that isn't one.
type InvalidError struct{ Message string }

func (e *InvalidError) Error() string { return e.Message }

// TooSoonError is a resend within ResendGap of the last email.
type TooSoonError struct{ RetryAfter time.Duration }

func (e *TooSoonError) Error() string {
	return "a confirm email went to this address less than a minute ago"
}

// SendError is a confirm email the mail provider refused or didn't answer.
// The address is kept, pending; resending tries again.
type SendError struct{ Err error }

func (e *SendError) Error() string { return "send the confirm email: " + e.Err.Error() }
func (e *SendError) Unwrap() error { return e.Err }

// Address is one test address.
type Address struct {
	ID     string `json:"id"`
	Email  string `json:"email"`
	Status string `json:"status"`
	// ConfirmSentAt is when the latest confirm email went; ConfirmExpiresAt
	// when its link stops working. Both nil once confirmed.
	ConfirmSentAt    *time.Time `json:"confirm_sent_at"`
	ConfirmExpiresAt *time.Time `json:"confirm_expires_at"`
	ConfirmedAt      *time.Time `json:"confirmed_at"`
	CreatedAt        time.Time  `json:"created_at"`
}

// Service keeps test addresses and sends their confirm emails.
type Service struct {
	DB   *pgxpool.Pool
	Mail mail.Sender
	// From is mailgoose's own system sender, as for sign-in codes.
	From mail.Address
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

const columns = `id, email, confirm_sent_at, confirm_expires_at, confirmed_at, created_at`

func scan(row pgx.Row) (Address, error) {
	var a Address
	if err := row.Scan(&a.ID, &a.Email, &a.ConfirmSentAt, &a.ConfirmExpiresAt, &a.ConfirmedAt, &a.CreatedAt); err != nil {
		return a, err
	}
	a.Status = StatusPending
	if a.ConfirmedAt != nil {
		a.Status = StatusConfirmed
		a.ConfirmSentAt, a.ConfirmExpiresAt = nil, nil
	}
	return a, nil
}

// List is the account's test addresses, oldest first.
func (s *Service) List(ctx context.Context, accountID string) ([]Address, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+columns+` FROM test_addresses WHERE account_id = $1 ORDER BY created_at, id`, accountID)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Address, error) { return scan(row) })
	if out == nil {
		out = []Address{}
	}
	return out, err
}

// Get is one of the account's test addresses.
func (s *Service) Get(ctx context.Context, accountID, id string) (*Address, error) {
	a, err := scan(s.DB.QueryRow(ctx, `SELECT `+columns+` FROM test_addresses WHERE account_id = $1 AND id = $2`, accountID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// Add adds a test address and emails it a confirm link. baseURL is the web
// app's public URL, for the link.
func (s *Service) Add(ctx context.Context, accountID, rawEmail, baseURL string) (*Address, error) {
	email, err := contacts.NormalizeEmail(rawEmail)
	if err != nil {
		return nil, &InvalidError{Message: fmt.Sprintf("%q isn't an email address.", strings.TrimSpace(rawEmail))}
	}
	now := s.now()
	token, err := newToken()
	if err != nil {
		return nil, err
	}
	var id, accountName string
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		// The account row is locked, so two adds at once can't both take
		// the last slot.
		if err := tx.QueryRow(ctx, `SELECT name FROM accounts WHERE id = $1 FOR UPDATE`, accountID).Scan(&accountName); err != nil {
			return err
		}
		var owner bool
		var n int
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM users WHERE account_id = $1 AND lower(email) = $2),
			       (SELECT count(*) FROM test_addresses WHERE account_id = $1)`, accountID, email).Scan(&owner, &n); err != nil {
			return err
		}
		if owner {
			return ErrOwnerAddress
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM test_addresses WHERE account_id = $1 AND lower(email) = $2)`,
			accountID, email).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return ErrExists
		}
		if n >= MaxAddresses {
			return &LimitError{Count: n}
		}
		err := tx.QueryRow(ctx, `
			INSERT INTO test_addresses (account_id, email, confirm_token_hash, confirm_expires_at, confirm_sent_at, created_at)
			VALUES ($1, $2, $3, $4, $5, $5) RETURNING id`,
			accountID, email, hashToken(token), now.Add(ConfirmTTL), now).Scan(&id)
		if isUniqueViolation(err) {
			return ErrExists
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := s.send(ctx, accountID, accountName, id, email, token, baseURL); err != nil {
		return nil, err
	}
	return s.Get(ctx, accountID, id)
}

// Resend emails a pending address a new confirm link; the old one stops
// working. At most one a minute per address.
func (s *Service) Resend(ctx context.Context, accountID, id, baseURL string) (*Address, error) {
	now := s.now()
	token, err := newToken()
	if err != nil {
		return nil, err
	}
	var email, accountName string
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		var confirmed, sent *time.Time
		err := tx.QueryRow(ctx, `
			SELECT t.email, t.confirmed_at, t.confirm_sent_at, a.name
			FROM test_addresses t JOIN accounts a ON a.id = t.account_id
			WHERE t.account_id = $1 AND t.id = $2 FOR UPDATE OF t`, accountID, id).Scan(&email, &confirmed, &sent, &accountName)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if confirmed != nil {
			return ErrConfirmed
		}
		if sent != nil && now.Sub(*sent) < ResendGap {
			return &TooSoonError{RetryAfter: sent.Add(ResendGap).Sub(now)}
		}
		_, err = tx.Exec(ctx, `
			UPDATE test_addresses SET confirm_token_hash = $3, confirm_expires_at = $4, confirm_sent_at = $5
			WHERE account_id = $1 AND id = $2`, accountID, id, hashToken(token), now.Add(ConfirmTTL), now)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := s.send(ctx, accountID, accountName, id, email, token, baseURL); err != nil {
		return nil, err
	}
	return s.Get(ctx, accountID, id)
}

// Remove removes a test address, freeing its slot. Its link stops working.
func (s *Service) Remove(ctx context.Context, accountID, id string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM test_addresses WHERE account_id = $1 AND id = $2`, accountID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Confirmed is what a used confirm link confirmed.
type Confirmed struct {
	Email       string
	AccountName string
	ConfirmedAt time.Time
}

// Confirm uses a confirm link's token: the address counts from now on.
// Using it again changes nothing and answers the same.
func (s *Service) Confirm(ctx context.Context, token string) (*Confirmed, error) {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, tokenPrefix) || len(token) != len(tokenPrefix)+43 {
		return nil, ErrBadToken
	}
	var out Confirmed
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var id string
		var expires, confirmed *time.Time
		err := tx.QueryRow(ctx, `
			SELECT t.id, t.email, a.name, t.confirm_expires_at, t.confirmed_at
			FROM test_addresses t JOIN accounts a ON a.id = t.account_id
			WHERE t.confirm_token_hash = $1 AND a.status = 'active'
			FOR UPDATE OF t`, hashToken(token)).Scan(&id, &out.Email, &out.AccountName, &expires, &confirmed)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrTokenNotFound
		}
		if err != nil {
			return err
		}
		if confirmed != nil {
			out.ConfirmedAt = *confirmed
			return nil
		}
		now := s.now()
		if expires == nil || !now.Before(*expires) {
			return ErrExpired
		}
		out.ConfirmedAt = now
		_, err = tx.Exec(ctx, `UPDATE test_addresses SET confirmed_at = $2 WHERE id = $1`, id, now)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// send emails the confirm link. When it can't, the send isn't recorded,
// so a resend may go at once, and a *SendError is returned.
func (s *Service) send(ctx context.Context, accountID, accountName, id, to, token, baseURL string) error {
	link := strings.TrimRight(baseURL, "/") + ConfirmPath + "?token=" + token
	if _, err := s.Mail.Send(ctx, confirmMessage(s.From, accountID, accountName, to, link)); err != nil {
		if _, cerr := s.DB.Exec(context.WithoutCancel(ctx), `UPDATE test_addresses SET confirm_sent_at = NULL WHERE id = $1`, id); cerr != nil {
			err = errors.Join(err, cerr)
		}
		return &SendError{Err: err}
	}
	return nil
}

var confirmHTML = template.Must(template.New("confirm").Parse(`<!doctype html>
<html><body style="font-family:-apple-system,Segoe UI,Helvetica,Arial,sans-serif;color:#1a1a1a">
<p>{{.Account}} added this address as a test address on mailgoose, so it can get their test emails while their account is in test mode.</p>
<p><a href="{{.URL}}">Confirm this test address</a></p>
<p style="color:#555">Or open this link: {{.URL}}</p>
<p style="color:#555">The link works for 7 days. If you don't know {{.Account}}, ignore this email: nothing will be sent to you.</p>
</body></html>
`))

// confirmMessage is the email that asks an address to confirm.
func confirmMessage(from mail.Address, accountID, accountName, to, link string) mail.Message {
	var b strings.Builder
	_ = confirmHTML.Execute(&b, map[string]string{"Account": accountName, "URL": link})
	text := fmt.Sprintf(`%s added this address as a test address on mailgoose, so it can get their test emails while their account is in test mode.

Confirm it:
%s

The link works for 7 days. If you don't know %s, ignore this email: nothing will be sent to you.
`, accountName, link, accountName)
	return mail.Message{
		From:      from,
		To:        mail.Address{Email: to},
		Subject:   "Confirm your test address for " + accountName,
		HTML:      b.String(),
		Text:      text,
		Kind:      mail.KindSystem,
		AccountID: accountID,
	}
}

func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return tokenPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte("mailgoose-test-address\x00" + token))
	return sum[:]
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (s *Service) inTx(ctx context.Context, f func(pgx.Tx) error) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := f(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Querier is a pool or a transaction.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Gate is who an account may send to now.
type Gate struct {
	// Mode is auth.SendingTest or auth.SendingFull.
	Mode string
	// allowed holds, in test mode, the owner's address and the confirmed
	// test addresses, lower-cased.
	allowed map[string]bool
}

// Test reports whether the account is in test mode.
func (g *Gate) Test() bool { return g.Mode == auth.SendingTest }

// Allows reports whether the account may send to email now.
func (g *Gate) Allows(email string) bool {
	return !g.Test() || g.allowed[strings.ToLower(strings.TrimSpace(email))]
}

// Allowed is the addresses a test-mode account may send to, lower-cased
// and sorted; empty with full sending.
func (g *Gate) Allowed() []string {
	out := make([]string, 0, len(g.allowed))
	for a := range g.allowed {
		out = append(out, a)
	}
	slices.Sort(out)
	return out
}

// Load reads the account's mode and, in test mode, who it may send to.
func Load(ctx context.Context, q Querier, accountID string) (*Gate, error) {
	g := &Gate{}
	if err := q.QueryRow(ctx, `SELECT sending_mode FROM accounts WHERE id = $1`, accountID).Scan(&g.Mode); err != nil {
		return nil, err
	}
	if !g.Test() {
		return g, nil
	}
	rows, err := q.Query(ctx, `
		SELECT lower(email) FROM users WHERE account_id = $1
		UNION SELECT lower(email) FROM test_addresses WHERE account_id = $1 AND confirmed_at IS NOT NULL`, accountID)
	if err != nil {
		return nil, err
	}
	emails, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	g.allowed = make(map[string]bool, len(emails))
	for _, e := range emails {
		g.allowed[e] = true
	}
	return g, nil
}

// Allows reports whether the account may send to email now: always with
// full sending; in test mode only to its owner's address or a confirmed
// test address. It's one query, for the send pipeline's every copy.
func Allows(ctx context.Context, q Querier, accountID, email string) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `
		SELECT a.sending_mode <> 'test'
			OR EXISTS (SELECT 1 FROM users u WHERE u.account_id = a.id AND lower(u.email) = lower($2))
			OR EXISTS (SELECT 1 FROM test_addresses t WHERE t.account_id = a.id AND lower(t.email) = lower($2) AND t.confirmed_at IS NOT NULL)
		FROM accounts a WHERE a.id = $1`, accountID, strings.TrimSpace(email)).Scan(&ok)
	return ok, err
}
