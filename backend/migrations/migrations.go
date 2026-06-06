// Package migrations holds the ordered SQL migration files for the gateway
// database and embeds them so the runner and the migrate CLI work from a
// compiled binary (e.g. inside the container) with no source tree present.
//
// Files are named "<version>_<name>.up.sql" and "<version>_<name>.down.sql",
// where <version> is a 14-digit UTC timestamp (YYYYMMDDHHMMSS). Timestamp keys
// (rather than sequential integers) are globally unique, so migrations authored
// in parallel never collide while still applying in chronological order.
package migrations

import "embed"

// FS holds every .sql migration file, embedded at build time.
//
//go:embed *.sql
var FS embed.FS
