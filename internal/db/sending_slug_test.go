package db

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/config"
)

// slugMigration adds sending slugs and sender addresses (MG-85).
const slugMigration = 41

// TestSendingSlugBackfill checks that migration 00041 gives every account
// made before it a sending slug from its name, oldest first, so the first
// of two accounts with one name keeps the plain slug.
func TestSendingSlugBackfill(t *testing.T) {
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

	for applied(ctx, t, m, slugMigration) {
		if _, err := m.Down(ctx); err != nil {
			t.Fatalf("down: %v", err)
		}
	}
	start := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	names := []string{"Pat's Bakery", "Pat's Bakery", "Support", "Mailgoose"}
	ids := make([]string, len(names))
	for i, n := range names {
		if err := pool.QueryRow(ctx, `INSERT INTO accounts (name, created_at) VALUES ($1, $2) RETURNING id`,
			n, start.Add(time.Duration(i)*time.Hour)).Scan(&ids[i]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.Up(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}
	slugs := make([]string, len(ids))
	for i, id := range ids {
		if err := pool.QueryRow(ctx, `SELECT sending_slug FROM accounts WHERE id = $1`, id).Scan(&slugs[i]); err != nil {
			t.Fatal(err)
		}
	}
	if slugs[0] != "pats-bakery" || !strings.HasPrefix(slugs[1], "pats-bakery-") {
		t.Errorf("same name: %q then %q", slugs[0], slugs[1])
	}
	if !strings.HasPrefix(slugs[2], "support-") || !strings.HasPrefix(slugs[3], "mailgoose-") {
		t.Errorf("reserved names got %q and %q", slugs[2], slugs[3])
	}
	// New accounts get one too.
	var s string
	if err := pool.QueryRow(ctx, `INSERT INTO accounts (name) VALUES ('Garden Shop') RETURNING sending_slug`).Scan(&s); err != nil || s != "garden-shop" {
		t.Errorf("new account: %q, %v", s, err)
	}
}
