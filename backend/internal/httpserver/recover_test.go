package httpserver

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/config"
)

// TestRecoverer_PanicBecomes500AndKeepsServing covers the core acceptance
// criterion: a panicking handler yields a 500, the stack is logged, internals do
// not leak to the client, and the server keeps serving later requests.
func TestRecoverer_PanicBecomes500AndKeepsServing(t *testing.T) {
	var buf bytes.Buffer
	r := NewRouter(config.Config{}, captureLogger(&buf))
	const secret = "kaboom-internal-detail"
	r.HandleFunc("GET /boom", func(http.ResponseWriter, *http.Request) {
		panic(secret)
	})
	h := r.Handler()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/boom", nil))

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("panic status = %d, want 500", rr.Code)
	}
	if strings.Contains(rr.Body.String(), secret) {
		t.Errorf("response body leaked panic internals: %q", rr.Body.String())
	}

	// The stack and panic value must be logged (with the request-scoped logger).
	if !strings.Contains(buf.String(), "panic recovered") {
		t.Errorf("expected a 'panic recovered' log line, got: %s", buf.String())
	}
	if !strings.Contains(buf.String(), secret) {
		t.Error("panic value should be logged")
	}
	if !strings.Contains(buf.String(), "stack") {
		t.Error("a stack trace should be logged")
	}
	// The summary line for the panicking request should record status 500.
	if !strings.Contains(buf.String(), "\"status\":500") {
		t.Errorf("summary line should record status 500: %s", buf.String())
	}

	// Server keeps serving.
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, httptest.NewRequest(http.MethodGet, "/version", nil))
	if rr2.Code != http.StatusOK {
		t.Errorf("after a panic the server should keep serving; /version = %d", rr2.Code)
	}
}

// TestRecoverer_DoesNotCorruptInFlightResponse: when the handler already started
// the response (e.g. streaming) and then panics, recovery logs but must not
// append a 500 body or overwrite the status.
func TestRecoverer_DoesNotCorruptInFlightResponse(t *testing.T) {
	var buf bytes.Buffer
	r := NewRouter(config.Config{}, captureLogger(&buf))
	r.HandleFunc("GET /midstream", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "partial-data")
		panic("after start")
	})
	h := r.Handler()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/midstream", nil))

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (already committed before panic)", rr.Code)
	}
	if got := rr.Body.String(); got != "partial-data" {
		t.Errorf("body = %q, want just the partial data with no 500 appended", got)
	}
	if !strings.Contains(buf.String(), "panic recovered") {
		t.Error("panic should still be logged even when the response already started")
	}
}

// TestRecoverer_PropagatesAbortHandler: http.ErrAbortHandler is the stdlib's
// intentional-abort sentinel and must not be swallowed.
func TestRecoverer_PropagatesAbortHandler(t *testing.T) {
	r := NewRouter(config.Config{}, captureLogger(&bytes.Buffer{}))
	r.HandleFunc("GET /abort", func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	})
	h := r.Handler()

	defer func() {
		rv := recover()
		if rv != http.ErrAbortHandler {
			t.Errorf("ErrAbortHandler should propagate, got %v", rv)
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/abort", nil))
	t.Fatal("expected ServeHTTP to propagate the abort panic")
}

// TestRecoverer_StandaloneWithoutStatusWriter: used directly (not behind the
// logging middleware), responseStarted falls back to false and a fresh panic
// still produces a 500.
func TestRecoverer_StandaloneWithoutStatusWriter(t *testing.T) {
	h := Recoverer(captureLogger(&bytes.Buffer{}))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &body) // body need not be JSON; just ensure no panic value
	if strings.Contains(rr.Body.String(), "boom") {
		t.Error("panic value must not leak to the client")
	}
}
