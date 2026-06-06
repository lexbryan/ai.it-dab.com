package httpserver

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/config"
	applog "github.com/lexbryan/ai.it-dab.com/backend/internal/log"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/version"
)

// Router is the route-registration seam. Domains attach handlers with Handle /
// HandleFunc; Handler() returns the mux wrapped in the base middleware chain so
// the chain applies uniformly to every registered route.
type Router struct {
	mux    *http.ServeMux
	cfg    config.Config
	logger *slog.Logger
}

// NewRouter builds a Router and registers the baseline routes (/version proves
// the wiring; /healthz is the scaffold liveness placeholder).
func NewRouter(cfg config.Config, logger *slog.Logger) *Router {
	if logger == nil {
		logger = applog.New(cfg)
	}
	r := &Router{mux: http.NewServeMux(), cfg: cfg, logger: logger}
	r.HandleFunc("GET /version", versionHandler)
	r.HandleFunc("GET /healthz", Healthz)
	return r
}

// Handle registers a handler for an http.ServeMux pattern (e.g. "POST /v1/keys").
func (r *Router) Handle(pattern string, h http.Handler) { r.mux.Handle(pattern, h) }

// HandleFunc registers a handler function for a pattern.
func (r *Router) HandleFunc(pattern string, h http.HandlerFunc) { r.mux.Handle(pattern, h) }

// Handler returns the fully assembled handler: the mux wrapped, outermost to
// innermost, in request ID + structured logging, then CORS, then panic
// recovery. Logging is outermost so it observes the final status (including a
// 500 synthesized by recovery); recovery is innermost so it wraps only handler
// execution. None of the layers re-wrap the ResponseWriter, so http.Flusher and
// http.Hijacker reach the handler intact for streaming.
func (r *Router) Handler() http.Handler {
	return Chain(r.mux,
		applog.Middleware(r.logger),
		CORS(r.cfg.CORSAllowedOrigins),
		Recoverer(r.logger),
	)
}

// Chain wraps h in the given middleware so that the first listed middleware is
// the outermost layer: Chain(h, a, b, c) == a(b(c(h))).
func Chain(h http.Handler, mw ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}

// versionHandler reports the build version as JSON. It proves the base chain is
// wired and gives ops a cheap deployed-version probe.
func versionHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"version": version.String()})
}
