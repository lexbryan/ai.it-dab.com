// Package admin holds the admin API: the login endpoint, the auth middleware
// that guards it, and (in later tickets) API-key CRUD. The admin session is a
// JWT cookie, distinct from the project two-key gateway credentials.
package admin

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

// writeError writes the gateway's error envelope: {"error":{"type","message"}}.
// The full unified contract is centralized in a later ticket; admin endpoints
// adopt the shape now.
func writeError(w http.ResponseWriter, code int, errType, message string) {
	writeJSON(w, code, map[string]any{
		"error": map[string]string{"type": errType, "message": message},
	})
}
