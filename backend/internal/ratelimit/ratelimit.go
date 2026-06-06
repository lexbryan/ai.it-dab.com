// Package ratelimit provides in-process token-bucket rate limiting middleware:
// a per-IP limiter for the unauthenticated login endpoint (credential-stuffing
// defense) and a per-credential limiter for the public LLM gateway path.
//
// Limits come from configuration. A non-positive requests-per-minute disables
// the limiter (a pass-through), which is convenient for local development.
//
// This is in-process and therefore per-instance: with multiple gateway replicas
// each enforces its own buckets. A shared store (e.g. Redis) is the multi-
// instance follow-up.
package ratelimit

import (
	"math"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/config"
)

// staleAfter is how long an unused per-key bucket is retained before cleanup.
const staleAfter = 10 * time.Minute

// limiterSet holds one token-bucket limiter per key, with lazy cleanup of stale
// keys so the map cannot grow without bound.
type limiterSet struct {
	mu       sync.Mutex
	buckets  map[string]*bucket
	limit    rate.Limit
	burst    int
	now      func() time.Time
	lastSwep time.Time
}

type bucket struct {
	lim  *rate.Limiter
	seen time.Time
}

func newLimiterSet(rl config.RateLimit) *limiterSet {
	return &limiterSet{
		buckets: make(map[string]*bucket),
		limit:   rate.Limit(float64(rl.RequestsPerMinute) / 60.0),
		burst:   rl.Burst,
		now:     time.Now,
	}
}

// allow reports whether a request keyed by key may proceed, consuming a token.
func (s *limiterSet) allow(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	s.sweep(now)

	b, ok := s.buckets[key]
	if !ok {
		b = &bucket{lim: rate.NewLimiter(s.limit, s.burst)}
		s.buckets[key] = b
	}
	b.seen = now
	return b.lim.Allow()
}

// retryAfter is the whole-second hint for when a bucket may have a token again.
func (s *limiterSet) retryAfter() int {
	if s.limit <= 0 {
		return 1
	}
	secs := math.Ceil(1 / float64(s.limit))
	if secs < 1 {
		secs = 1
	}
	return int(secs)
}

func (s *limiterSet) sweep(now time.Time) {
	if now.Sub(s.lastSwep) < staleAfter {
		return
	}
	s.lastSwep = now
	for k, b := range s.buckets {
		if now.Sub(b.seen) > staleAfter {
			delete(s.buckets, k)
		}
	}
}

// PerIP returns middleware that limits by client IP, for the login endpoint. A
// non-positive RPM disables it (pass-through).
func PerIP(rl config.RateLimit) func(http.Handler) http.Handler {
	if rl.RequestsPerMinute <= 0 {
		return passthrough
	}
	set := newLimiterSet(rl)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !set.allow(clientIP(r)) {
				tooManyRequests(w, set.retryAfter())
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// PerKey returns middleware that limits by a key extracted from the request
// (e.g. the resolved api_key_id attached by the two-key middleware, which runs
// first). An empty key passes through. It only performs an admission check
// before the handler and never wraps the ResponseWriter, so a permitted
// streaming response is unaffected. A non-positive RPM disables it.
func PerKey(rl config.RateLimit, keyFn func(*http.Request) string) func(http.Handler) http.Handler {
	if rl.RequestsPerMinute <= 0 {
		return passthrough
	}
	set := newLimiterSet(rl)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if key := keyFn(r); key != "" && !set.allow(key) {
				tooManyRequests(w, set.retryAfter())
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func passthrough(next http.Handler) http.Handler { return next }

// clientIP uses the direct connection's remote address. Forwarded headers
// (X-Forwarded-For) are intentionally NOT trusted, since a client can spoof
// them to evade per-IP limits; behind a trusted proxy, parse them explicitly.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// tooManyRequests writes a generic 429 with a Retry-After header. The body is
// the gateway error envelope and never reveals whether an account exists.
func tooManyRequests(w http.ResponseWriter, retryAfterSeconds int) {
	w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusTooManyRequests)
	_, _ = w.Write([]byte(`{"error":{"type":"rate_limited","message":"too many requests"}}` + "\n"))
}
