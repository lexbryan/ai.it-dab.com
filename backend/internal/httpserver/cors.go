package httpserver

import "net/http"

// corsAllowedMethods is the method set advertised on preflight responses. It is
// broad enough for the admin REST API and the gateway endpoint.
const corsAllowedMethods = "GET, POST, PUT, PATCH, DELETE, OPTIONS"

// corsDefaultAllowedHeaders is echoed on preflight when the browser does not
// send Access-Control-Request-Headers.
const corsDefaultAllowedHeaders = "Content-Type, Authorization"

// CORS returns middleware enforcing a strict origin allowlist sourced from
// CORS_ALLOWED_ORIGINS.
//
// The admin browser app calls this API cross-origin with credentials (the admin
// JWT, whether carried as a cookie or an Authorization header). Per the Fetch
// spec a credentialed response must name a single concrete origin and may never
// use "*", so this middleware reflects the *specific* matched origin and sets
// Access-Control-Allow-Credentials: true. It never emits "*" together with
// credentials.
//
// An empty allowlist denies all cross-origin browser access (no
// Access-Control-Allow-Origin is sent). Note this is browser-enforced: requests
// without an Origin header (server-to-server callers, including the public
// gateway's API-key clients) are unaffected and pass straight through. The
// public gateway therefore relies on its two-key auth, not CORS, for access
// control.
func CORS(allowedOrigins []string) func(http.Handler) http.Handler {
	allow := make(map[string]struct{}, len(allowedOrigins))
	for _, o := range allowedOrigins {
		allow[o] = struct{}{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			isPreflight := r.Method == http.MethodOptions &&
				r.Header.Get("Access-Control-Request-Method") != ""

			if origin != "" {
				// The response varies by Origin even when we deny it, so caches
				// must key on it.
				w.Header().Add("Vary", "Origin")
				if _, ok := allow[origin]; ok {
					h := w.Header()
					h.Set("Access-Control-Allow-Origin", origin)
					h.Set("Access-Control-Allow-Credentials", "true")
					if isPreflight {
						h.Set("Access-Control-Allow-Methods", corsAllowedMethods)
						if reqHeaders := r.Header.Get("Access-Control-Request-Headers"); reqHeaders != "" {
							h.Set("Access-Control-Allow-Headers", reqHeaders)
						} else {
							h.Set("Access-Control-Allow-Headers", corsDefaultAllowedHeaders)
						}
						h.Set("Access-Control-Max-Age", "600")
					}
				}
			}

			if isPreflight {
				// Preflight never reaches application handlers. Allowed origins
				// received their Access-Control-* headers above; disallowed ones
				// get a bare 204 with no grant, which the browser blocks.
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
