package log

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/config"
)

func TestNewWithWriter_LevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	logger := NewWithWriter(config.Config{Env: "production", LogLevel: "warn"}, &buf)

	logger.Debug("debug-msg")
	logger.Info("info-msg")
	logger.Warn("warn-msg")
	logger.Error("error-msg")

	out := buf.String()
	if strings.Contains(out, "debug-msg") || strings.Contains(out, "info-msg") {
		t.Errorf("below-threshold records should be suppressed at warn level: %s", out)
	}
	if !strings.Contains(out, "warn-msg") || !strings.Contains(out, "error-msg") {
		t.Errorf("warn/error records should be emitted at warn level: %s", out)
	}
}

func TestNewWithWriter_JSONInProd(t *testing.T) {
	var buf bytes.Buffer
	logger := NewWithWriter(config.Config{Env: "production", LogLevel: "info"}, &buf)
	logger.Info("hello", slog.String("k", "v"))

	var rec map[string]any
	line := strings.TrimSpace(buf.String())
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("production output is not JSON: %q (%v)", line, err)
	}
	if rec["msg"] != "hello" || rec["level"] != "INFO" || rec["k"] != "v" {
		t.Errorf("unexpected JSON record: %v", rec)
	}
}

func TestNewWithWriter_TextInDev(t *testing.T) {
	var buf bytes.Buffer
	logger := NewWithWriter(config.Config{Env: "development", LogLevel: "info"}, &buf)
	logger.Info("hello")

	out := buf.String()
	if json.Valid([]byte(strings.TrimSpace(out))) {
		t.Errorf("development output should be text, not JSON: %q", out)
	}
	if !strings.Contains(out, "msg=hello") || !strings.Contains(out, "level=INFO") {
		t.Errorf("expected text fields, got: %q", out)
	}
}

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug":   slog.LevelDebug,
		"DEBUG":   slog.LevelDebug,
		"info":    slog.LevelInfo,
		"":        slog.LevelInfo,
		"unknown": slog.LevelInfo,
		"warn":    slog.LevelWarn,
		"warning": slog.LevelWarn,
		"error":   slog.LevelError,
	}
	for in, want := range cases {
		if got := parseLevel(in); got != want {
			t.Errorf("parseLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestContextLoggerAndRequestID(t *testing.T) {
	ctx := context.Background()
	if FromContext(ctx) == nil {
		t.Fatal("FromContext should never return nil")
	}
	if got := RequestID(ctx); got != "" {
		t.Errorf("RequestID on empty ctx = %q, want empty", got)
	}

	logger := NewWithWriter(config.Config{Env: "production", LogLevel: "info"}, &bytes.Buffer{})
	ctx = WithContext(ctx, logger)
	if FromContext(ctx) != logger {
		t.Error("FromContext should return the attached logger")
	}
	ctx = withRequestID(ctx, "abc123")
	if got := RequestID(ctx); got != "abc123" {
		t.Errorf("RequestID = %q, want abc123", got)
	}
}
