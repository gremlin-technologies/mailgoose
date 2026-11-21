// Package senders keeps an account's sender addresses and works out who a
// customer's email really goes from (MG-85).
//
// A custom domain is optional. An account that hasn't verified a domain may
// still send, from mailgoose's shared sending domain (SHARED_SENDING_DOMAIN),
// when its From address is one it has proved it owns: the owner's own
// sign-in address, or a sender address confirmed by a link emailed to it.
// The message then goes from <the account's sending slug>@<shared domain>,
// with replies to their own address. Resolver does that, for launches, test
// sends, signup confirmations and the email check alike.
//
// An account keeps up to MaxAddresses sender addresses, pending ones
// included. Adding one emails it a link; it counts once the link is used.
// The confirm emails have the same limits as test addresses' (MG-80).
package senders

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/contacts"
	"github.com/gremlin-technologies/mailgoose/internal/mail"
	"github.com/gremlin-technologies/mailgoose/internal/testmode"
)

const (
	// MaxAddresses is how many sender addresses an account keeps, pending
	// ones included.
	MaxAddresses = 10
	// ConfirmTTL is how long a confirm link works after it's sent, as for
	// test addresses.
	ConfirmTTL = testmode.ConfirmTTL
	// ResendGap is the least time between two confirm emails to one
	// address.
	ResendGap = testmode.ResendGap
	// DailyEmails is how many sender address confirm emails an account may
	// send in any EmailWindow, adds and resends alike. Removing an address
	// doesn't give any back, so adding and removing in a loop can't mail
	// strangers.
	DailyEmails = testmode.DailyEmails
	// EmailWindow is the window DailyEmails counts over.
	EmailWindow = testmode.EmailWindow
	// ConfirmPath is the web app's page a confirm link opens, with the
	// token in its query: ConfirmPath + "?token=" + token. The page calls
	// the API to confirm, so mail scanners that open links confirm nothing.
	ConfirmPath = "/sender-addresses/confirm"
	// tokenPrefix starts every confirm token, so its format can change and
	// it can't be mistaken for a test address's.
	tokenPrefix = "s1"
)

// Statuses of a sender address.
const (
	StatusPending   = "pending"
	StatusConfirmed = "confirmed"
)

var (
	// ErrNotFound is no sender address with that id in the account.
	ErrNotFound = errors.New("sender address not found")
	// ErrExists is an address the account has already.
	ErrExists = errors.New("the account has that sender address already")
	// ErrOwnerAddress is the owner's own sign-in address, which is always
	// verified and needs no slot.
	ErrOwnerAddress = errors.New("the owner's own address is always verified")
	// ErrConfirmed is resending the link of an address that's confirmed.
	ErrConfirmed = errors.New("the sender address is confirmed already")
	// ErrBadToken is a confirm token that isn't one, or was cut short.
	ErrBadToken = errors.New("confirm link not valid")
	// ErrTokenNotFound is a confirm token for no address: it was removed,
	// or a newer link replaced this one.
	ErrTokenNotFound = errors.New("no sender address waits for that link")
	// ErrExpired is a confirm link older than ConfirmTTL.
	ErrExpired = errors.New("confirm link expired")
)

// LimitError is an account that has MaxAddresses sender addresses already.
type LimitError struct{ Count int }

func (e *LimitError) Error() string {
	return fmt.Sprintf("the account has %d sender addresses, the most it can have", e.Count)
}

// InvalidError is an address that isn't one.
type InvalidError struct{ Message string }

func (e *InvalidError) Error() string { return e.Message }

// TooSoonError is a resend within ResendGap of the last email.
type TooSoonError struct{ RetryAfter time.Duration }

func (e *TooSoonError) Error() string {
	return "a confirm email went to this address less than a minute ago"
}

// RateLimitError is an account that has sent DailyEmails confirm emails in
// the last EmailWindow. RetryAfter is when the oldest of them leaves it.
type RateLimitError struct{ RetryAfter time.Duration }

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("the account has sent %d sender address confirm emails in the last 24 hours, the most it can", DailyEmails)
}

// SendError is a confirm email the mail provider refused or didn't answer.
// The address is kept, pending; resending tries again.
type SendError struct{ Err error }

func (e *SendError) Error() string { return "send the confirm email: " + e.Err.Error() }
func (e *SendError) Unwrap() error { return e.Err }

// Address is one sender address.
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

// Service keeps sender addresses and sends their confirm emails.
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

// List is the account's sender addresses, oldest first.
func (s *Service) List(ctx context.Context, accountID string) ([]Address, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+columns+` FROM sender_addresses WHERE account_id = $1 ORDER BY created_at, id`, accountID)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Address, error) { return scan(row) })
	if out == nil {
		out = []Address{}
	}
	return out, err
}

// Get is one of the account's sender addresses.
func (s *Service) Get(ctx context.Context, accountID, id string) (*Address, error) {
	a, err := scan(s.DB.QueryRow(ctx, `SELECT `+columns+` FROM sender_addresses WHERE account_id = $1 AND id = $2`, accountID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// Add adds a sender address and emails it a confirm link. baseURL is the
// web app's public URL, for the link.
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
		var owner, exists bool
		var n int
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM users WHERE account_id = $1 AND lower(email) = $2),
			       EXISTS (SELECT 1 FROM sender_addresses WHERE account_id = $1 AND lower(email) = $2),
			       (SELECT count(*) FROM sender_addresses WHERE account_id = $1)`, accountID, email).Scan(&owner, &exists, &n); err != nil {
			return err
		}
		if owner {
			return ErrOwnerAddress
		}
		if exists {
			return ErrExists
		}
		if n >= MaxAddresses {
			return &LimitError{Count: n}
		}
		if err := countEmail(ctx, tx, accountID, now); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `
			INSERT INTO sender_addresses (account_id, email, confirm_token_hash, confirm_expires_at, confirm_sent_at, created_at)
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
			FROM sender_addresses t JOIN accounts a ON a.id = t.account_id
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
		// The account row is locked, as in Add, so resends of two addresses
		// at once can't both take the last email of the day.
		if _, err := tx.Exec(ctx, `SELECT 1 FROM accounts WHERE id = $1 FOR UPDATE`, accountID); err != nil {
			return err
		}
		if err := countEmail(ctx, tx, accountID, now); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			UPDATE sender_addresses SET confirm_token_hash = $3, confirm_expires_at = $4, confirm_sent_at = $5
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

// Remove removes a sender address, freeing its slot. Its link stops
// working, and the account can no longer send from it on the shared domain.
func (s *Service) Remove(ctx context.Context, accountID, id string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM sender_addresses WHERE account_id = $1 AND id = $2`, accountID, id)
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
			FROM sender_addresses t JOIN accounts a ON a.id = t.account_id
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
		_, err = tx.Exec(ctx, `UPDATE sender_addresses SET confirmed_at = $2 WHERE id = $1`, id, now)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// countEmail records a confirm email the account is about to send, or
// returns a *RateLimitError when it has sent DailyEmails in the last
// EmailWindow. The caller holds the account row's lock. A send that then
// fails still counts: the cap is on attempts.
func countEmail(ctx context.Context, tx pgx.Tx, accountID string, now time.Time) error {
	since := now.Add(-EmailWindow)
	if _, err := tx.Exec(ctx, `DELETE FROM sender_address_emails WHERE account_id = $1 AND sent_at <= $2`, accountID, since); err != nil {
		return err
	}
	var n int
	var oldest *time.Time
	if err := tx.QueryRow(ctx, `SELECT count(*), min(sent_at) FROM sender_address_emails WHERE account_id = $1`,
		accountID).Scan(&n, &oldest); err != nil {
		return err
	}
	if n >= DailyEmails && oldest != nil {
		return &RateLimitError{RetryAfter: oldest.Add(EmailWindow).Sub(now)}
	}
	_, err := tx.Exec(ctx, `INSERT INTO sender_address_emails (account_id, sent_at) VALUES ($1, $2)`, accountID, now)
	return err
}

// send emails the confirm link. When it can't, the send isn't recorded,
// so a resend may go at once, and a *SendError is returned.
func (s *Service) send(ctx context.Context, accountID, accountName, id, to, token, baseURL string) error {
	link := strings.TrimRight(baseURL, "/") + ConfirmPath + "?token=" + token
	if _, err := s.Mail.Send(ctx, confirmMessage(s.From, accountID, accountName, to, link)); err != nil {
		if _, cerr := s.DB.Exec(context.WithoutCancel(ctx), `UPDATE sender_addresses SET confirm_sent_at = NULL WHERE id = $1`, id); cerr != nil {
			err = errors.Join(err, cerr)
		}
		return &SendError{Err: err}
	}
	return nil
}

var confirmHTML = template.Must(template.New("confirm").Parse(`<!doctype html>
<html><body style="font-family:-apple-system,Segoe UI,Helvetica,Arial,sans-serif;color:#1a1a1a">
<p>{{.Account}} wants to send emails on mailgoose with this address as the sender, so replies come back to it.</p>
<p><a href="{{.URL}}">Confirm this sender address</a></p>
<p style="color:#555">Or open this link: {{.URL}}</p>
<p style="color:#555">The link works for 7 days. If you don't know {{.Account}}, ignore this email: nothing will be sent with your address.</p>
</body></html>
`))

// confirmMessage is the email that asks an address to confirm.
func confirmMessage(from mail.Address, accountID, accountName, to, link string) mail.Message {
	var b strings.Builder
	_ = confirmHTML.Execute(&b, map[string]string{"Account": accountName, "URL": link})
	text := fmt.Sprintf(`%s wants to send emails on mailgoose with this address as the sender, so replies come back to it.

Confirm it:
%s

The link works for 7 days. If you don't know %s, ignore this email: nothing will be sent with your address.
`, accountName, link, accountName)
	return mail.Message{
		From:      from,
		To:        mail.Address{Email: to},
		Subject:   "Confirm a sender address on mailgoose",
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
	sum := sha256.Sum256([]byte("mailgoose-sender-address\x00" + token))
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
