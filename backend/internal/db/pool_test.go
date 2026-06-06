package db

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/config"
)

func TestBuildPoolConfig_AppliesDefaults(t *testing.T) {
	cfg, err := buildPoolConfig("postgres://user:pass@localhost:5432/dab?sslmode=disable")
	if err != nil {
		t.Fatalf("buildPoolConfig: %v", err)
	}
	if cfg.MaxConns != defaultMaxConns {
		t.Errorf("MaxConns = %d, want default %d", cfg.MaxConns, defaultMaxConns)
	}
	if cfg.MinConns != defaultMinConns {
		t.Errorf("MinConns = %d, want default %d", cfg.MinConns, defaultMinConns)
	}
	if cfg.MaxConnLifetime != defaultMaxConnLifetime {
		t.Errorf("MaxConnLifetime = %v, want default %v", cfg.MaxConnLifetime, defaultMaxConnLifetime)
	}
	if cfg.MaxConnIdleTime != defaultMaxConnIdleTime {
		t.Errorf("MaxConnIdleTime = %v, want default %v", cfg.MaxConnIdleTime, defaultMaxConnIdleTime)
	}
	if cfg.HealthCheckPeriod != defaultHealthCheck {
		t.Errorf("HealthCheckPeriod = %v, want default %v", cfg.HealthCheckPeriod, defaultHealthCheck)
	}
}

func TestBuildPoolConfig_DSNOverridesDefaults(t *testing.T) {
	dsn := "postgres://user:pass@localhost:5432/dab?pool_max_conns=25&pool_max_conn_lifetime=2h"
	cfg, err := buildPoolConfig(dsn)
	if err != nil {
		t.Fatalf("buildPoolConfig: %v", err)
	}
	if cfg.MaxConns != 25 {
		t.Errorf("MaxConns = %d, want 25 from DSN", cfg.MaxConns)
	}
	if cfg.MaxConnLifetime != 2*time.Hour {
		t.Errorf("MaxConnLifetime = %v, want 2h from DSN", cfg.MaxConnLifetime)
	}
	// An unspecified parameter still falls back to the package default.
	if cfg.MaxConnIdleTime != defaultMaxConnIdleTime {
		t.Errorf("MaxConnIdleTime = %v, want default %v", cfg.MaxConnIdleTime, defaultMaxConnIdleTime)
	}
}

func TestBuildPoolConfig_Empty(t *testing.T) {
	if _, err := buildPoolConfig("   "); err == nil {
		t.Fatal("expected error for empty DSN")
	}
}

func TestBuildPoolConfig_Invalid(t *testing.T) {
	// A non-numeric port makes pgxpool.ParseConfig fail.
	if _, err := buildPoolConfig("postgres://user:pass@localhost:notaport/dab"); err == nil {
		t.Fatal("expected error for invalid DSN")
	}
}

// TestNew_UnreachableHostFailsFast covers the no-hang requirement: a closed port
// must surface a clear error well within the bounded connectivity timeout, with
// no live database involved.
func TestNew_UnreachableHostFailsFast(t *testing.T) {
	cfg := config.Config{DatabaseURL: "postgres://user:pass@127.0.0.1:1/dab?sslmode=disable"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	pool, err := New(ctx, cfg)
	elapsed := time.Since(start)

	if err == nil {
		pool.Close()
		t.Fatal("expected an error connecting to a closed port")
	}
	if elapsed >= 5*time.Second {
		t.Errorf("New took %v; should fail fast within the timeout", elapsed)
	}
}

func TestNew_CanceledContext(t *testing.T) {
	cfg := config.Config{DatabaseURL: "postgres://user:pass@127.0.0.1:1/dab?sslmode=disable"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already canceled

	if pool, err := New(ctx, cfg); err == nil {
		pool.Close()
		t.Fatal("expected an error for a canceled context")
	}
}

// TestNew_LiveDB exercises the happy path against a real Postgres. It is skipped
// unless DAB_TEST_DATABASE_URL points at a reachable database.
func TestNew_LiveDB(t *testing.T) {
	dsn := os.Getenv("DAB_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set DAB_TEST_DATABASE_URL to run the live-database pool test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := New(ctx, config.Config{DatabaseURL: dsn})
	if err != nil {
		t.Fatalf("New against live DB: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	var got int
	if err := pool.QueryRow(ctx, "SELECT 1").Scan(&got); err != nil {
		t.Fatalf("SELECT 1: %v", err)
	}
	if got != 1 {
		t.Errorf("SELECT 1 = %d, want 1", got)
	}

	// Ping must respect a canceled context.
	cctx, ccancel := context.WithCancel(ctx)
	ccancel()
	if err := pool.Ping(cctx); err == nil {
		t.Error("Ping should fail on a canceled context")
	}
}
