// Package db owns the Postgres connection pool and (in later tickets) the
// repository layer built on top of it.
//
// # Driver choice
//
// The pool is jackc/pgx v5 via pgxpool. pgx is the de-facto standard pure-Go
// Postgres driver: it speaks the binary protocol, has first-class context
// support, and pgxpool gives a bounded, health-checked connection pool out of
// the box. database/sql is avoided so the repository layer can use pgx's typed
// query helpers directly.
//
// New parses the DSN from config, applies bounded pool sizing/lifetime defaults
// (each overridable via the DSN's pool_* parameters), opens the pool, and
// verifies connectivity with a bounded Ping so a bad database fails fast at
// startup rather than on the first request. The returned *pgxpool.Pool exposes
// Ping(ctx) for the readiness endpoint and Close() for graceful shutdown.
//
// # Integration tests
//
// Tests that need a live database are skipped unless DAB_TEST_DATABASE_URL is
// set to a reachable Postgres DSN (see `make test-integration` and the compose
// `db` service). The unit tests for DSN parsing, default application, and the
// unreachable/invalid-DSN error paths run with no database.
package db
