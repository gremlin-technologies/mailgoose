// Package dbtest gives tests a migrated Postgres database of their own.
//
// Tests that need Postgres call New. It skips the test unless
// MAILGOOSE_TEST_DATABASE_URL is set (CI and `make test-db` set it). Each
// call gets a fresh schema, so packages that run in parallel never see each
// other's rows or migrations, and the schema is dropped when the test ends.
package dbtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/db"
)

// EnvURL names the variable that points tests at a throwaway database.
const EnvURL = "MAILGOOSE_TEST_DATABASE_URL"

// New returns a pool on a new schema with every migration applied.
func New(t testing.TB) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv(EnvURL)
	if dsn == "" {
		t.Skip(EnvURL + " not set")
	}
	ctx := context.Background()

	var b [6]byte
	_, _ = rand.Read(b[:])
	schema := "test_" + hex.EncodeToString(b[:])

	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = admin.Close(ctx) }()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), dsn)
		if err != nil {
			t.Errorf("drop schema %s: %v", schema, err)
			return
		}
		defer func() { _ = c.Close(context.Background()) }()
		if _, err := c.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop schema %s: %v", schema, err)
		}
	})

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	m, err := db.NewMigrator(pool)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	if _, err := m.Up(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return pool
}
