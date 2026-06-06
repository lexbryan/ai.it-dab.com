package httpserver

import (
	"log/slog"
	"net/http"
	"runtime/debug"

	applog "github.com/lexbryan/ai.it-dab.com/backend/internal/log"
)

// Recoverer returns middleware that turns a panicking handler into a logged 500
// while keeping the server serving other requests. base is a fallback logger;
// the request-scoped logger from the context (set by the logging middleware) is
// preferred so the panic line carries the request ID.
//
// The recovered response is a generic message — internal details (the panic
// value, the stack) go only to the logs, never to the client. The middleware
// does not wrap the ResponseWriter, so http.Flusher/http.Hijacker are
// preserved; and if the handler already started the response (e.g. a streaming
// handler panicked mid-flight) it does not write a body, which would corrupt the
// in-flight response.
func Recoverer(base *slog.Logger) func(http.Handler) http.Handler {
	if base == nil {
		base = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rv := recover()
				if rv == nil {
					return
				}
				// http.ErrAbortHandler is the stdlib's intentional-abort
				// sentinel; propagate it so net/http handles it as designed.
				if rv == http.ErrAbortHandler {
					panic(rv)
				}

				logger := applog.FromContext(r.Context())
				if logger == nil {
					logger = base
				}
				logger.LogAttrs(r.Context(), slog.LevelError, "panic recovered",
					slog.Any("panic", rv),
					slog.String("stack", string(debug.Stack())),
				)

				if !responseStarted(w) {
					http.Error(w, "internal server error", http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// responseStarted reports whether the response has already begun, so recovery
// can avoid corrupting an in-flight (e.g. streaming) response. It relies on the
// StatusWriter that the logging middleware installs; if absent, it conservatively
// reports false.
func responseStarted(w http.ResponseWriter) bool {
	if sw, ok := w.(applog.StatusWriter); ok {
		return sw.Written()
	}
	return false
}
