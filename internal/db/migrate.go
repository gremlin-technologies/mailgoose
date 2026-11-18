package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

//go:embed migrations/*.sql
var embedded embed.FS

// Migrations returns the SQL migrations built into the binary.
func Migrations() fs.FS {
	sub, err := fs.Sub(embedded, "migrations")
	if err != nil {
		panic(err) // the directory is embedded at build time
	}
	return sub
}

// Migrator wraps a goose provider over the pool. A Postgres advisory lock
// keeps two tasks starting at once from migrating at the same time.
type Migrator struct {
	provider *goose.Provider
	close    func() error
}

// NewMigrator returns a Migrator for pool.
func NewMigrator(pool *pgxpool.Pool) (*Migrator, error) {
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return nil, fmt.Errorf("create migration lock: %w", err)
	}
	sqlDB := stdlib.OpenDBFromPool(pool)
	p, err := goose.NewProvider(goose.DialectPostgres, sqlDB, Migrations(), goose.WithSessionLocker(locker))
	if err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("load migrations: %w", err)
	}
	return &Migrator{provider: p, close: sqlDB.Close}, nil
}

// Close releases the migrator's database handle. The pool stays open.
func (m *Migrator) Close() error { return m.close() }

// Up applies every pending migration and returns the versions applied.
func (m *Migrator) Up(ctx context.Context) ([]int64, error) {
	res, err := m.provider.Up(ctx)
	return versions(res), err
}

// Down rolls back the most recent migration.
func (m *Migrator) Down(ctx context.Context) ([]int64, error) {
	res, err := m.provider.Down(ctx)
	if res == nil {
		return nil, err
	}
	return []int64{res.Source.Version}, err
}

// Status lists each migration and whether it is applied.
func (m *Migrator) Status(ctx context.Context) ([]*goose.MigrationStatus, error) {
	return m.provider.Status(ctx)
}

func versions(res []*goose.MigrationResult) []int64 {
	out := make([]int64, 0, len(res))
	for _, r := range res {
		out = append(out, r.Source.Version)
	}
	return out
}
