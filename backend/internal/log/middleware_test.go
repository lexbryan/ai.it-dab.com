package log

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/config"
)

func prodLogger(buf *bytes.Buffer) *slog.Logger {
	return NewWithWriter(config.Config{Env: "production", LogLevel: "debug"}, buf)
}

func serve(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// bareWriter implements http.ResponseWriter and nothing else — no Flusher, no
// Hijacker — so tests can assert the wrapper does not invent capabilities.
type bareWriter struct{ header http.Header }

func (b *bareWriter) Header() http.Header {
	if b.header == nil {
		b.header = http.Header{}
	}
	return b.header
}
func (b *bareWriter) Write(p []byte) (int, error) { return len(p), nil }
func (b *bareWriter) WriteHeader(int)             {}

func TestMiddleware_LogsOneSummaryLine(t *testing.T) {
	var buf bytes.Buffer
	h := Middleware(prodLogger(&buf))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, "hello")
	}))

	serve(h, httptest.NewRequest(http.MethodGet, "/widgets", nil))

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected exactly one summary line, got %d: %q", len(lines), buf.String())
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("summary line is not JSON: %v", err)
	}
	if rec["msg"] != "request" || rec["method"] != "GET" || rec["path"] != "/widgets" {
		t.Errorf("unexpected fields: %v", rec)
	}
	if rec["status"] != float64(http.StatusCreated) {
		t.Errorf("status = %v, want 201", rec["status"])
	}
	if rec["bytes"] != float64(5) {
		t.Errorf("bytes = %v, want 5", rec["bytes"])
	}
	if id, _ := rec["request_id"].(string); id == "" {
		t.Error("request_id should be present and non-empty")
	}
	if v, ok := rec["duration_ms"].(float64); !ok || v < 0 {
		t.Errorf("duration_ms should be a non-negative number, got %v", rec["duration_ms"])
	}
}

func TestMiddleware_RecorderStatusAndBytes(t *testing.T) {
	t.Run("implicit 200 when handler only writes", func(t *testing.T) {
		var buf bytes.Buffer
		h := Middleware(prodLogger(&buf))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "abcd")
		}))
		serve(h, httptest.NewRequest(http.MethodGet, "/", nil))

		rec := decodeLine(t, &buf)
		if rec["status"] != float64(http.StatusOK) {
			t.Errorf("status = %v, want 200", rec["status"])
		}
		if rec["bytes"] != float64(4) {
			t.Errorf("bytes = %v, want 4", rec["bytes"])
		}
	})

	t.Run("first WriteHeader wins and logs one line", func(t *testing.T) {
		var buf bytes.Buffer
		h := Middleware(prodLogger(&buf))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.WriteHeader(http.StatusOK) // must be ignored
		}))
		serve(h, httptest.NewRequest(http.MethodGet, "/", nil))

		if lines := strings.Split(strings.TrimSpace(buf.String()), "\n"); len(lines) != 1 {
			t.Fatalf("expected one line, got %d: %q", len(lines), buf.String())
		}
		rec := decodeLine(t, &buf)
		if rec["status"] != float64(http.StatusServiceUnavailable) {
			t.Errorf("status = %v, want 503 (first WriteHeader wins)", rec["status"])
		}
	})
}

func TestMiddleware_RequestIDGeneratedAndPropagated(t *testing.T) {
	noop := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})

	t.Run("generated when absent", func(t *testing.T) {
		var buf bytes.Buffer
		rr := serve(Middleware(prodLogger(&buf))(noop), httptest.NewRequest(http.MethodGet, "/", nil))
		got := rr.Header().Get(HeaderRequestID)
		if got == "" {
			t.Fatal("response should carry a generated X-Request-Id")
		}
		if !strings.Contains(buf.String(), got) {
			t.Errorf("log should use the same request id %q: %s", got, buf.String())
		}
	})

	t.Run("propagated when present", func(t *testing.T) {
		var buf bytes.Buffer
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(HeaderRequestID, "client-123")
		rr := serve(Middleware(prodLogger(&buf))(noop), req)
		if got := rr.Header().Get(HeaderRequestID); got != "client-123" {
			t.Errorf("response X-Request-Id = %q, want client-123", got)
		}
		if !strings.Contains(buf.String(), "client-123") {
			t.Errorf("log should use inbound request id: %s", buf.String())
		}
	})
}

func TestMiddleware_StatusLevels(t *testing.T) {
	cases := map[int]string{
		http.StatusOK:                  "INFO",
		http.StatusNotFound:            "WARN",
		http.StatusBadRequest:          "WARN",
		http.StatusInternalServerError: "ERROR",
		http.StatusServiceUnavailable:  "ERROR",
	}
	for status, wantLevel := range cases {
		var buf bytes.Buffer
		h := Middleware(prodLogger(&buf))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		}))
		serve(h, httptest.NewRequest(http.MethodGet, "/", nil))

		var rec map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &rec); err != nil {
			t.Fatalf("status %d: not JSON: %v", status, err)
		}
		if rec["level"] != wantLevel {
			t.Errorf("status %d => level %v, want %s", status, rec["level"], wantLevel)
		}
	}
}

// TestMiddleware_AdvertisesCapabilitiesConditionally proves the wrapper only
// claims http.Flusher / http.Hijacker when the underlying writer truly supports
// them, and that Hijack actually works on the positive path.
func TestMiddleware_AdvertisesCapabilitiesConditionally(t *testing.T) {
	t.Run("real server: flusher+hijacker, Hijack works", func(t *testing.T) {
		h := Middleware(prodLogger(&bytes.Buffer{}))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := w.(http.Flusher); !ok {
				t.Error("real-server writer must advertise http.Flusher")
			}
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Error("real-server writer must advertise http.Hijacker")
				return
			}
			conn, brw, err := hj.Hijack()
			if err != nil {
				t.Errorf("Hijack() error = %v, want nil", err)
				return
			}
			defer func() { _ = conn.Close() }()
			_, _ = brw.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nhi")
			_ = brw.Flush()
		}))
		srv := httptest.NewServer(h)
		defer srv.Close()

		resp, err := http.Get(srv.URL)
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		if string(body) != "hi" {
			t.Errorf("hijacked response body = %q, want hi", body)
		}
	})

	t.Run("flusher-only writer does not advertise hijacker", func(t *testing.T) {
		// httptest.ResponseRecorder is an http.Flusher but not an http.Hijacker.
		var gotFlusher, gotHijacker bool
		h := Middleware(prodLogger(&bytes.Buffer{}))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, gotFlusher = w.(http.Flusher)
			_, gotHijacker = w.(http.Hijacker)
		}))
		serve(h, httptest.NewRequest(http.MethodGet, "/", nil))
		if !gotFlusher {
			t.Error("underlying recorder is a Flusher; wrapper should advertise it")
		}
		if gotHijacker {
			t.Error("underlying recorder is NOT a Hijacker; wrapper must not advertise it")
		}
	})

	t.Run("bare writer advertises neither", func(t *testing.T) {
		var gotFlusher, gotHijacker bool
		h := Middleware(prodLogger(&bytes.Buffer{}))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, gotFlusher = w.(http.Flusher)
			_, gotHijacker = w.(http.Hijacker)
		}))
		h.ServeHTTP(&bareWriter{}, httptest.NewRequest(http.MethodGet, "/", nil))
		if gotFlusher || gotHijacker {
			t.Errorf("bare writer supports neither; wrapper advertised flusher=%v hijacker=%v", gotFlusher, gotHijacker)
		}
	})
}

// TestMiddleware_FlushBeforeWriteHeader covers the SSE "flush headers first"
// pattern: a Flush with no prior WriteHeader must commit a 200 to both the wire
// and the log, and a later WriteHeader must be ignored (no divergence, no
// stdlib "superfluous WriteHeader" warning).
func TestMiddleware_FlushBeforeWriteHeader(t *testing.T) {
	var buf bytes.Buffer
	h := Middleware(prodLogger(&buf))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("recorder should be an http.Flusher")
		}
		f.Flush()                                     // commits implicit 200
		w.WriteHeader(http.StatusInternalServerError) // must be ignored
		_, _ = io.WriteString(w, "data")
	}))
	rr := serve(h, httptest.NewRequest(http.MethodGet, "/", nil))

	if rr.Code != http.StatusOK {
		t.Errorf("wire status = %d, want 200", rr.Code)
	}
	rec := decodeLine(t, &buf)
	if rec["status"] != float64(http.StatusOK) {
		t.Errorf("logged status = %v, want 200 (must match wire)", rec["status"])
	}
}

func TestMiddleware_FlushReachesClientImmediately(t *testing.T) {
	released := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(released) }) }
	defer release()

	h := Middleware(NewWithWriter(config.Config{Env: "production", LogLevel: "error"}, io.Discard))(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			f, ok := w.(http.Flusher)
			if !ok {
				t.Error("wrapped writer is not http.Flusher")
				return
			}
			_, _ = io.WriteString(w, "chunk-1\n")
			f.Flush()
			select {
			case <-released:
			case <-time.After(2 * time.Second): // safety: never block the handler goroutine forever
			}
			_, _ = io.WriteString(w, "chunk-2\n")
		}))

	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Read the first chunk under a hard timeout so a buffering regression fails
	// cleanly here instead of deadlocking into a whole-package test timeout.
	first := make([]byte, len("chunk-1\n"))
	done := make(chan error, 1)
	go func() { _, err := io.ReadFull(resp.Body, first); done <- err }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("reading flushed first chunk: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("flushed chunk did not arrive within 2s -> response was buffered")
	}
	if string(first) != "chunk-1\n" {
		t.Fatalf("first chunk = %q, want chunk-1", first)
	}

	// Received the flushed chunk while the handler is still blocked => not buffered.
	release()
	rest, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(rest), "chunk-2") {
		t.Errorf("did not receive second chunk: %q", rest)
	}
}

func TestMiddleware_DoesNotLogResponseBody(t *testing.T) {
	var buf bytes.Buffer
	const bodyToken = "do-not-log-this-body-token"
	h := Middleware(prodLogger(&buf))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, bodyToken)
	}))
	serve(h, httptest.NewRequest(http.MethodGet, "/", nil))

	if strings.Contains(buf.String(), bodyToken) {
		t.Errorf("response body leaked into logs: %s", buf.String())
	}
}

// TestMiddleware_DoesNotLogSecrets guards the no-leak acceptance criterion
// across every channel a regression might log: request body, query string, and
// headers. The summary logs only method + URL path, so none should appear.
func TestMiddleware_DoesNotLogSecrets(t *testing.T) {
	var buf bytes.Buffer
	const (
		bodySecret   = "SECRET_BODY_aaa"
		querySecret  = "SECRET_QUERY_bbb"
		headerSecret = "SECRET_HEADER_ccc"
	)
	h := Middleware(prodLogger(&buf))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat?token="+querySecret, strings.NewReader(bodySecret))
	req.Header.Set("Authorization", "Bearer "+headerSecret)
	serve(h, req)

	out := buf.String()
	for _, secret := range []string{bodySecret, querySecret, headerSecret} {
		if strings.Contains(out, secret) {
			t.Errorf("secret %q leaked into logs: %s", secret, out)
		}
	}
	// The path itself is fine to log.
	if rec := decodeLine(t, &buf); rec["path"] != "/v1/chat" {
		t.Errorf("path = %v, want /v1/chat", rec["path"])
	}
}

func TestMiddleware_ConcurrentRequestsDistinctIDs(t *testing.T) {
	var mu sync.Mutex
	var buf bytes.Buffer
	mw := Middleware(NewWithWriter(config.Config{Env: "production", LogLevel: "info"},
		writerFunc(func(p []byte) (int, error) {
			mu.Lock()
			defer mu.Unlock()
			return buf.Write(p)
		})))
	srv := httptest.NewServer(mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))
	defer srv.Close()

	const n = 25
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			resp, err := http.Get(srv.URL)
			if err != nil {
				return
			}
			_ = resp.Body.Close()
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	ids := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		if id, _ := rec["request_id"].(string); id != "" {
			ids[id] = true
		}
	}
	if len(ids) != n {
		t.Errorf("got %d distinct request_ids across %d requests, want %d", len(ids), n, n)
	}
}

// decodeLine parses the single JSON summary line currently in buf.
func decodeLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var rec map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &rec); err != nil {
		t.Fatalf("log line is not JSON: %v (%q)", err, buf.String())
	}
	return rec
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
