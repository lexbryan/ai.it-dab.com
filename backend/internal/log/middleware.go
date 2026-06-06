package log

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"
)

// HeaderRequestID is the header used to read an inbound request ID and to echo
// the resolved one back on the response.
const HeaderRequestID = "X-Request-Id"

// Middleware returns request-logging middleware. For each request it:
//   - resolves a request ID (inbound X-Request-Id header, else a fresh random
//     id) and echoes it on the response,
//   - attaches a request-scoped logger and the request ID to the context,
//   - logs exactly one summary line per request (method, path, status,
//     duration, bytes, request id), with level by status class
//     (5xx=error, 4xx=warn, else info).
//
// The wrapped ResponseWriter preserves http.Flusher and http.Hijacker — but
// only when the underlying writer actually supports them — so streaming (SSE)
// responses are not buffered, capability-sniffing handlers see the truth, and
// the wrapper never captures the body.
func Middleware(base *slog.Logger) func(http.Handler) http.Handler {
	if base == nil {
		base = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqID := r.Header.Get(HeaderRequestID)
			if reqID == "" {
				reqID = newRequestID()
			}
			w.Header().Set(HeaderRequestID, reqID)

			reqLogger := base.With(slog.String("request_id", reqID))
			ctx := withRequestID(WithContext(r.Context(), reqLogger), reqID)

			rec, rw := wrapResponseWriter(w)
			start := time.Now()
			next.ServeHTTP(rw, r.WithContext(ctx))
			dur := time.Since(start)

			level := slog.LevelInfo
			switch {
			case rec.status >= 500:
				level = slog.LevelError
			case rec.status >= 400:
				level = slog.LevelWarn
			}
			reqLogger.LogAttrs(ctx, level, "request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rec.status),
				slog.Int64("duration_ms", dur.Milliseconds()),
				slog.Int("bytes", rec.bytes),
			)
		})
	}
}

// newRequestID returns a random 128-bit hex request id.
func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand essentially never fails; fall back to a time-based id.
		return "req-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

// responseRecorder wraps an http.ResponseWriter to capture the status code and
// number of bytes written. It passes writes straight through — it never buffers
// the body. It deliberately does NOT implement http.Flusher/http.Hijacker
// itself; those are added conditionally by wrapResponseWriter so the wrapper
// advertises a capability only when the underlying writer truly has it.
type responseRecorder struct {
	http.ResponseWriter
	status      int
	bytes       int
	wroteHeader bool
}

func (r *responseRecorder) WriteHeader(code int) {
	if r.wroteHeader {
		return
	}
	r.status = code
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(code)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// Unwrap exposes the underlying writer so http.ResponseController can reach
// capabilities (deadlines, etc.) that this recorder does not itself wrap.
func (r *responseRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// flush commits an implicit 200 (so the logged status matches the wire status
// and a later WriteHeader is correctly ignored) and then flushes the underlying
// writer. Only ever called when the underlying writer is an http.Flusher.
func (r *responseRecorder) flush() {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// hijack delegates to the underlying writer. Only ever wired up when the
// underlying writer is an http.Hijacker.
func (r *responseRecorder) hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := r.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, errors.New("underlying ResponseWriter does not support Hijack")
}

// The conditional-capability wrappers below each embed *responseRecorder (so
// Header/Write/WriteHeader/Unwrap promote and status/bytes are captured) and
// add exactly the streaming interface(s) the underlying writer supports.

type flushWriter struct{ *responseRecorder }

func (w flushWriter) Flush() { w.responseRecorder.flush() }

type hijackWriter struct{ *responseRecorder }

func (w hijackWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.responseRecorder.hijack()
}

type flushHijackWriter struct{ *responseRecorder }

func (w flushHijackWriter) Flush() { w.responseRecorder.flush() }
func (w flushHijackWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.responseRecorder.hijack()
}

// wrapResponseWriter builds the status/bytes recorder and selects a wrapper that
// advertises http.Flusher and/or http.Hijacker iff w itself supports them.
func wrapResponseWriter(w http.ResponseWriter) (*responseRecorder, http.ResponseWriter) {
	rec := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
	_, canFlush := w.(http.Flusher)
	_, canHijack := w.(http.Hijacker)
	switch {
	case canFlush && canHijack:
		return rec, flushHijackWriter{rec}
	case canFlush:
		return rec, flushWriter{rec}
	case canHijack:
		return rec, hijackWriter{rec}
	default:
		return rec, rec
	}
}
