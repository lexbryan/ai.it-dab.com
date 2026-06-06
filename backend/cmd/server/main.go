// Command server is the entrypoint for the DAB AI gateway. It opens the Postgres
// pool, optionally applies migrations, assembles the full route tree (via
// internal/app), and serves until a signal, then drains in-flight requests within
// a bounded grace period before releasing resources.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/app"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/config"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/db"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/db/migrate"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/httpserver"
	applog "github.com/lexbryan/ai.it-dab.com/backend/internal/log"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/version"
)

func main() {
	// A SIGINT/SIGTERM cancels ctx, which drives graceful shutdown in runServer.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx); err != nil {
		// run returns an error only on a startup or serve failure; a clean
		// signal-driven shutdown returns nil. There is no half-started server.
		fmt.Fprintf(os.Stderr, "dab-ai-gateway: fatal: %v\n", err)
		os.Exit(1)
	}
}

// run loads and validates configuration, opens the database pool, optionally
// applies pending migrations, assembles the handler, and serves until ctx is
// canceled. Any startup failure is returned (so main exits non-zero) without a
// partially-started server; a clean shutdown returns nil.
func run(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := applog.New(cfg)
	logger.Info("starting", "service", "dab-ai-gateway", "version", version.String(), "env", cfg.Env)

	// Open the pool first so an unreachable database fails fast, before we bind
	// the listener or accept any traffic.
	pool, err := db.New(ctx, cfg)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}

	if cfg.AutoMigrate {
		applied, err := migrate.Migrate(ctx, pool)
		if err != nil {
			pool.Close()
			return fmt.Errorf("migrations: %w", err)
		}
		logger.Info("migrations applied", "count", len(applied))
	}

	addr := cfg.ListenAddr
	if addr == "" {
		addr = ":8080"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		pool.Close()
		return fmt.Errorf("listen on %s: %w", addr, err)
	}

	srv := httpserver.New(cfg, app.BuildHandler(cfg, logger, pool))
	return runServer(ctx, ln, srv, pool, cfg.ShutdownGrace, logger)
}

// runServer serves on ln until ctx is canceled, then gracefully shuts the HTTP
// server down within grace and closes the pool. The order is deliberate: stop
// accepting and drain in-flight requests FIRST, then release the pool, so a
// request never loses its database connection mid-flight. If the drain exceeds
// grace the server is force-closed (logged) so a hung streaming connection cannot
// block shutdown forever. It returns nil on a clean shutdown and an error on a
// listen/serve failure or a grace-exceeded force-close.
func runServer(ctx context.Context, ln net.Listener, srv *http.Server, pool interface{ Close() }, grace time.Duration, logger *slog.Logger) error {
	// The pool is always released, and (on the shutdown path) only after Shutdown
	// has returned — i.e. after in-flight requests have drained.
	defer pool.Close()

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	logger.Info("listening", "addr", ln.Addr().String())

	select {
	case err := <-serveErr:
		// Serve returned before any signal: a real listen/serve failure.
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	logger.Info("shutdown signal received; draining in-flight requests", "grace", grace)
	shutCtx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		logger.Error("graceful shutdown exceeded grace; forcing close", "error", err)
		_ = srv.Close()
		<-serveErr // let the Serve goroutine unblock after the forced close
		return err
	}

	<-serveErr // Serve returns http.ErrServerClosed once Shutdown completes
	logger.Info("shutdown complete")
	return nil
}
