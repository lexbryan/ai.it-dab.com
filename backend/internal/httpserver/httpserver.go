// Package httpserver holds the HTTP wiring for the gateway: the router, the
// base middleware stack (request ID + structured logging, CORS, panic
// recovery), and a configured *http.Server builder.
//
// # Router choice
//
// The router is the standard library's net/http.ServeMux using Go 1.22
// method-based patterns ("GET /version"). Reasons:
//
//   - It does not buffer responses, so Server-Sent Events from the gateway pass
//     through immediately (chi/gin/echo would also work, but a dependency buys
//     little here). http.Flusher/http.Hijacker survive the whole chain.
//   - The routing surface is small (admin API + one gateway endpoint), well
//     within what method+path patterns express.
//   - Zero third-party dependencies, consistent with internal/config and
//     internal/log.
//
// Domains attach handlers through the Router seam (Router.Handle /
// Router.HandleFunc) without editing this package, and Router.Handler() applies
// the base middleware to every route.
package httpserver

import (
	"net/http"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/config"
	applog "github.com/lexbryan/ai.it-dab.com/backend/internal/log"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/version"
)

// NewMux returns the scaffold handler: the base middleware chain wrapping the
// router with /version and the liveness/readiness endpoints. It uses a
// development logger, no CORS origins, and no database — so /readyz reports
// not-ready until the server-lifecycle ticket wires the real pool.
//
// This is an interim entry point for the current scaffold binary. The
// server-lifecycle ticket replaces it with explicit config-driven wiring
// (config.Load → logger → Router(cfg, logger) → RegisterHealth → New(cfg,
// handler)) in cmd/server.
func NewMux() http.Handler {
	logger := applog.New(config.Config{Env: "development"})
	r := NewRouter(config.Config{}, logger)
	RegisterHealth(r, HealthDeps{Version: version.String()})
	return r.Handler()
}
