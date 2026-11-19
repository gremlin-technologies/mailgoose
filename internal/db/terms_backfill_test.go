package db

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/pressly/goose/v3"

	"github.com/gremlin-technologies/mailgoose/internal/config"
	"github.com/gremlin-technologies/mailgoose/internal/policy"
)

// termsMigration records the terms version accepted (MG-68).
const termsMigration = 36

// TestTermsBackfill checks that migration 00036 gives accounts that signed
// up before it the version published with the policy pages, at the time
// they accepted, as a history row.
func TestTermsBackfill(t *testing.T) {
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
	defer func() {
		// Leave the database empty for the next test.
		if _, err := pool.Exec(ctx, `DELETE FROM accounts`); err != nil {
			t.Error(err)
		}
		for {
			v, err := m.Down(ctx)
			if err != nil || len(v) == 0 {
				break
			}
		}
	}()

	// Back to just before the migration.
	for applied(ctx, t, m, termsMigration) {
		if _, err := m.Down(ctx); err != nil {
			t.Fatalf("down: %v", err)
		}
	}
	accepted := time.Date(2026, 11, 2, 9, 30, 0, 0, time.UTC)
	var old, pending string
	if err := pool.QueryRow(ctx, `INSERT INTO accounts (name, status, terms_accepted_at) VALUES ('Old', 'active', $1) RETURNING id`,
		accepted).Scan(&old); err != nil {
		t.Fatal(err)
	}
	// An account from before sign-up recorded any acceptance gets none.
	if err := pool.QueryRow(ctx, `INSERT INTO accounts (name) VALUES ('Older') RETURNING id`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Up(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}

	var version *string
	if err := pool.QueryRow(ctx, `SELECT terms_version FROM accounts WHERE id = $1`, old).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version == nil || *version != policy.Initial {
		t.Errorf("terms_version = %v, want %s", version, policy.Initial)
	}
	var v, via string
	var at time.Time
	if err := pool.QueryRow(ctx, `SELECT version, accepted_at, via FROM terms_acceptances WHERE account_id = $1`, old).Scan(&v, &at, &via); err != nil {
		t.Fatal(err)
	}
	if v != policy.Initial || !at.Equal(accepted) || via != "backfill" {
		t.Errorf("history row %s %v %s, want %s %v backfill", v, at, via, policy.Initial, accepted)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM terms_acceptances WHERE account_id = $1`, pending).Scan(&n); err != nil || n != 0 {
		t.Errorf("an account that never accepted got %d rows (err %v)", n, err)
	}
}

func applied(ctx context.Context, t *testing.T, m *Migrator, version int64) bool {
	t.Helper()
	status, err := m.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range status {
		if s.Source.Version == version {
			return s.State == goose.StateApplied
		}
	}
	t.Fatalf("no migration %d", version)
	return false
}
