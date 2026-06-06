package httpserver

import (
	"net/http"
	"time"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/config"
)

// Server timeouts. These bound how long the server waits to *read* a request
// and how long an idle keep-alive connection lingers. There is deliberately no
// http.Server.WriteTimeout: it caps the time from the end of the request
// headers to the end of the response write, which would truncate long-lived
// Server-Sent Events from the gateway. Streaming responses are bounded instead
// by request context / per-route deadlines where appropriate.
const (
	readHeaderTimeout = 10 * time.Second // slowloris protection on the header read
	readTimeout       = 60 * time.Second // full request read (bodies are small)
	idleTimeout       = 120 * time.Second
)

// New builds an *http.Server from Config and the fully assembled handler
// (typically Router.Handler()). It sets read/idle timeouts but no WriteTimeout,
// so streaming responses are not cut off. The caller owns ListenAndServe and
// graceful shutdown (the server-lifecycle ticket).
func New(cfg config.Config, handler http.Handler) *http.Server {
	addr := cfg.ListenAddr
	if addr == "" {
		addr = ":8080"
	}
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		IdleTimeout:       idleTimeout,
		// WriteTimeout intentionally unset — see the const block above.
	}
}
