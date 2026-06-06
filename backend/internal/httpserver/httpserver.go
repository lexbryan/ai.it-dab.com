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
)

// NewMux returns the scaffold handler: the base middleware chain wrapping the
// router (/version and the placeholder /healthz). It uses a development logger
// and no CORS origins.
//
// This is an interim entry point for the current scaffold binary. The
// server-lifecycle ticket replaces it with explicit config-driven wiring
// (config.Load → logger → Router(cfg, logger) → New(cfg, handler)) in
// cmd/server.
func NewMux() http.Handler {
	logger := applog.New(config.Config{Env: "development"})
	return NewRouter(config.Config{}, logger).Handler()
}

// Healthz reports process liveness with a 200. Readiness and upstream (vLLM)
// signals are added by the health/readiness ticket.
func Healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}
