// Package db opens the Postgres pool and runs migrations.
package db

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/config"
)

// Open returns a connection pool for cfg. When cfg uses Secrets Manager, src
// supplies the credentials for every new connection; otherwise src may be nil.
func Open(ctx context.Context, cfg config.Database, src CredentialSource) (*pgxpool.Pool, error) {
	if !cfg.Configured() {
		return nil, errors.New("no database configured: set DATABASE_URL, or DATABASE_HOST and DATABASE_SECRET_ARN")
	}

	dsn := cfg.URL
	if dsn == "" {
		dsn = awsDSN(cfg)
	}
	pc, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse database settings: %w", err)
	}
	pc.MaxConnLifetime = time.Hour
	pc.MaxConnIdleTime = 15 * time.Minute

	if cfg.UsesSecret() {
		if src == nil {
			return nil, errors.New("database uses Secrets Manager but no credential source was given")
		}
		creds := newRotatingCredentials(src, 30*time.Minute)
		pc.BeforeConnect = func(ctx context.Context, cc *pgx.ConnConfig) error {
			c, err := creds.beforeConnect(ctx)
			if err != nil {
				return err
			}
			cc.User, cc.Password = c.Username, c.Password
			return nil
		}
		pc.AfterConnect = func(context.Context, *pgx.Conn) error {
			creds.afterConnect()
			return nil
		}
	}

	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, fmt.Errorf("open database pool: %w", err)
	}
	return pool, nil
}

// awsDSN builds a URL with no credentials; BeforeConnect fills them in.
func awsDSN(cfg config.Database) string {
	u := url.URL{
		Scheme:   "postgres",
		Host:     cfg.Host + ":" + strconv.Itoa(cfg.Port),
		Path:     "/" + cfg.Name,
		RawQuery: url.Values{"sslmode": {cfg.SSLMode}}.Encode(),
	}
	return u.String()
}
