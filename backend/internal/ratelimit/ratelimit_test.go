package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/config"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
}

func requestFromIP(ip string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/admin/login", nil)
	r.RemoteAddr = ip + ":40000"
	return r
}

func TestPerIP_LimitsThenRejectsWithRetryAfter(t *testing.T) {
	// Burst of 2: two requests pass immediately, the third is limited.
	mw := PerIP(config.RateLimit{RequestsPerMinute: 60, Burst: 2})(okHandler())

	for i := 0; i < 2; i++ {
		rr := httptest.NewRecorder()
		mw.ServeHTTP(rr, requestFromIP("203.0.113.7"))
		if rr.Code != http.StatusOK {
			t.Fatalf("request %d = %d, want 200 (within burst)", i+1, rr.Code)
		}
	}
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, requestFromIP("203.0.113.7"))
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("over-budget = %d, want 429", rr.Code)
	}
	if rr.Header().Get("Retry-After") == "" {
		t.Error("429 must include a Retry-After header")
	}
}

func TestPerIP_DifferentIPsIndependent(t *testing.T) {
	mw := PerIP(config.RateLimit{RequestsPerMinute: 60, Burst: 1})(okHandler())

	// Exhaust IP A.
	mw.ServeHTTP(httptest.NewRecorder(), requestFromIP("198.51.100.1"))
	exhausted := httptest.NewRecorder()
	mw.ServeHTTP(exhausted, requestFromIP("198.51.100.1"))
	if exhausted.Code != http.StatusTooManyRequests {
		t.Fatalf("IP A second request = %d, want 429", exhausted.Code)
	}

	// IP B is unaffected.
	other := httptest.NewRecorder()
	mw.ServeHTTP(other, requestFromIP("198.51.100.2"))
	if other.Code != http.StatusOK {
		t.Errorf("IP B = %d, want 200 (independent bucket)", other.Code)
	}
}

func TestPerKey_OverBudgetThenSecondKeyPasses(t *testing.T) {
	keyFn := func(r *http.Request) string { return r.Header.Get("X-Key") }
	mw := PerKey(config.RateLimit{RequestsPerMinute: 60, Burst: 1}, keyFn)(okHandler())

	req := func(key string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/v1/gateway/chat", nil)
		r.Header.Set("X-Key", key)
		mw.ServeHTTP(rr, r)
		return rr
	}

	if got := req("key-a"); got.Code != http.StatusOK {
		t.Fatalf("first key-a = %d, want 200", got.Code)
	}
	if got := req("key-a"); got.Code != http.StatusTooManyRequests {
		t.Fatalf("second key-a = %d, want 429", got.Code)
	}
	// A different credential has its own bucket.
	if got := req("key-b"); got.Code != http.StatusOK {
		t.Errorf("key-b = %d, want 200", got.Code)
	}
}

func TestPerKey_EmptyKeyPassesThrough(t *testing.T) {
	keyFn := func(*http.Request) string { return "" }
	mw := PerKey(config.RateLimit{RequestsPerMinute: 1, Burst: 1}, keyFn)(okHandler())
	for i := 0; i < 5; i++ {
		rr := httptest.NewRecorder()
		mw.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/v1/gateway/chat", nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("empty-key request %d = %d, want 200 (passes through)", i, rr.Code)
		}
	}
}

func TestDisabledConfigIsPassThrough(t *testing.T) {
	mw := PerIP(config.RateLimit{RequestsPerMinute: 0, Burst: 0})(okHandler())
	for i := 0; i < 10; i++ {
		rr := httptest.NewRecorder()
		mw.ServeHTTP(rr, requestFromIP("203.0.113.9"))
		if rr.Code != http.StatusOK {
			t.Fatalf("disabled limiter request %d = %d, want 200", i, rr.Code)
		}
	}
}

// TestPerKey_StreamingPathUnaffected confirms the admission check does not wrap
// the ResponseWriter: a permitted handler still sees an http.Flusher.
func TestPerKey_StreamingPathUnaffected(t *testing.T) {
	var sawFlusher bool
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, sawFlusher = w.(http.Flusher)
		w.WriteHeader(http.StatusOK)
	})
	mw := PerKey(config.RateLimit{RequestsPerMinute: 60, Burst: 5}, func(*http.Request) string { return "k" })(h)

	rr := httptest.NewRecorder() // httptest recorder is an http.Flusher
	mw.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/v1/gateway/chat", nil))
	if !sawFlusher {
		t.Error("limiter must not strip http.Flusher from a permitted streaming request")
	}
}
