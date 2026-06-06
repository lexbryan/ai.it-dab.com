package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const allowedOrigin = "https://app.example.com"

func corsHandler(allowed []string, called *bool) http.Handler {
	return CORS(allowed)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if called != nil {
			*called = true
		}
		w.WriteHeader(http.StatusOK)
	}))
}

func preflight(origin string) *http.Request {
	req := httptest.NewRequest(http.MethodOptions, "/v1/keys", nil)
	req.Header.Set("Origin", origin)
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	return req
}

func TestCORS_PreflightAllowedOrigin(t *testing.T) {
	var called bool
	h := corsHandler([]string{allowedOrigin}, &called)

	req := preflight(allowedOrigin)
	req.Header.Set("Access-Control-Request-Headers", "Content-Type, Authorization")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Errorf("preflight status = %d, want 204", rr.Code)
	}
	if called {
		t.Error("preflight must not reach the application handler")
	}
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != allowedOrigin {
		t.Errorf("Allow-Origin = %q, want %q", got, allowedOrigin)
	}
	if got := rr.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("Allow-Credentials = %q, want true", got)
	}
	if got := rr.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, "POST") {
		t.Errorf("Allow-Methods = %q, want it to include POST", got)
	}
	if got := rr.Header().Get("Access-Control-Allow-Headers"); got != "Content-Type, Authorization" {
		t.Errorf("Allow-Headers = %q, want the reflected request headers", got)
	}
	if got := rr.Header().Get("Access-Control-Max-Age"); got == "" {
		t.Error("preflight should set Access-Control-Max-Age")
	}
	if !containsValue(rr.Header().Values("Vary"), "Origin") {
		t.Error("response should Vary on Origin")
	}
}

func TestCORS_PreflightDefaultAllowedHeaders(t *testing.T) {
	h := corsHandler([]string{allowedOrigin}, nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, preflight(allowedOrigin)) // no Access-Control-Request-Headers
	if got := rr.Header().Get("Access-Control-Allow-Headers"); got != corsDefaultAllowedHeaders {
		t.Errorf("Allow-Headers = %q, want default %q", got, corsDefaultAllowedHeaders)
	}
}

func TestCORS_PreflightDisallowedOrigin(t *testing.T) {
	var called bool
	h := corsHandler([]string{allowedOrigin}, &called)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, preflight("https://evil.example.com"))

	if rr.Code != http.StatusNoContent {
		t.Errorf("preflight status = %d, want 204", rr.Code)
	}
	if called {
		t.Error("preflight must not reach the application handler")
	}
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("disallowed origin must not be granted, got Allow-Origin = %q", got)
	}
}

func TestCORS_ActualRequestAllowedOrigin(t *testing.T) {
	var called bool
	h := corsHandler([]string{allowedOrigin}, &called)

	req := httptest.NewRequest(http.MethodGet, "/v1/keys", nil)
	req.Header.Set("Origin", allowedOrigin)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if !called {
		t.Error("actual request should reach the handler")
	}
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != allowedOrigin {
		t.Errorf("Allow-Origin = %q, want %q", got, allowedOrigin)
	}
	if got := rr.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("Allow-Credentials = %q, want true", got)
	}
	if got := rr.Header().Get("Access-Control-Expose-Headers"); got != "X-Request-Id" {
		t.Errorf("Expose-Headers = %q, want X-Request-Id so the SPA can read the request id", got)
	}
	if !containsValue(rr.Header().Values("Vary"), "Origin") {
		t.Error("response should Vary on Origin")
	}
}

func TestCORS_ActualRequestDisallowedOriginStillServedWithoutGrant(t *testing.T) {
	var called bool
	h := corsHandler([]string{allowedOrigin}, &called)

	req := httptest.NewRequest(http.MethodGet, "/v1/keys", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	// CORS is browser-enforced: the server still serves, it just withholds the
	// grant so the browser blocks the response.
	if !called {
		t.Error("handler should still run; CORS is enforced by the browser, not the server")
	}
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("disallowed origin must not be granted, got %q", got)
	}
}

func TestCORS_NoOriginPassesThrough(t *testing.T) {
	var called bool
	h := corsHandler([]string{allowedOrigin}, &called)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/keys", nil)) // no Origin

	if !called {
		t.Error("server-to-server request (no Origin) should pass through")
	}
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("no Origin should yield no CORS headers, got %q", got)
	}
}

func TestCORS_EmptyAllowlistDeniesAll(t *testing.T) {
	h := corsHandler(nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/v1/keys", nil)
	req.Header.Set("Origin", allowedOrigin)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("empty allowlist must grant no origin, got %q", got)
	}
}

// TestCORS_NeverWildcardWithCredentials guards the invariant that a credentialed
// response never uses "*".
func TestCORS_NeverWildcardWithCredentials(t *testing.T) {
	h := corsHandler([]string{allowedOrigin}, nil)
	req := httptest.NewRequest(http.MethodGet, "/v1/keys", nil)
	req.Header.Set("Origin", allowedOrigin)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Header().Get("Access-Control-Allow-Credentials") == "true" &&
		rr.Header().Get("Access-Control-Allow-Origin") == "*" {
		t.Error("must never emit Allow-Origin: * together with credentials")
	}
}

func containsValue(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
