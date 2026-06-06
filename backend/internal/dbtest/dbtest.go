// Package dbtest provides helpers for integration tests that need a real
// Postgres. Tests using it are skipped unless DAB_TEST_DATABASE_URL points at a
// reachable database (e.g. the compose `db` service or a throwaway container).
package dbtest

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/config"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/db"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/db/migrate"
)

// Pool returns a pool connected to DAB_TEST_DATABASE_URL with all embedded
// migrations applied, skipping the test when the variable is unset. The pool is
// closed automatically on test cleanup.
func Pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DAB_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set DAB_TEST_DATABASE_URL to run integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := db.New(ctx, config.Config{DatabaseURL: dsn})
	if err != nil {
		t.Fatalf("dbtest: connecting: %v", err)
	}
	// Start each test from a clean schema, then migrate. This is deterministic
	// and self-heals from any partial state a prior crashed run left behind
	// (e.g. tables present without a schema_migrations record). Run integration
	// tests serially (go test -p 1) against the shared database.
	for _, stmt := range []string{"DROP SCHEMA public CASCADE", "CREATE SCHEMA public"} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			pool.Close()
			t.Fatalf("dbtest: resetting schema: %v", err)
		}
	}
	if _, err := migrate.Migrate(ctx, pool); err != nil {
		pool.Close()
		t.Fatalf("dbtest: applying migrations: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// Truncate empties the given tables so a test starts from a known state.
func Truncate(t *testing.T, pool *pgxpool.Pool, tables ...string) {
	t.Helper()
	for _, table := range tables {
		if _, err := pool.Exec(context.Background(), "TRUNCATE "+table+" RESTART IDENTITY CASCADE"); err != nil {
			t.Fatalf("dbtest: truncate %s: %v", table, err)
		}
	}
}
