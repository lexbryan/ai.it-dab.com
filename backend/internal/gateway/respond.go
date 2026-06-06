// Package gateway holds the public LLM gateway: the two-key authentication
// middleware that guards it (auth.go) and, in later tickets, the chat endpoint
// and SSE streaming. It is SEPARATE from internal/admin — the admin packages
// authenticate human admins via a session JWT, while the gateway authenticates
// calling PROJECTS via their two-key credential.
package gateway

import (
	"encoding/json"
	"net/http"
)

// writeJSON writes v as a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes the gateway error envelope: {"error":{"type","message"}}.
// The full unified error contract (incl. the SSE error frame) is centralized in
// a later ticket; the gateway adopts the shape now.
func writeError(w http.ResponseWriter, code int, errType, message string) {
	writeJSON(w, code, map[string]any{
		"error": map[string]string{"type": errType, "message": message},
	})
}
