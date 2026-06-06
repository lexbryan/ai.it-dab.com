package app

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/config"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testConfig() config.Config {
	return config.Config{
		Env:              "development",
		ListenAddr:       "127.0.0.1:0",
		LogLevel:         "info",
		VLLMURL:          "http://vllm.invalid",
		JWTSecret:        "test-secret",
		LoginRateLimit:   config.RateLimit{RequestsPerMinute: 0}, // disabled for deterministic routing checks
		GatewayRateLimit: config.RateLimit{RequestsPerMinute: 0},
	}
}

// Every domain router is mounted and guarded. With a nil pool we only exercise
// the routes that reject before touching the database, which proves routing and
// middleware wiring without a live DB.
func TestBuildHandler_RoutesMountedAndGuarded(t *testing.T) {
	h := BuildHandler(testConfig(), discardLogger(), nil)

	cases := []struct {
		name, method, path, body string
		want                     int
	}{
		{"version route", http.MethodGet, "/version", "", http.StatusOK},
		{"liveness route", http.MethodGet, "/healthz", "", http.StatusOK},
		{"admin login mounted (bad body rejected before DB)", http.MethodPost, "/api/admin/login", `{}`, http.StatusBadRequest},
		{"admin keys guarded by session auth", http.MethodGet, "/api/admin/keys", "", http.StatusUnauthorized},
		{"gateway guarded by two-key auth", http.MethodPost, "/v1/gateway/chat", `{"model":"m","message":"hi"}`, http.StatusUnauthorized},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != c.want {
			t.Errorf("%s: %s %s = %d, want %d", c.name, c.method, c.path, rr.Code, c.want)
		}
	}
}
