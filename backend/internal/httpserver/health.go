package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Pinger is the readiness dependency the health endpoints require: a bounded
// connectivity check. *pgxpool.Pool satisfies it.
type Pinger interface {
	Ping(ctx context.Context) error
}

// HealthDeps configures the health endpoints. DB is the required readiness
// dependency (nil reports the process as not ready). VLLMProbe is an optional,
// non-fatal upstream signal. Version is echoed for ops.
type HealthDeps struct {
	DB        Pinger
	VLLMProbe func(context.Context) error
	Version   string
	// Timeout bounds each readiness check so a slow dependency can never hang
	// the endpoint. Defaults to 2s.
	Timeout time.Duration
}

// RegisterHealth mounts GET /healthz (liveness) and GET /readyz (readiness) on
// the router. Both are unauthenticated — compose healthchecks and load
// balancers must reach them without credentials.
func RegisterHealth(r *Router, deps HealthDeps) {
	if deps.Timeout <= 0 {
		deps.Timeout = 2 * time.Second
	}
	r.HandleFunc("GET /healthz", livenessHandler(deps.Version))
	r.HandleFunc("GET /readyz", readinessHandler(deps))
}

// livenessHandler reports that the process is up. It makes no external calls and
// always returns 200 — it answers "is the process running", not "are its
// dependencies healthy".
func livenessHandler(version string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "ok",
			"version": version,
		})
	}
}

// readinessHandler reports whether required dependencies are reachable. Postgres
// gates readiness: if its bounded Ping fails (or no DB is configured) the
// endpoint returns 503 with a body naming the failing dependency. The vLLM probe
// is non-fatal — its result is surfaced in the body but never flips readiness to
// 503, because the gateway can still serve admin traffic while the upstream is
// down, and a slow upstream must not hang readiness.
func readinessHandler(deps HealthDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		checks := make(map[string]string, 2)
		ready := true

		switch {
		case deps.DB == nil:
			checks["postgres"] = "unconfigured"
			ready = false
		default:
			ctx, cancel := context.WithTimeout(r.Context(), deps.Timeout)
			if err := deps.DB.Ping(ctx); err != nil {
				checks["postgres"] = "unreachable"
				ready = false
			} else {
				checks["postgres"] = "ok"
			}
			cancel()
		}

		if deps.VLLMProbe != nil {
			ctx, cancel := context.WithTimeout(r.Context(), deps.Timeout)
			if err := deps.VLLMProbe(ctx); err != nil {
				checks["vllm"] = "unreachable"
			} else {
				checks["vllm"] = "ok"
			}
			cancel()
		} else {
			checks["vllm"] = "skipped"
		}

		status, code := "ready", http.StatusOK
		if !ready {
			status, code = "not_ready", http.StatusServiceUnavailable
		}
		writeJSON(w, code, map[string]any{
			"status":  status,
			"checks":  checks,
			"version": deps.Version,
		})
	}
}

// NewVLLMProbe returns a non-fatal upstream probe that GETs <vllmURL>/health
// with a per-call deadline. A transport error or non-2xx status reports the
// upstream as unreachable. It never carries or logs the VLLM_API_KEY — the
// health endpoint is for reachability, not authentication.
func NewVLLMProbe(vllmURL string) func(context.Context) error {
	client := &http.Client{}
	target := strings.TrimRight(vllmURL, "/") + "/health"
	return func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("vllm: upstream returned status %d", resp.StatusCode)
		}
		return nil
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
