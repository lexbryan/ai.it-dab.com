package config

import (
	"errors"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"
)

// validEnv returns the minimal set of required variables with valid values.
func validEnv() map[string]string {
	return map[string]string{
		EnvDatabaseURL: "postgres://app:supersecret@postgres:5432/dab?sslmode=disable",
		EnvVLLMURL:     "http://qwen:8000",
		EnvVLLMAPIKey:  "vllm-shared-upstream-key",
		EnvJWTSecret:   "jwt-signing-secret-value",
	}
}

func setEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

// baseEnv makes the environment hermetic: it clears every variable Load reads
// (so ambient/CI env can't bleed into a case) and then sets the required vars to
// valid values. Tests layer their own overrides on top.
func baseEnv(t *testing.T) {
	t.Helper()
	for _, k := range EnvKeys {
		t.Setenv(k, "")
	}
	setEnv(t, validEnv())
}

func TestLoad_AllValid(t *testing.T) {
	baseEnv(t)
	setEnv(t, map[string]string{
		EnvAppEnv:             "production",
		EnvLogLevel:           "debug",
		EnvPort:               "9000",
		EnvAutoMigrate:        "true",
		EnvCORSAllowedOrigins: "http://a.com, http://b.com",
		EnvShutdownGrace:      "15s",
		EnvLoginRateRPM:       "30",
		EnvLoginRateBurst:     "7",
		EnvGatewayRateRPM:     "200",
		EnvGatewayRateBurst:   "50",
	})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Env != "production" || cfg.LogLevel != "debug" {
		t.Errorf("Env/LogLevel = %q/%q", cfg.Env, cfg.LogLevel)
	}
	if cfg.ListenAddr != ":9000" {
		t.Errorf("ListenAddr = %q, want :9000", cfg.ListenAddr)
	}
	if !cfg.AutoMigrate {
		t.Error("AutoMigrate = false, want true")
	}
	if cfg.ShutdownGrace != 15*time.Second {
		t.Errorf("ShutdownGrace = %s, want 15s", cfg.ShutdownGrace)
	}
	if len(cfg.CORSAllowedOrigins) != 2 || cfg.CORSAllowedOrigins[0] != "http://a.com" || cfg.CORSAllowedOrigins[1] != "http://b.com" {
		t.Errorf("CORSAllowedOrigins = %v", cfg.CORSAllowedOrigins)
	}
	if cfg.LoginRateLimit != (RateLimit{RequestsPerMinute: 30, Burst: 7}) {
		t.Errorf("LoginRateLimit = %+v", cfg.LoginRateLimit)
	}
	if cfg.GatewayRateLimit != (RateLimit{RequestsPerMinute: 200, Burst: 50}) {
		t.Errorf("GatewayRateLimit = %+v", cfg.GatewayRateLimit)
	}
	if cfg.VLLMAPIKey != "vllm-shared-upstream-key" || cfg.JWTSecret != "jwt-signing-secret-value" {
		t.Error("secrets not loaded into Config")
	}
}

func TestLoad_DefaultsApplied(t *testing.T) {
	baseEnv(t)
	// Clear every optional so defaults are exercised deterministically.
	for _, k := range []string{
		EnvAppEnv, EnvListenAddr, EnvPort, EnvLogLevel, EnvAutoMigrate,
		EnvCORSAllowedOrigins, EnvShutdownGrace,
		EnvLoginRateRPM, EnvLoginRateBurst, EnvGatewayRateRPM, EnvGatewayRateBurst,
	} {
		t.Setenv(k, "")
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Env != defaultEnv || cfg.LogLevel != defaultLogLevel || cfg.ListenAddr != defaultListenAddr {
		t.Errorf("defaults not applied: Env=%q LogLevel=%q ListenAddr=%q", cfg.Env, cfg.LogLevel, cfg.ListenAddr)
	}
	if cfg.AutoMigrate {
		t.Error("AutoMigrate default should be false")
	}
	if cfg.ShutdownGrace != defaultShutdownGrace {
		t.Errorf("ShutdownGrace = %s, want %s", cfg.ShutdownGrace, defaultShutdownGrace)
	}
	if len(cfg.CORSAllowedOrigins) != 0 {
		t.Errorf("CORSAllowedOrigins = %v, want empty", cfg.CORSAllowedOrigins)
	}
	if cfg.LoginRateLimit != (RateLimit{defaultLoginRPM, defaultLoginBurst}) {
		t.Errorf("LoginRateLimit = %+v", cfg.LoginRateLimit)
	}
	if cfg.GatewayRateLimit != (RateLimit{defaultGatewayRPM, defaultGatewayBurst}) {
		t.Errorf("GatewayRateLimit = %+v", cfg.GatewayRateLimit)
	}
}

func TestLoad_ListenAddrOverridesPort(t *testing.T) {
	baseEnv(t)
	t.Setenv(EnvListenAddr, "127.0.0.1:7000")
	t.Setenv(EnvPort, "9000")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:7000" {
		t.Errorf("ListenAddr = %q, want LISTEN_ADDR to win", cfg.ListenAddr)
	}
}

func TestLoad_MissingRequired(t *testing.T) {
	required := []string{EnvDatabaseURL, EnvVLLMURL, EnvVLLMAPIKey, EnvJWTSecret}
	for _, missing := range required {
		t.Run(missing, func(t *testing.T) {
			baseEnv(t)
			t.Setenv(missing, "") // empty counts as missing

			cfg, err := Load()
			if err == nil {
				t.Fatalf("expected error when %s is empty, got Config %v", missing, cfg)
			}
			if !strings.Contains(err.Error(), missing) {
				t.Errorf("error %q does not name offender %s", err, missing)
			}
		})
	}
}

func TestLoad_InvalidValues(t *testing.T) {
	cases := []struct {
		name   string
		key    string
		value  string
		offend string
	}{
		{"port-not-int", EnvPort, "abc", EnvPort},
		{"port-zero", EnvPort, "0", EnvPort},
		{"port-too-big", EnvPort, "70000", EnvPort},
		{"vllm-url-unparseable", EnvVLLMURL, "http://%zz", EnvVLLMURL},
		{"vllm-url-wrong-scheme", EnvVLLMURL, "ftp://qwen:8000", EnvVLLMURL},
		{"vllm-url-no-host", EnvVLLMURL, "http://", EnvVLLMURL},
		{"auto-migrate-not-bool", EnvAutoMigrate, "maybe", EnvAutoMigrate},
		{"shutdown-grace-bad", EnvShutdownGrace, "soon", EnvShutdownGrace},
		{"login-rpm-negative", EnvLoginRateRPM, "-3", EnvLoginRateRPM},
		{"gateway-burst-not-int", EnvGatewayRateBurst, "lots", EnvGatewayRateBurst},
		{"database-url-unparseable", EnvDatabaseURL, "postgres://%zz", EnvDatabaseURL},
		{"database-url-bad-scheme", EnvDatabaseURL, "mysql://host:3306/db", EnvDatabaseURL},
		{"database-url-no-host", EnvDatabaseURL, "postgres:///db", EnvDatabaseURL},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			baseEnv(t)
			t.Setenv(tc.key, tc.value)

			_, err := Load()
			if err == nil {
				t.Fatalf("expected error for %s=%q", tc.key, tc.value)
			}
			if !strings.Contains(err.Error(), tc.offend) {
				t.Errorf("error %q does not name offender %s", err, tc.offend)
			}
		})
	}
}

func TestLoad_AggregatesAllOffenders(t *testing.T) {
	baseEnv(t)
	t.Setenv(EnvVLLMAPIKey, "")
	t.Setenv(EnvJWTSecret, "")
	t.Setenv(EnvPort, "70000")

	_, err := Load()
	if err == nil {
		t.Fatal("expected aggregated error")
	}
	for _, want := range []string{EnvVLLMAPIKey, EnvJWTSecret, EnvPort} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("aggregated error %q missing offender %s", err, want)
		}
	}
}

func TestLoad_DatabaseURLErrorDoesNotLeakSecret(t *testing.T) {
	baseEnv(t)
	// A malformed DSN that still carries a password (control char forces a parse error).
	t.Setenv(EnvDatabaseURL, "postgres://app:supersecret@host\x7fbad/db")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for unparseable DATABASE_URL")
	}
	if !strings.Contains(err.Error(), EnvDatabaseURL) {
		t.Errorf("error should name DATABASE_URL: %v", err)
	}
	if strings.Contains(err.Error(), "supersecret") {
		t.Errorf("error leaked the DSN password: %v", err)
	}
}

func TestConfig_StringRedactsSecrets(t *testing.T) {
	baseEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	s := cfg.String()
	secrets := []string{"supersecret", "vllm-shared-upstream-key", "jwt-signing-secret-value"}
	for _, secret := range secrets {
		if strings.Contains(s, secret) {
			t.Errorf("String() leaked secret %q: %s", secret, s)
		}
	}
	// Both secret string fields must be masked — not just one.
	if n := strings.Count(s, redactedMask); n != 2 {
		t.Errorf("String() should mask exactly 2 secrets (VLLM_API_KEY, JWT_SECRET), found %d: %s", n, s)
	}
	if !strings.Contains(s, "xxxxx") {
		t.Errorf("String() should mask the DSN password: %s", s)
	}
	// Non-secret context should remain for debuggability.
	if !strings.Contains(s, "postgres:5432") {
		t.Errorf("String() should keep non-secret DSN parts: %s", s)
	}
	if !strings.Contains(s, "http://qwen:8000") {
		t.Errorf("String() over-redacted: VLLM_URL is not a secret and should be visible: %s", s)
	}
}

func TestMaskSecret(t *testing.T) {
	if got := maskSecret(""); got != "[empty]" {
		t.Errorf(`maskSecret("") = %q, want "[empty]"`, got)
	}
	if got := maskSecret("super-secret-value"); got != redactedMask {
		t.Errorf("maskSecret(non-empty) = %q, want %q", got, redactedMask)
	}
	if strings.Contains(maskSecret("super-secret-value"), "secret") {
		t.Error("maskSecret leaked part of the input")
	}
}

// TestEnvExample_PlaceholdersOnly guards against a real secret being committed
// to the tracked .env.example: every secret-bearing line must look like a
// placeholder.
func TestEnvExample_PlaceholdersOnly(t *testing.T) {
	data, err := os.ReadFile("../../../.env.example")
	if err != nil {
		t.Fatalf("read .env.example: %v", err)
	}
	placeholder := regexp.MustCompile(`(?i)change-me|replace|example|localhost|<.*>|placeholder`)
	secretLines := []string{"POSTGRES_PASSWORD=", "VLLM_API_KEY=", "JWT_SECRET=", "DATABASE_URL="}
	for _, line := range strings.Split(string(data), "\n") {
		for _, prefix := range secretLines {
			if strings.HasPrefix(line, prefix) && !placeholder.MatchString(line) {
				t.Errorf("possible real secret committed to .env.example: %q", line)
			}
		}
	}
}

func TestEnvExample_DocumentsEveryLoadKey(t *testing.T) {
	const path = "../../../.env.example"
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	content := string(data)
	for _, key := range EnvKeys {
		re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `=`)
		if !re.MatchString(content) {
			t.Errorf(".env.example is missing a line for %s (drift)", key)
		}
	}
}

func TestDotEnvIsGitIgnored(t *testing.T) {
	cmd := exec.Command("git", "check-ignore", "-q", ".env")
	cmd.Dir = "../../.." // repo root
	err := cmd.Run()
	if err == nil {
		return // exit 0 => .env is ignored
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ee.ExitCode() == 1 {
			t.Fatal(".env is not git-ignored, but it must be")
		}
		t.Skipf("git check-ignore unavailable (exit %d)", ee.ExitCode())
	}
	t.Skipf("could not run git: %v", err)
}
