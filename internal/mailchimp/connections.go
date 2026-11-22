package mailchimp

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Connected Mailchimp accounts (MG-73). Mailchimp's OAuth tokens don't
// expire and carry full access with no scopes, so a token is kept as briefly
// and narrowly as it can be: sealed in Postgres, never logged or returned by
// the API, and deleted after ConnectionTTL, on disconnect, or once the
// import that needed it is done (Forget).

const (
	// StateTTL is how long a connect link works.
	StateTTL = 10 * time.Minute
	// ConnectionTTL is the longest a token is kept.
	ConnectionTTL = 24 * time.Hour
)

// ErrNotConfigured means the app has no Mailchimp client id and secret.
var ErrNotConfigured = errors.New("mailchimp oauth isn't configured")

// ErrStateNotFound means the callback's state is unknown, used or expired.
var ErrStateNotFound = errors.New("mailchimp connect link not found or expired")

// ErrWrongAccount means the browser finishing the connect isn't signed in to
// the account that started it.
var ErrWrongAccount = errors.New("mailchimp connect finished by another account")

// ErrDenied means the person didn't allow access on Mailchimp's page.
var ErrDenied = errors.New("mailchimp access not allowed")

// ErrNotConnected means the account has no Mailchimp connection.
var ErrNotConnected = errors.New("no mailchimp account connected")

// Connection is an account's connected Mailchimp account. The token is
// never part of it.
type Connection struct {
	AccountName string
	DataCenter  string
	ConnectedAt time.Time
	ExpiresAt   time.Time
}

// Connections keeps accounts' Mailchimp connections.
type Connections struct {
	DB    *pgxpool.Pool
	OAuth *OAuth
	// APIURL, if set, is where the clients Client makes go instead of
	// Mailchimp (tests).
	APIURL string
	// HTTP is passed to the clients Client makes.
	HTTP *http.Client
	Now  func() time.Time
}

func (c *Connections) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Configured reports whether connecting is set up.
func (c *Connections) Configured() bool {
	return c != nil && c.OAuth != nil && c.OAuth.ClientID != "" && c.OAuth.ClientSecret != ""
}

// Start makes a single-use connect link for the account, replacing any
// earlier link. A connection already made stays until a new one replaces it.
func (c *Connections) Start(ctx context.Context, accountID string) (authorizeURL string, expires time.Time, err error) {
	if !c.Configured() {
		return "", time.Time{}, ErrNotConfigured
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", time.Time{}, err
	}
	state := base64.RawURLEncoding.EncodeToString(b)
	expires = c.now().Add(StateTTL).UTC()
	if _, err := c.DB.Exec(ctx, `
		INSERT INTO mailchimp_connections (account_id, state_hash, state_expires_at) VALUES ($1, $2, $3)
		ON CONFLICT (account_id) DO UPDATE SET state_hash = $2, state_expires_at = $3`,
		accountID, stateHash(state), expires); err != nil {
		return "", time.Time{}, err
	}
	return c.OAuth.AuthorizeURL(state), expires, nil
}

func stateHash(state string) []byte {
	h := sha256.Sum256([]byte(state))
	return h[:]
}

// Finish completes a connect from the callback. The state is used up first,
// whatever happens next. signedIn is the account of the browser's web
// session ("" for none): only the account that started the connect may
// finish it, so a link sent to someone else can't connect their Mailchimp
// to the sender's account. denied is Mailchimp's error parameter, if any.
func (c *Connections) Finish(ctx context.Context, state, signedIn, code, denied string) (*Connection, error) {
	if !c.Configured() {
		return nil, ErrNotConfigured
	}
	if state == "" {
		return nil, ErrStateNotFound
	}
	var accountID string
	err := c.DB.QueryRow(ctx, `
		UPDATE mailchimp_connections SET state_hash = NULL, state_expires_at = NULL
		WHERE state_hash = $1 AND state_expires_at > $2 RETURNING account_id`, stateHash(state), c.now()).Scan(&accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrStateNotFound
	}
	if err != nil {
		return nil, err
	}
	switch {
	case denied != "":
		return nil, ErrDenied
	case signedIn != accountID:
		return nil, ErrWrongAccount
	case code == "":
		return nil, &ErrOAuth{Step: "callback", Reason: "no code"}
	}
	token, err := c.OAuth.Exchange(ctx, code)
	if err != nil {
		return nil, err
	}
	meta, err := c.OAuth.Metadata(ctx, token)
	if err != nil {
		return nil, err
	}
	return c.Put(ctx, accountID, token, meta.DataCenter, meta.AccountName)
}

// Put stores token as the account's connection, replacing any it has, for
// ConnectionTTL. Finish calls it once Mailchimp has given the token.
func (c *Connections) Put(ctx context.Context, accountID, token, dataCenter, accountName string) (*Connection, error) {
	sealed, err := c.seal(accountID, token)
	if err != nil {
		return nil, err
	}
	now := c.now().UTC()
	conn := &Connection{AccountName: accountName, DataCenter: dataCenter, ConnectedAt: now, ExpiresAt: now.Add(ConnectionTTL)}
	if _, err := c.DB.Exec(ctx, `
		INSERT INTO mailchimp_connections (account_id, token, data_center, account_name, connected_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (account_id) DO UPDATE SET token = $2, data_center = $3, account_name = $4, connected_at = $5, expires_at = $6`,
		accountID, sealed, conn.DataCenter, conn.AccountName, conn.ConnectedAt, conn.ExpiresAt); err != nil {
		return nil, err
	}
	return conn, nil
}

// Get is the account's connection, or nil when there's none or it expired.
func (c *Connections) Get(ctx context.Context, accountID string) (*Connection, error) {
	if err := c.dropExpired(ctx, accountID); err != nil {
		return nil, err
	}
	var conn Connection
	var name *string
	err := c.DB.QueryRow(ctx, `
		SELECT account_name, data_center, connected_at, expires_at FROM mailchimp_connections
		WHERE account_id = $1 AND token IS NOT NULL AND expires_at > $2`, accountID, c.now()).
		Scan(&name, &conn.DataCenter, &conn.ConnectedAt, &conn.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if name != nil {
		conn.AccountName = *name
	}
	return &conn, nil
}

// Forget deletes the account's connection and any connect link: on
// disconnect, and once an import that read the account is done.
func (c *Connections) Forget(ctx context.Context, accountID string) error {
	_, err := c.DB.Exec(ctx, `DELETE FROM mailchimp_connections WHERE account_id = $1`, accountID)
	return err
}

// Client is a read-only client for the account's connected Mailchimp
// account (MG-74 reads with it).
func (c *Connections) Client(ctx context.Context, accountID string) (*Client, error) {
	if !c.Configured() {
		return nil, ErrNotConfigured
	}
	if err := c.dropExpired(ctx, accountID); err != nil {
		return nil, err
	}
	var sealed []byte
	var dc string
	err := c.DB.QueryRow(ctx, `
		SELECT token, data_center FROM mailchimp_connections
		WHERE account_id = $1 AND token IS NOT NULL AND expires_at > $2`, accountID, c.now()).Scan(&sealed, &dc)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotConnected
	}
	if err != nil {
		return nil, err
	}
	token, err := c.open(accountID, sealed)
	if err != nil {
		// A token sealed with an earlier client secret can't be read; the
		// account connects again.
		return nil, ErrNotConnected
	}
	return NewClient(token, dc, c.APIURL, c.HTTP)
}

// dropExpired deletes the account's token once it's past ConnectionTTL, so
// it goes as soon as anything looks for it rather than at the next prune. A
// connect link on the same row stays.
func (c *Connections) dropExpired(ctx context.Context, accountID string) error {
	_, err := c.DB.Exec(ctx, `
		UPDATE mailchimp_connections SET token = NULL, data_center = NULL, account_name = NULL, connected_at = NULL, expires_at = NULL
		WHERE account_id = $1 AND token IS NOT NULL AND expires_at <= $2`, accountID, c.now())
	return err
}

// ClearExpired deletes every token past ConnectionTTL. The fetch job calls
// it each minute, so no token outlives its 24 hours by more than that.
func (c *Connections) ClearExpired(ctx context.Context) (int64, error) {
	tag, err := c.DB.Exec(ctx, `
		UPDATE mailchimp_connections SET token = NULL, data_center = NULL, account_name = NULL, connected_at = NULL, expires_at = NULL
		WHERE token IS NOT NULL AND expires_at <= $1`, c.now())
	return tag.RowsAffected(), err
}

// pruneConnections deletes expired connections and unused connect links.
func pruneConnections(ctx context.Context, db *pgxpool.Pool, now time.Time) (int64, error) {
	tag, err := db.Exec(ctx, `
		DELETE FROM mailchimp_connections
		WHERE expires_at <= $1 OR (token IS NULL AND (state_expires_at IS NULL OR state_expires_at <= $1))`, now)
	return tag.RowsAffected(), err
}

// Sealing tokens: AES-256-GCM with a key derived from the app's Mailchimp
// client secret, and the account id as associated data, so a sealed token
// copied to another account's row doesn't open. Changing the secret makes
// stored tokens unreadable, which only means connecting again; none is kept
// past a day.

const sealVersion = 1

func (c *Connections) key() ([]byte, error) {
	return hkdf.Key(sha256.New, []byte(c.OAuth.ClientSecret), nil, "mailgoose mailchimp token v1", 32)
}

func (c *Connections) aead() (cipher.AEAD, error) {
	k, err := c.key()
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (c *Connections) seal(accountID, token string) ([]byte, error) {
	g, err := c.aead()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	out := append([]byte{sealVersion}, nonce...)
	return g.Seal(out, nonce, []byte(token), []byte("mailchimp-token:"+accountID)), nil
}

func (c *Connections) open(accountID string, sealed []byte) (string, error) {
	g, err := c.aead()
	if err != nil {
		return "", err
	}
	if len(sealed) < 1+g.NonceSize() || sealed[0] != sealVersion {
		return "", fmt.Errorf("mailchimp: sealed token has an unknown form")
	}
	nonce, ct := sealed[1:1+g.NonceSize()], sealed[1+g.NonceSize():]
	plain, err := g.Open(nil, nonce, ct, []byte("mailchimp-token:"+accountID))
	if err != nil {
		return "", errors.New("mailchimp: sealed token doesn't open")
	}
	return string(plain), nil
}
