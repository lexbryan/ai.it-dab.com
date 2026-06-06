package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/config"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakePool stands in for *pgxpool.Pool's Close() so the lifecycle can be tested
// without a real database.
type fakePool struct{ closed atomic.Bool }

func (p *fakePool) Close() { p.closed.Store(true) }

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
	h := buildHandler(testConfig(), discardLogger(), nil)

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

// On a signal the server stops accepting, drains the in-flight request, and only
// then closes the pool — and refuses new connections afterward.
func TestRunServer_DrainsInFlightThenClosesPool(t *testing.T) {
	started := make(chan struct{})
	var poolOpenDuringRequest atomic.Bool
	pool := &fakePool{}

	mux := http.NewServeMux()
	mux.HandleFunc("/slow", func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		time.Sleep(150 * time.Millisecond)
		poolOpenDuringRequest.Store(!pool.closed.Load()) // the pool must still be open mid-request
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	srv := &http.Server{Handler: mux}
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() { errCh <- runServer(ctx, ln, srv, pool, 2*time.Second, discardLogger()) }()

	respCh := make(chan *http.Response, 1)
	go func() {
		resp, err := http.Get("http://" + addr + "/slow")
		if err != nil {
			respCh <- nil
			return
		}
		respCh <- resp
	}()

	<-started // the request is in-flight
	cancel()  // simulate SIGTERM

	resp := <-respCh
	if resp == nil || resp.StatusCode != http.StatusOK {
		t.Fatal("in-flight request did not drain to completion")
	}
	_ = resp.Body.Close()

	if err := <-errCh; err != nil {
		t.Fatalf("runServer = %v, want nil on a clean shutdown", err)
	}
	if !poolOpenDuringRequest.Load() {
		t.Error("pool was closed before the in-flight request finished")
	}
	if !pool.closed.Load() {
		t.Error("pool was not closed after shutdown")
	}
	if _, err := http.Get("http://" + addr + "/slow"); err == nil {
		t.Error("server still accepting connections after shutdown")
	}
}

// A drain that exceeds the grace period is force-closed: runServer returns within
// roughly the grace window (not the full handler duration) with an error, and the
// pool is still released.
func TestRunServer_ShutdownBoundedByGrace(t *testing.T) {
	hung := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/hang", func(w http.ResponseWriter, _ *http.Request) {
		close(hung)
		time.Sleep(2 * time.Second) // far longer than the grace below
		w.WriteHeader(http.StatusOK)
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	srv := &http.Server{Handler: mux}
	pool := &fakePool{}
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() { errCh <- runServer(ctx, ln, srv, pool, 50*time.Millisecond, discardLogger()) }()

	go func() { _, _ = http.Get("http://" + addr + "/hang") }()
	<-hung
	start := time.Now()
	cancel()

	err = <-errCh
	elapsed := time.Since(start)
	if err == nil {
		t.Error("runServer should return an error when the drain exceeds grace")
	}
	if elapsed > time.Second {
		t.Errorf("shutdown took %v; it should be bounded by the grace period, not the hung handler", elapsed)
	}
	if !pool.closed.Load() {
		t.Error("pool must be closed even after a forced shutdown")
	}
}

// A listen/serve failure is surfaced as an error, and the pool is still released.
func TestRunServer_ReturnsServeError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_ = ln.Close() // a closed listener makes Serve fail immediately

	pool := &fakePool{}
	err = runServer(context.Background(), ln, &http.Server{Handler: http.NewServeMux()}, pool, time.Second, discardLogger())
	if err == nil {
		t.Error("runServer should return the Serve error for a dead listener")
	}
	if !pool.closed.Load() {
		t.Error("pool must be closed even when Serve fails")
	}
}

// Invalid configuration aborts startup with an error (which main turns into a
// non-zero exit) before any database or listener work.
func TestRun_ConfigErrorAbortsStartup(t *testing.T) {
	for _, k := range []string{"DATABASE_URL", "VLLM_URL", "VLLM_API_KEY", "JWT_SECRET"} {
		t.Setenv(k, "")
	}
	if err := run(context.Background()); err == nil {
		t.Error("run should return an error when configuration is invalid")
	}
}
