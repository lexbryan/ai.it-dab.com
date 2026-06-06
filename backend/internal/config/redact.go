package config

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

const redactedMask = "[REDACTED]"

var (
	// reUserinfoPw matches a "user:secret@" userinfo segment in any DSN form
	// (URL, opaque, schemeless, or unparseable) and is applied as an
	// unconditional safety net. It is idempotent on already-masked input.
	reUserinfoPw = regexp.MustCompile(`([^/\s:@]+):[^/\s@]+@`)
	// reKeywordPw matches a libpq keyword/value password (bare or single-quoted,
	// possibly containing spaces). Applied only to non-URL DSNs.
	reKeywordPw = regexp.MustCompile(`(?i)(password\s*=\s*)('(?:[^'\\]|\\.)*'|\S+)`)
)

// String returns a human-readable, log-safe representation of the Config. All
// secrets — VLLM_API_KEY, JWT_SECRET, and the DATABASE_URL password — are
// masked so the result is safe to write to logs.
func (c Config) String() string {
	return fmt.Sprintf(
		"Config{Env:%s ListenAddr:%s LogLevel:%s DatabaseURL:%s AutoMigrate:%t "+
			"VLLMURL:%s VLLMAPIKey:%s JWTSecret:%s CORSAllowedOrigins:%v "+
			"ShutdownGrace:%s LoginRateLimit:%+v GatewayRateLimit:%+v}",
		c.Env, c.ListenAddr, c.LogLevel,
		redactDSN(c.DatabaseURL), c.AutoMigrate,
		c.VLLMURL, maskSecret(c.VLLMAPIKey), maskSecret(c.JWTSecret),
		c.CORSAllowedOrigins, c.ShutdownGrace,
		c.LoginRateLimit, c.GatewayRateLimit,
	)
}

// maskSecret hides a secret value entirely, distinguishing set from empty.
func maskSecret(s string) string {
	if s == "" {
		return "[empty]"
	}
	return redactedMask
}

// redactDSN masks the password in a Postgres DSN while keeping the rest (host,
// db, user) useful for debugging. It defends in depth so a password can never
// survive in the output regardless of DSN form:
//   - URL form: mask the userinfo password and any password-bearing query
//     params (password, sslpassword), preserving the rest of the query.
//   - An unconditional userinfo regex catches opaque/schemeless/unparseable URLs
//     that url.Parse does not structure.
//   - For non-URL (keyword/value) DSNs, a quote-aware regex masks password=...
//
// The invariant is that redactDSN never returns a substring matching
// "user:secret@" or "password=<value>".
func redactDSN(dsn string) string {
	if strings.TrimSpace(dsn) == "" {
		return "[empty]"
	}

	out := dsn
	handled := false
	if u, err := url.Parse(dsn); err == nil && u.Scheme != "" {
		if _, hasPw := u.User.Password(); hasPw {
			u.User = url.UserPassword(u.User.Username(), "xxxxx")
		}
		if u.RawQuery != "" {
			q := u.Query()
			changed := false
			for k := range q {
				if lk := strings.ToLower(k); lk == "password" || lk == "sslpassword" {
					q.Set(k, "xxxxx")
					changed = true
				}
			}
			if changed {
				u.RawQuery = q.Encode()
			}
		}
		out = u.String()
		handled = true
	}

	// Safety net: userinfo "user:secret@" in any form (idempotent on URLs already
	// masked above; catches opaque/schemeless/unparseable forms url.Parse missed).
	out = reUserinfoPw.ReplaceAllString(out, "${1}:xxxxx@")
	// Keyword/value form only — avoid clobbering URL query strings handled above.
	if !handled {
		out = reKeywordPw.ReplaceAllString(out, "${1}xxxxx")
	}
	return out
}
