// Package gateway holds the public LLM gateway: the two-key authentication
// middleware that guards it (auth.go), the chat endpoint (chat.go) and its SSE
// streaming path (stream.go), and the unified error contract (this file).
//
// The error contract is one shape across both transports —
// {"error":{"type","message"}} — emitted as a JSON body on the non-streaming
// path and as an `event: error` SSE frame on the streaming path. The error TYPE
// is the stable, documented part of the contract; the HTTP status is derived
// from it HERE, in one place, so a handler can never pair a type with the wrong
// status, and no message ever leaks an upstream body, URL, stack, or secret.
package gateway

import (
	"encoding/json"
	"io"
	"net/http"
)

// Stable error types. These are the public contract (see docs/CONNECTING.md);
// the HTTP status for each lives only in statusForError.
const (
	errInvalidRequest = "invalid_request" // 400
	errUnauthorized   = "unauthorized"    // 401
	errNotFound       = "not_found"       // 404
	errRateLimited    = "rate_limited"    // 429
	errUpstream       = "upstream_error"  // 502
	errUnavailable    = "unavailable"     // 503
	errInternal       = "internal_error"  // 500
)

// statusForError maps a stable error type to its HTTP status. An unknown type
// falls back to 500 so a miswired type can never be served as a success.
func statusForError(errType string) int {
	switch errType {
	case errInvalidRequest:
		return http.StatusBadRequest
	case errUnauthorized:
		return http.StatusUnauthorized
	case errNotFound:
		return http.StatusNotFound
	case errRateLimited:
		return http.StatusTooManyRequests
	case errUpstream:
		return http.StatusBadGateway
	case errUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// errorEnvelope is the shared error body for both transports.
func errorEnvelope(errType, message string) map[string]any {
	return map[string]any{"error": map[string]string{"type": errType, "message": message}}
}

// writeJSON writes v as a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes the JSON error envelope with the status derived from errType.
// message must be a short, sanitized string — never an upstream body or secret.
func writeError(w http.ResponseWriter, errType, message string) {
	writeJSON(w, statusForError(errType), errorEnvelope(errType, message))
}

// writeSSEError emits the error envelope as an `event: error` SSE frame — the
// streaming counterpart of writeError. The status is already in the SSE headers
// sent at stream start, so only the typed envelope rides the frame.
func writeSSEError(w io.Writer, errType, message string) {
	payload, _ := json.Marshal(errorEnvelope(errType, message))
	_, _ = io.WriteString(w, "event: error\ndata: ")
	_, _ = w.Write(payload)
	_, _ = io.WriteString(w, "\n\n")
}
