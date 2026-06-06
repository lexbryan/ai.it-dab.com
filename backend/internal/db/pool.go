package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/config"
)

// Pool sizing and lifetime defaults. Each is applied only when the DSN does not
// set the corresponding pgx pool_* parameter, so operators can tune the pool
// through DATABASE_URL (e.g. "...?pool_max_conns=25&pool_max_conn_lifetime=2h")
// without a code change.
const (
	defaultMaxConns        = 10
	defaultMinConns        = 0
	defaultMaxConnLifetime = 30 * time.Minute
	defaultMaxConnIdleTime = 5 * time.Minute
	defaultHealthCheck     = time.Minute

	// defaultConnectTimeout bounds the startup connectivity check when the
	// caller's context carries no deadline, so a bad database fails fast.
	defaultConnectTimeout = 5 * time.Second
)

// New builds a Postgres connection pool from cfg.DatabaseURL, applies the pool
// defaults, and verifies connectivity with a bounded Ping. On any failure it
// closes the pool (if created) and returns a clear, DSN-free error — an
// unreachable or invalid database fails fast instead of hanging. The caller owns
// the returned pool and must Close() it on shutdown.
func New(ctx context.Context, cfg config.Config) (*pgxpool.Pool, error) {
	poolCfg, err := buildPoolConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("db: creating connection pool: %w", err)
	}

	pingCtx, cancel := connectivityContext(ctx)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: connectivity check failed: %w", err)
	}
	return pool, nil
}

// connectivityContext returns ctx unchanged (with a no-op cancel) when it
// already carries a deadline; otherwise it bounds the connectivity check with
// defaultConnectTimeout so New can never block indefinitely.
func connectivityContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, defaultConnectTimeout)
}

// buildPoolConfig parses dsn and applies the sizing/lifetime defaults for any
// pool parameter the DSN did not set explicitly. It never includes the DSN in
// an error, since the DSN may embed the database password.
func buildPoolConfig(dsn string) (*pgxpool.Config, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("db: DATABASE_URL is empty")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("db: invalid DATABASE_URL")
	}

	low := strings.ToLower(dsn)
	if !strings.Contains(low, "pool_max_conns") {
		cfg.MaxConns = defaultMaxConns
	}
	if !strings.Contains(low, "pool_min_conns") {
		cfg.MinConns = defaultMinConns
	}
	if !strings.Contains(low, "pool_max_conn_lifetime") {
		cfg.MaxConnLifetime = defaultMaxConnLifetime
	}
	if !strings.Contains(low, "pool_max_conn_idle_time") {
		cfg.MaxConnIdleTime = defaultMaxConnIdleTime
	}
	if !strings.Contains(low, "pool_health_check_period") {
		cfg.HealthCheckPeriod = defaultHealthCheck
	}
	return cfg, nil
}
