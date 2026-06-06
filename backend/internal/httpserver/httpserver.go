// Package httpserver holds the HTTP wiring for the gateway.
//
// This scaffold exposes a single health endpoint via the standard-library mux.
// The production router, CORS, and middleware stack are introduced in a later
// ticket; this file deliberately stays minimal.
package httpserver

import "net/http"

// NewMux returns a mux serving the scaffold's health endpoint. It uses Go 1.22
// method-based routing patterns ("GET /healthz").
func NewMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", Healthz)
	return mux
}

// Healthz reports process liveness with a 200. Readiness and upstream (vLLM)
// signals are added in a later ticket.
func Healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}
