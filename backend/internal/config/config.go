// Package config loads and validates the gateway's runtime configuration from
// the environment.
//
// It is the single source of truth for environment validation: it is the only
// place that enforces required variables — including that secrets such as
// JWT_SECRET and VLLM_API_KEY are present and non-empty. Other packages consume
// the validated Config and must not re-validate environment variables.
//
// Dependency choice: this package uses only the Go standard library
// (os, net/url, strconv, strings, time, errors, fmt). A third-party config
// library is intentionally avoided — the configuration surface is small and the
// stdlib covers parsing, defaulting, and validation without adding dependencies.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Environment variable names consumed by Load.
const (
	EnvAppEnv             = "ENV"
	EnvListenAddr         = "LISTEN_ADDR"
	EnvPort               = "PORT"
	EnvLogLevel           = "LOG_LEVEL"
	EnvDatabaseURL        = "DATABASE_URL"
	EnvAutoMigrate        = "AUTO_MIGRATE"
	EnvVLLMURL            = "VLLM_URL"
	EnvVLLMAPIKey         = "VLLM_API_KEY"
	EnvJWTSecret          = "JWT_SECRET"
	EnvCORSAllowedOrigins = "CORS_ALLOWED_ORIGINS"
	EnvShutdownGrace      = "SHUTDOWN_GRACE"
	EnvLoginRateRPM       = "LOGIN_RATE_LIMIT_RPM"
	EnvLoginRateBurst     = "LOGIN_RATE_LIMIT_BURST"
	EnvGatewayRateRPM     = "GATEWAY_RATE_LIMIT_RPM"
	EnvGatewayRateBurst   = "GATEWAY_RATE_LIMIT_BURST"
)

// EnvKeys is every environment variable Load reads. The drift test asserts that
// .env.example documents each of them.
var EnvKeys = []string{
	EnvAppEnv, EnvListenAddr, EnvPort, EnvLogLevel, EnvDatabaseURL,
	EnvAutoMigrate, EnvVLLMURL, EnvVLLMAPIKey, EnvJWTSecret,
	EnvCORSAllowedOrigins, EnvShutdownGrace,
	EnvLoginRateRPM, EnvLoginRateBurst, EnvGatewayRateRPM, EnvGatewayRateBurst,
}

// Defaults applied when an optional variable is unset. Secrets and the DSN have
// no defaults — they are always required.
const (
	defaultListenAddr    = ":8080"
	defaultLogLevel      = "info"
	defaultEnv           = "development"
	defaultShutdownGrace = 10 * time.Second
	defaultLoginRPM      = 10
	defaultLoginBurst    = 5
	defaultGatewayRPM    = 120
	defaultGatewayBurst  = 40
)

// RateLimit carries a configured token-bucket rate-limit knob. The limiter
// itself is implemented in its own ticket; this type only holds the values.
type RateLimit struct {
	RequestsPerMinute int
	Burst             int
}

// Config is the validated runtime configuration. A Config returned by Load with
// a nil error is guaranteed to have all required fields present.
type Config struct {
	Env                string
	ListenAddr         string
	LogLevel           string
	DatabaseURL        string
	AutoMigrate        bool
	VLLMURL            string
	VLLMAPIKey         string
	JWTSecret          string
	CORSAllowedOrigins []string
	ShutdownGrace      time.Duration
	LoginRateLimit     RateLimit
	GatewayRateLimit   RateLimit
}

// Load reads configuration from the environment, applies defaults, and
// validates it. On any problem it returns a single aggregated error naming
// every offending variable; the returned Config is meaningful only when the
// error is nil.
func Load() (Config, error) {
	var errs []error

	cfg := Config{
		Env:      getenvDefault(EnvAppEnv, defaultEnv),
		LogLevel: getenvDefault(EnvLogLevel, defaultLogLevel),
	}

	cfg.ListenAddr = resolveListenAddr(&errs)

	// Required secrets and DSN — no defaults, must be present and non-empty.
	cfg.DatabaseURL = requireNonEmpty(EnvDatabaseURL, &errs)
	cfg.VLLMAPIKey = requireNonEmpty(EnvVLLMAPIKey, &errs)
	cfg.JWTSecret = requireNonEmpty(EnvJWTSecret, &errs)
	cfg.VLLMURL = requireNonEmpty(EnvVLLMURL, &errs)

	if cfg.VLLMURL != "" {
		validateHTTPURL(EnvVLLMURL, cfg.VLLMURL, &errs)
	}
	if cfg.DatabaseURL != "" {
		validateDatabaseURL(cfg.DatabaseURL, &errs)
	}

	cfg.AutoMigrate = parseBoolDefault(EnvAutoMigrate, false, &errs)
	cfg.CORSAllowedOrigins = splitCSV(os.Getenv(EnvCORSAllowedOrigins))
	cfg.ShutdownGrace = parseDurationDefault(EnvShutdownGrace, defaultShutdownGrace, &errs)

	cfg.LoginRateLimit = RateLimit{
		RequestsPerMinute: parseIntDefault(EnvLoginRateRPM, defaultLoginRPM, &errs),
		Burst:             parseIntDefault(EnvLoginRateBurst, defaultLoginBurst, &errs),
	}
	cfg.GatewayRateLimit = RateLimit{
		RequestsPerMinute: parseIntDefault(EnvGatewayRateRPM, defaultGatewayRPM, &errs),
		Burst:             parseIntDefault(EnvGatewayRateBurst, defaultGatewayBurst, &errs),
	}

	if len(errs) > 0 {
		return Config{}, fmt.Errorf("invalid configuration: %w", errors.Join(errs...))
	}
	return cfg, nil
}

// resolveListenAddr prefers LISTEN_ADDR, then derives ":PORT" (validating the
// port range), then falls back to the default.
func resolveListenAddr(errs *[]error) string {
	if addr := strings.TrimSpace(os.Getenv(EnvListenAddr)); addr != "" {
		return addr
	}
	portStr := strings.TrimSpace(os.Getenv(EnvPort))
	if portStr == "" {
		return defaultListenAddr
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		*errs = append(*errs, fmt.Errorf("%s: must be an integer in 1..65535, got %q", EnvPort, portStr))
		return defaultListenAddr
	}
	return fmt.Sprintf(":%d", port)
}

func getenvDefault(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func requireNonEmpty(key string, errs *[]error) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		*errs = append(*errs, fmt.Errorf("%s: required but missing or empty", key))
	}
	return v
}

// validateDatabaseURL checks a Postgres DSN. URL-form DSNs must use a
// postgres(ql) scheme with a non-empty host; libpq keyword/value DSNs (no
// "://") are accepted as-is. It never echoes the value or the raw parse error,
// since a malformed DSN may embed the password and this error is logged.
func validateDatabaseURL(raw string, errs *[]error) {
	if !strings.Contains(raw, "://") {
		return // keyword/value (libpq) DSN form
	}
	u, err := url.Parse(raw)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s: not a parseable Postgres URL", EnvDatabaseURL))
		return
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		*errs = append(*errs, fmt.Errorf("%s: must use a postgres:// or postgresql:// scheme, got %q", EnvDatabaseURL, u.Scheme))
		return
	}
	if u.Host == "" {
		*errs = append(*errs, fmt.Errorf("%s: missing host", EnvDatabaseURL))
	}
}

func validateHTTPURL(key, raw string, errs *[]error) {
	u, err := url.Parse(raw)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s: not a parseable URL: %v", key, err))
		return
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		*errs = append(*errs, fmt.Errorf("%s: must be an http(s) URL, got scheme %q", key, u.Scheme))
		return
	}
	if u.Host == "" {
		*errs = append(*errs, fmt.Errorf("%s: missing host in %q", key, raw))
	}
}

func parseBoolDefault(key string, def bool, errs *[]error) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s: must be a boolean (true/false), got %q", key, v))
		return def
	}
	return b
}

func parseIntDefault(key string, def int, errs *[]error) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		*errs = append(*errs, fmt.Errorf("%s: must be a non-negative integer, got %q", key, v))
		return def
	}
	return n
}

func parseDurationDefault(key string, def time.Duration, errs *[]error) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		*errs = append(*errs, fmt.Errorf("%s: must be a non-negative Go duration (e.g. 10s), got %q", key, v))
		return def
	}
	return d
}

func splitCSV(s string) []string {
	out := make([]string, 0)
	for _, p := range strings.Split(s, ",") {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
