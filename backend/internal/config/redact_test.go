package config

import (
	"strings"
	"testing"
)

func TestRedactDSN(t *testing.T) {
	cases := []struct {
		name     string
		dsn      string
		leaked   string // must NOT appear (empty = no secret to check)
		mustKeep string // must still appear (empty = skip)
	}{
		{"userinfo", "postgres://app:supersecret@postgres:5432/dab?sslmode=disable", "supersecret", "postgres:5432"},
		{"query-param", "postgres://app@postgres:5432/dab?password=supersecret&sslmode=disable", "supersecret", "postgres:5432"},
		{"query-param-uppercase", "postgres://app@postgres:5432/dab?Password=supersecret", "supersecret", "postgres:5432"},
		{"query-sslpassword", "postgres://app@postgres:5432/dab?sslpassword=supersecret&sslmode=require", "supersecret", "sslmode"},
		{"keyword-form", "host=postgres user=app password=supersecret dbname=dab", "supersecret", "host=postgres"},
		{"keyword-quoted-spaced", "host=postgres password='hunter2 with spaces' dbname=dab", "hunter2", "host=postgres"},
		{"unparseable-userinfo", "postgres://app:supersecret@host\x7fbad/db", "supersecret", ""},
		{"schemeless-userinfo", "//app:supersecret@postgres:5432/db", "supersecret", "postgres:5432"},
		{"opaque-mispaste", "app:supersecret@postgres:5432/db", "supersecret", "postgres:5432"},
		{"no-password", "postgres://app@postgres:5432/dab?sslmode=disable", "", "postgres:5432"},
		{"empty", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactDSN(tc.dsn)
			if tc.leaked != "" && strings.Contains(got, tc.leaked) {
				t.Errorf("redactDSN(%q) leaked %q: %s", tc.dsn, tc.leaked, got)
			}
			if tc.mustKeep != "" && !strings.Contains(got, tc.mustKeep) {
				t.Errorf("redactDSN(%q) dropped %q: %s", tc.dsn, tc.mustKeep, got)
			}
		})
	}
}
