// Package log provides the gateway's structured logger (built on log/slog) and
// helpers to carry a request-scoped logger and request ID through a
// context.Context.
//
// JSON output is used in production and human-friendly text output elsewhere;
// the level comes from configuration. The package never logs request bodies or
// secrets — callers decide what to attach, and the request middleware logs only
// metadata (method, path, status, duration, bytes, request id).
package log

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/config"
)

// New builds a logger from configuration, writing to os.Stdout.
func New(cfg config.Config) *slog.Logger {
	return NewWithWriter(cfg, os.Stdout)
}

// NewWithWriter is New with an explicit destination, used by tests.
func NewWithWriter(cfg config.Config, w io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(cfg.LogLevel)}
	var h slog.Handler
	if strings.EqualFold(cfg.Env, "production") {
		h = slog.NewJSONHandler(w, opts)
	} else {
		h = slog.NewTextHandler(w, opts)
	}
	return slog.New(h)
}

// parseLevel maps a LOG_LEVEL string to a slog.Level, defaulting to info.
func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

type ctxKey int

const (
	loggerKey ctxKey = iota
	requestIDKey
)

// WithContext returns a copy of ctx carrying the request-scoped logger.
func WithContext(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey, logger)
}

// FromContext returns the request-scoped logger, or slog.Default() if none is
// attached. The result is never nil.
func FromContext(ctx context.Context) *slog.Logger {
	return FromContextOr(ctx, nil)
}

// FromContextOr returns the request-scoped logger attached to ctx, or fallback
// when none is attached. If fallback is also nil it returns slog.Default(). The
// result is never nil. This lets a caller supply its own default (e.g. a
// component logger) instead of the global one when the context carries no
// request-scoped logger.
func FromContextOr(ctx context.Context, fallback *slog.Logger) *slog.Logger {
	if l, ok := ctx.Value(loggerKey).(*slog.Logger); ok && l != nil {
		return l
	}
	if fallback != nil {
		return fallback
	}
	return slog.Default()
}

// withRequestID stores the request ID on the context.
func withRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestID returns the request ID stored on ctx, or "" if none.
func RequestID(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey).(string); ok {
		return id
	}
	return ""
}
