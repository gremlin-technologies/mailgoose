package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

// Credentials are a Postgres user name and password.
type Credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// CredentialSource fetches the current credentials.
type CredentialSource interface {
	Fetch(ctx context.Context) (Credentials, error)
}

// SecretsManagerSource reads the RDS-managed master secret, which holds
// {"username": ..., "password": ...}.
type SecretsManagerSource struct {
	Client   *secretsmanager.Client
	SecretID string
}

// Fetch implements CredentialSource.
func (s SecretsManagerSource) Fetch(ctx context.Context) (Credentials, error) {
	out, err := s.Client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(s.SecretID),
	})
	if err != nil {
		return Credentials{}, fmt.Errorf("read database secret: %w", err)
	}
	var c Credentials
	if err := json.Unmarshal([]byte(aws.ToString(out.SecretString)), &c); err != nil {
		return Credentials{}, errors.New("database secret is not {\"username\",\"password\"} JSON")
	}
	if c.Username == "" || c.Password == "" {
		return Credentials{}, errors.New("database secret is missing username or password")
	}
	return c, nil
}

// rotatingCredentials caches credentials and re-fetches them when they may be
// stale. RDS rotates the master password (every 7 days by default), so the
// pool asks for credentials on every new connection. A connection attempt that
// never succeeded (wrong password after a rotation, or any other failure)
// makes the next attempt re-read the secret. maxAge is a backstop.
//
// Connections in flight at the same moment can also trigger a re-read. That
// costs one extra Secrets Manager call, which is fine because the pool opens
// new connections rarely.
type rotatingCredentials struct {
	src    CredentialSource
	maxAge time.Duration
	now    func() time.Time

	mu        sync.Mutex
	cached    Credentials
	fetchedAt time.Time
	pending   int // connection attempts since the last success
}

func newRotatingCredentials(src CredentialSource, maxAge time.Duration) *rotatingCredentials {
	return &rotatingCredentials{src: src, maxAge: maxAge, now: time.Now}
}

// beforeConnect returns the credentials for a new connection attempt.
func (r *rotatingCredentials) beforeConnect(ctx context.Context) (Credentials, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	stale := r.fetchedAt.IsZero() || r.now().Sub(r.fetchedAt) > r.maxAge || r.pending > 0
	if stale {
		c, err := r.src.Fetch(ctx)
		if err != nil {
			return Credentials{}, err
		}
		r.cached, r.fetchedAt, r.pending = c, r.now(), 0
	}
	r.pending++
	return r.cached, nil
}

// afterConnect records a successful connection.
func (r *rotatingCredentials) afterConnect() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pending > 0 {
		r.pending--
	}
}
