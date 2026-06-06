package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/config"
)

type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

func okProbe(context.Context) error  { return nil }
func badProbe(context.Context) error { return errors.New("upstream down") }

// newHealthRouter mounts the health endpoints with the given deps and returns
// the assembled handler.
func newHealthRouter(deps HealthDeps) http.Handler {
	r := NewRouter(config.Config{}, captureLogger(&bytes.Buffer{}))
	RegisterHealth(r, deps)
	return r.Handler()
}

func getJSON(t *testing.T, h http.Handler, path string) (int, map[string]any) {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s body not JSON: %v (%q)", path, err, rr.Body.String())
	}
	return rr.Code, body
}

// TestHealthz_AlwaysOK: liveness is 200 even when the DB is down and no probe is
// configured — it answers "is the process up", making no external calls.
func TestHealthz_AlwaysOK(t *testing.T) {
	h := newHealthRouter(HealthDeps{DB: fakePinger{err: errors.New("db down")}, Version: "1.2.3"})
	code, body := getJSON(t, h, "/healthz")
	if code != http.StatusOK {
		t.Fatalf("/healthz = %d, want 200", code)
	}
	if body["status"] != "ok" || body["version"] != "1.2.3" {
		t.Errorf("unexpected /healthz body: %v", body)
	}
}

func TestReadyz_OKWhenPostgresReachable(t *testing.T) {
	h := newHealthRouter(HealthDeps{DB: fakePinger{}, VLLMProbe: okProbe})
	code, body := getJSON(t, h, "/readyz")
	if code != http.StatusOK {
		t.Fatalf("/readyz = %d, want 200", code)
	}
	checks := body["checks"].(map[string]any)
	if body["status"] != "ready" || checks["postgres"] != "ok" || checks["vllm"] != "ok" {
		t.Errorf("unexpected /readyz body: %v", body)
	}
}

func TestReadyz_503WhenPostgresDown(t *testing.T) {
	h := newHealthRouter(HealthDeps{DB: fakePinger{err: errors.New("connection refused")}, VLLMProbe: okProbe})
	code, body := getJSON(t, h, "/readyz")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz = %d, want 503", code)
	}
	checks := body["checks"].(map[string]any)
	if body["status"] != "not_ready" || checks["postgres"] != "unreachable" {
		t.Errorf("body should name postgres as the failing dep: %v", body)
	}
}

// TestReadyz_VLLMUnreachableIsNonFatal: a down upstream is surfaced but does not
// flip readiness to 503 while Postgres is reachable.
func TestReadyz_VLLMUnreachableIsNonFatal(t *testing.T) {
	h := newHealthRouter(HealthDeps{DB: fakePinger{}, VLLMProbe: badProbe})
	code, body := getJSON(t, h, "/readyz")
	if code != http.StatusOK {
		t.Fatalf("/readyz = %d, want 200 (vLLM is non-fatal)", code)
	}
	checks := body["checks"].(map[string]any)
	if checks["vllm"] != "unreachable" {
		t.Errorf("vllm should be reported unreachable: %v", body)
	}
	if body["status"] != "ready" {
		t.Errorf("readiness should stay ready when only vLLM is down: %v", body)
	}
}

func TestReadyz_NoDBConfiguredIsNotReady(t *testing.T) {
	h := newHealthRouter(HealthDeps{Version: "dev"})
	code, body := getJSON(t, h, "/readyz")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz with no DB = %d, want 503", code)
	}
	checks := body["checks"].(map[string]any)
	if checks["postgres"] != "unconfigured" || checks["vllm"] != "skipped" {
		t.Errorf("unexpected checks with no deps: %v", body)
	}
}

// TestNewVLLMProbe hits a stub upstream: a 200 is reachable, a 500 (or transport
// error) is unreachable.
func TestNewVLLMProbe(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("probe hit %q, want /health", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer up.Close()

	if err := NewVLLMProbe(up.URL)(context.Background()); err != nil {
		t.Errorf("probe against healthy upstream: %v", err)
	}

	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer down.Close()
	if err := NewVLLMProbe(down.URL)(context.Background()); err == nil {
		t.Error("probe against 500 upstream should report unreachable")
	}
}
