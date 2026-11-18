// Package unsubscribe makes and checks the links recipients use to leave an
// account's list (CMP-1): the {{unsubscribe_url}} link in every campaign
// email, and the RFC 8058 one-click List-Unsubscribe header that points at
// the same address.
//
// A link carries a token that names the account, the contact and the
// campaign it was sent for, signed with a key of that account. The token is all a
// recipient needs: no sign-in, and nothing to look up first. Tokens don't
// expire, since an old email's link must still work.
package unsubscribe

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Sources of an unsubscribe.
const (
	// SourceLink is the page an email's unsubscribe link opens.
	SourceLink = "link"
	// SourceOneClick is a mail app's RFC 8058 one-click POST.
	SourceOneClick = "one_click"
)

// Errors from Lookup and Unsubscribe.
var (
	// ErrBadToken means the token was cut short, changed, or is in a format
	// this version doesn't make. Nothing is changed.
	ErrBadToken = errors.New("that unsubscribe link isn't valid")
	// ErrNotFound means the token was well formed but names no contact: the
	// contact was deleted, or the account closed.
	ErrNotFound = errors.New("that contact isn't on the list any more")
)

// tokenPrefix marks the token format. A later format gets a new prefix, and
// this one keeps working.
const tokenPrefix = "u1"

const (
	idLen   = 16
	macLen  = 16
	bodyLen = idLen*3 + macLen
)

// PathPrefix is where unsubscribe pages are served, under the app's URL.
const PathPrefix = "/u/"

// Path is the path of the page for a token.
func Path(token string) string { return PathPrefix + token }

// Service makes and checks unsubscribe tokens and records unsubscribes.
type Service struct {
	DB *pgxpool.Pool
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Signer makes the tokens for one account's recipients. Get one per
// campaign, not per recipient: making it reads the account's key.
type Signer struct {
	account uuid.UUID
	key     []byte
}

// Signer returns the signer for an account, making the account's key the
// first time.
func (s *Service) Signer(ctx context.Context, accountID string) (*Signer, error) {
	account, err := uuid.Parse(accountID)
	if err != nil {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	// The insert does nothing when the account already has a key; the
	// select then reads the one it has. Two callers racing both read the
	// one that won.
	if _, err := s.DB.Exec(ctx, `INSERT INTO unsubscribe_keys (account_id, key) VALUES ($1, $2) ON CONFLICT (account_id) DO NOTHING`, accountID, key); err != nil {
		return nil, err
	}
	if err := s.DB.QueryRow(ctx, `SELECT key FROM unsubscribe_keys WHERE account_id = $1`, accountID).Scan(&key); err != nil {
		return nil, err
	}
	return &Signer{account: account, key: key}, nil
}

// ErrNoKey means the account has never signed a link, so no link can be
// its.
var ErrNoKey = errors.New("the account has no link key")

// Existing returns the signer for an account that has a key already,
// reading it only: ErrNoKey when it has none. Checking a link uses it, so
// a forged link can't make keys.
func (s *Service) Existing(ctx context.Context, accountID string) (*Signer, error) {
	account, err := uuid.Parse(accountID)
	if err != nil {
		return nil, ErrNoKey
	}
	var key []byte
	err = s.DB.QueryRow(ctx, `SELECT key FROM unsubscribe_keys WHERE account_id = $1`, accountID).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoKey
	}
	if err != nil {
		return nil, err
	}
	return &Signer{account: account, key: key}, nil
}

// AccountID is the account the signer signs for.
func (g *Signer) AccountID() string { return g.account.String() }

// Sum signs msg with the account's key under label. Other signed links
// (open and click tracking) share the key; the label keeps their
// signatures apart from unsubscribe tokens' and from each other's.
func (g *Signer) Sum(label string, msg []byte) []byte {
	m := hmac.New(sha256.New, g.key)
	m.Write([]byte(label + "\x00"))
	m.Write(msg)
	return m.Sum(nil)
}

// Token is the token for a contact, for a campaign or (campaignID "") for
// no campaign, as in a test send.
func (g *Signer) Token(contactID, campaignID string) (string, error) {
	contact, err := uuid.Parse(contactID)
	if err != nil {
		return "", err
	}
	var campaign uuid.UUID
	if campaignID != "" {
		if campaign, err = uuid.Parse(campaignID); err != nil {
			return "", err
		}
	}
	body := make([]byte, 0, bodyLen)
	body = append(body, g.account[:]...)
	body = append(body, contact[:]...)
	body = append(body, campaign[:]...)
	body = append(body, mac(g.key, body)...)
	return tokenPrefix + base64.RawURLEncoding.EncodeToString(body), nil
}

// URL is the unsubscribe address for a contact, under baseURL (the app's
// public URL, without a trailing slash).
func (g *Signer) URL(baseURL, contactID, campaignID string) (string, error) {
	t, err := g.Token(contactID, campaignID)
	if err != nil {
		return "", err
	}
	return baseURL + Path(t), nil
}

func mac(key, ids []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("mailgoose-unsubscribe\x00"))
	m.Write(ids)
	return m.Sum(nil)[:macLen]
}

// parsed is a token taken apart; its signature is still to check.
type parsed struct {
	accountID  string
	contactID  string
	campaignID string // "" for none
	ids, mac   []byte
}

// parse takes a token apart, or returns ErrBadToken.
func parse(token string) (*parsed, error) {
	rest, ok := strings.CutPrefix(token, tokenPrefix)
	if !ok || base64.RawURLEncoding.EncodedLen(bodyLen) != len(rest) {
		return nil, ErrBadToken
	}
	body, err := base64.RawURLEncoding.Strict().DecodeString(rest)
	if err != nil || len(body) != bodyLen {
		return nil, ErrBadToken
	}
	account, _ := uuid.FromBytes(body[:idLen])
	contact, _ := uuid.FromBytes(body[idLen : idLen*2])
	campaign, _ := uuid.FromBytes(body[idLen*2 : idLen*3])
	p := &parsed{accountID: account.String(), contactID: contact.String(), ids: body[:idLen*3], mac: body[idLen*3:]}
	if campaign != uuid.Nil {
		p.campaignID = campaign.String()
	}
	return p, nil
}

// Target is who a token unsubscribes, and from which account.
type Target struct {
	AccountID   string
	AccountName string
	ContactID   string
	Email       string
	// Status is the contact's status: after Unsubscribe, the new one.
	Status string
	// CampaignID is the campaign the link was sent for, or "".
	CampaignID string
}

// Subscribed reports whether the contact would still get the account's
// emails, so the page asks them to confirm rather than saying they're out.
func (t *Target) Subscribed() bool {
	return t.Status == "subscribed" || t.Status == "unconfirmed"
}

// Lookup checks a token and returns who it's for, changing nothing.
func (s *Service) Lookup(ctx context.Context, token string) (*Target, error) {
	return s.find(ctx, s.DB, token, false)
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func (s *Service) find(ctx context.Context, q querier, token string, lock bool) (*Target, error) {
	p, err := parse(token)
	if err != nil {
		return nil, err
	}
	// The signature first, so a changed token is refused as one, whatever
	// it now names.
	var key []byte
	err = q.QueryRow(ctx, `SELECT key FROM unsubscribe_keys WHERE account_id = $1`, p.accountID).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) {
		// No key: this account never signed a token.
		return nil, ErrBadToken
	}
	if err != nil {
		return nil, err
	}
	if !hmac.Equal(p.mac, mac(key, p.ids)) {
		return nil, ErrBadToken
	}
	sql := `
		SELECT a.name, c.email, c.status
		FROM contacts c JOIN accounts a ON a.id = c.account_id
		WHERE c.id = $1 AND c.account_id = $2`
	if lock {
		sql += ` FOR UPDATE OF c`
	}
	t := &Target{AccountID: p.accountID, ContactID: p.contactID, CampaignID: p.campaignID}
	err = q.QueryRow(ctx, sql, p.contactID, p.accountID).Scan(&t.AccountName, &t.Email, &t.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		// The contact was deleted, or the account closed.
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return t, nil
}

// Unsubscribe takes the token's contact off the list now: a subscribed or
// unconfirmed contact becomes unsubscribed, and the time, campaign and
// source are recorded. It's idempotent: a contact that already unsubscribed,
// bounced or complained is left as it is and nothing more is recorded.
// changed reports whether this call made the change.
func (s *Service) Unsubscribe(ctx context.Context, token, source string) (t *Target, changed bool, err error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	t, err = s.find(ctx, tx, token, true)
	if err != nil {
		return nil, false, err
	}
	if !t.Subscribed() {
		return t, false, nil
	}
	now := s.now()
	if _, err := tx.Exec(ctx, `
		UPDATE contacts SET status = 'unsubscribed', status_changed_at = $2, updated_at = $2
		WHERE id = $1`, t.ContactID, now); err != nil {
		return nil, false, err
	}
	// The campaign is kept only when it's still the account's; a deleted
	// draft's id isn't worth refusing the unsubscribe over.
	var campaign any
	if t.CampaignID != "" {
		campaign = t.CampaignID
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO unsubscribes (account_id, contact_id, campaign_id, source, created_at)
		VALUES ($1, $2, (SELECT id FROM campaigns WHERE id = $3::uuid AND account_id = $1), $4, $5)`,
		t.AccountID, t.ContactID, campaign, source, now); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	t.Status = "unsubscribed"
	return t, true, nil
}
