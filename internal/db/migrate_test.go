package db

import (
	"context"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"

	"github.com/gremlin-technologies/mailgoose/internal/config"
)

// TestMigrationsUpDown runs against a real Postgres when
// MAILGOOSE_TEST_DATABASE_URL is set (make test-db sets it), and is skipped
// otherwise. It needs an empty, throwaway database.
func TestMigrationsUpDown(t *testing.T) {
	dsn := os.Getenv("MAILGOOSE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("MAILGOOSE_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()

	pool, err := Open(ctx, config.Database{URL: dsn}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	m, err := NewMigrator(pool)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()

	if _, err := m.Up(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}

	var accountID string
	if err := pool.QueryRow(ctx, `INSERT INTO accounts (name) VALUES ('Acme') RETURNING id`).Scan(&accountID); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO users (account_id, email) VALUES ($1, 'Pat@Example.com')`, accountID); err != nil {
		t.Fatalf("insert user: %v", err)
	}

	var other string
	if err := pool.QueryRow(ctx, `INSERT INTO accounts (name) VALUES ('Other') RETURNING id`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO users (account_id, email) VALUES ($1, 'pat@example.com')`, other); err == nil {
		t.Error("expected the same email in another case to be rejected")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO users (account_id, email) VALUES ($1, 'second@example.com')`, accountID); err == nil {
		t.Error("expected a second user on one account to be rejected")
	}

	// Roll everything back so the test can run again on the same database.
	for {
		v, err := m.Down(ctx)
		if err != nil || len(v) == 0 {
			break
		}
	}
	status, err := m.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range status {
		if s.State == goose.StateApplied {
			t.Errorf("migration %d still applied after down", s.Source.Version)
		}
	}
}

// TestOpenRefetchesSecretAfterAuthFailure simulates an RDS password rotation:
// the first secret read returns a stale password, and the pool must re-read
// the secret on the next connection attempt.
func TestOpenRefetchesSecretAfterAuthFailure(t *testing.T) {
	dsn := os.Getenv("MAILGOOSE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("MAILGOOSE_TEST_DATABASE_URL not set")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(u.Port())
	password, _ := u.User.Password()
	src := &fakeSource{creds: []Credentials{
		{Username: u.User.Username(), Password: "stale"},
		{Username: u.User.Username(), Password: password},
	}}
	cfg := config.Database{
		Host:      u.Hostname(),
		Port:      port,
		Name:      strings.TrimPrefix(u.Path, "/"),
		SecretARN: "arn:aws:secretsmanager:us-east-2:0:secret:test",
		SSLMode:   "disable",
	}

	ctx := context.Background()
	pool, err := Open(ctx, cfg, src)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err == nil {
		t.Fatal("expected the stale password to fail")
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("second ping should use the re-read secret: %v", err)
	}
	if src.calls != 2 {
		t.Errorf("secret read %d times, want 2", src.calls)
	}
}
