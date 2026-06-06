package httpserver

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/config"
	applog "github.com/lexbryan/ai.it-dab.com/backend/internal/log"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/version"
)

// captureLogger returns a production (JSON) logger writing to buf, for asserting
// on emitted log records.
func captureLogger(buf *bytes.Buffer) *slog.Logger {
	return applog.NewWithWriter(config.Config{Env: "production", LogLevel: "debug"}, buf)
}

func okHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }
}

func TestRouter_VersionRoute(t *testing.T) {
	r := NewRouter(config.Config{}, captureLogger(&bytes.Buffer{}))
	rr := httptest.NewRecorder()
	r.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/version", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("/version status = %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v (%q)", err, rr.Body.String())
	}
	if body["version"] != version.String() {
		t.Errorf("version = %q, want %q", body["version"], version.String())
	}
}

func TestRouter_HealthzRoute(t *testing.T) {
	r := NewRouter(config.Config{}, captureLogger(&bytes.Buffer{}))
	rr := httptest.NewRecorder()
	r.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("/healthz status = %d, want 200", rr.Code)
	}
}

// TestRouter_BaseChainAppliesToAllRoutes proves the logging middleware wraps
// every route: a freshly registered handler gets a request ID echoed on the
// response and a summary line logged.
func TestRouter_BaseChainAppliesToAllRoutes(t *testing.T) {
	var buf bytes.Buffer
	r := NewRouter(config.Config{}, captureLogger(&buf))
	r.HandleFunc("GET /custom", okHandler())

	rr := httptest.NewRecorder()
	r.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/custom", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("/custom status = %d, want 200", rr.Code)
	}
	if rr.Header().Get(applog.HeaderRequestID) == "" {
		t.Error("base chain should echo X-Request-Id on a registered route")
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &rec); err != nil {
		t.Fatalf("expected one summary line: %v (%q)", err, buf.String())
	}
	if rec["path"] != "/custom" || rec["msg"] != "request" {
		t.Errorf("unexpected summary line: %v", rec)
	}
}

func TestRouter_UnknownRouteIsNotFound(t *testing.T) {
	r := NewRouter(config.Config{}, captureLogger(&bytes.Buffer{}))
	rr := httptest.NewRecorder()
	r.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/does-not-exist", nil))
	if rr.Code != http.StatusNotFound {
		t.Errorf("unknown route status = %d, want 404", rr.Code)
	}
}

// TestRouter_PreservesFlusherThroughChain proves no layer (logging, CORS,
// recovery) strips http.Flusher before the handler — required for SSE.
func TestRouter_PreservesFlusherThroughChain(t *testing.T) {
	var isFlusher bool
	r := NewRouter(config.Config{}, captureLogger(&bytes.Buffer{}))
	r.HandleFunc("GET /stream", func(w http.ResponseWriter, _ *http.Request) {
		_, isFlusher = w.(http.Flusher)
	})
	rr := httptest.NewRecorder() // httptest recorder is an http.Flusher
	r.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/stream", nil))
	if !isFlusher {
		t.Error("handler must see an http.Flusher through the full middleware chain")
	}
}

// TestRouter_FlushReachesClientThroughChain drives a real server: a flushed
// chunk must reach the client while the handler is still running.
func TestRouter_FlushReachesClientThroughChain(t *testing.T) {
	released := make(chan struct{})
	r := NewRouter(config.Config{}, applog.NewWithWriter(config.Config{Env: "production", LogLevel: "error"}, io.Discard))
	r.HandleFunc("GET /sse", func(w http.ResponseWriter, _ *http.Request) {
		f, ok := w.(http.Flusher)
		if !ok {
			t.Error("no flusher in handler")
			return
		}
		_, _ = io.WriteString(w, "first\n")
		f.Flush()
		select {
		case <-released:
		case <-time.After(2 * time.Second):
		}
	})

	srv := httptest.NewServer(r.Handler())
	defer srv.Close()
	defer close(released)

	resp, err := http.Get(srv.URL + "/sse")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	first := make([]byte, len("first\n"))
	done := make(chan error, 1)
	go func() { _, err := io.ReadFull(resp.Body, first); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("read first chunk: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("flushed chunk did not arrive -> chain buffered the response")
	}
	if string(first) != "first\n" {
		t.Errorf("first chunk = %q, want first", first)
	}
}
