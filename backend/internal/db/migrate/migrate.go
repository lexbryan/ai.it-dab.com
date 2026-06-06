// Package migrate is a small, pgx-native migration runner for the gateway
// database.
//
// # Why an in-house runner
//
// The migration surface is small (ordered SQL files, an up/down/status/create
// workflow, a bookkeeping table) and the rest of the backend already speaks pgx
// via pgxpool. A purpose-built runner keeps everything on that one driver — no
// database/sql adapter, no second connection library — and pins nothing extra
// to the module, so the backend stays on its target Go toolchain. It applies
// each migration file as a single simple-protocol script wrapped in an explicit
// transaction, so a file may contain multiple statements and either fully
// applies or fully rolls back.
//
// # Versioning
//
// Versions are 14-digit UTC timestamps (YYYYMMDDHHMMSS) taken from the file
// name "<version>_<name>.up.sql" / ".down.sql". Timestamp keys are globally
// unique, so migrations authored in parallel never collide, and they still sort
// into chronological apply order.
package migrate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lexbryan/ai.it-dab.com/backend/migrations"
)

// Migration is one versioned schema change with its forward and rollback SQL.
type Migration struct {
	Version string // 14-digit timestamp key
	Name    string // human label, e.g. "create_users"
	Up      string // forward SQL
	Down    string // rollback SQL
}

// Status is a migration paired with whether it has been applied.
type Status struct {
	Version string
	Name    string
	Applied bool
}

// bookkeepingDDL creates the table that records which versions are applied. The
// runner owns this table; it is not itself a migration.
const bookkeepingDDL = `CREATE TABLE IF NOT EXISTS schema_migrations (
	version    text PRIMARY KEY,
	name       text NOT NULL,
	applied_at timestamptz NOT NULL DEFAULT now()
)`

var (
	filenameRE = regexp.MustCompile(`^(\d{14})_([a-z0-9]+(?:_[a-z0-9]+)*)\.(up|down)\.sql$`)
	nameRE     = regexp.MustCompile(`[^a-z0-9]+`)
)

// Migrate loads the embedded migrations and applies all pending ones. It is the
// in-process entry point; callers gate it behind cfg.AutoMigrate (off by
// default). It returns the versions applied this call (empty when up to date).
func Migrate(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	migs, err := Load(migrations.FS)
	if err != nil {
		return nil, err
	}
	return Up(ctx, pool, migs)
}

// Load reads, validates, and orders the migrations in fsys. Every version must
// have both an .up.sql and a .down.sql; any .sql file that does not match the
// naming scheme is an error rather than silently ignored.
func Load(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("migrate: reading migrations: %w", err)
	}

	type pair struct {
		name           string
		up, down       string
		hasUp, hasDown bool
	}
	byVer := map[string]*pair{}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		version, name, dir, ok := parseFilename(e.Name())
		if !ok {
			return nil, fmt.Errorf("migrate: malformed migration filename %q", e.Name())
		}
		body, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, fmt.Errorf("migrate: reading %s: %w", e.Name(), err)
		}
		p := byVer[version]
		if p == nil {
			p = &pair{name: name}
			byVer[version] = p
		}
		if p.name != name {
			return nil, fmt.Errorf("migrate: version %s has conflicting names %q and %q", version, p.name, name)
		}
		if dir == "up" {
			p.up, p.hasUp = string(body), true
		} else {
			p.down, p.hasDown = string(body), true
		}
	}

	versions := make([]string, 0, len(byVer))
	for v := range byVer {
		versions = append(versions, v)
	}
	sort.Strings(versions)

	migs := make([]Migration, 0, len(versions))
	for _, v := range versions {
		p := byVer[v]
		switch {
		case !p.hasUp:
			return nil, fmt.Errorf("migrate: version %s is missing its .up.sql file", v)
		case !p.hasDown:
			return nil, fmt.Errorf("migrate: version %s is missing its .down.sql file", v)
		}
		migs = append(migs, Migration{Version: v, Name: p.name, Up: p.up, Down: p.down})
	}
	return migs, nil
}

// Up applies every pending migration in order, each in its own transaction, and
// records it in schema_migrations. It is idempotent: already-applied versions
// are skipped. Returns the versions applied this call.
func Up(ctx context.Context, pool *pgxpool.Pool, migs []Migration) ([]string, error) {
	if err := ensureBookkeeping(ctx, pool); err != nil {
		return nil, err
	}
	applied, err := appliedSet(ctx, pool)
	if err != nil {
		return nil, err
	}

	var done []string
	for _, m := range migs {
		if applied[m.Version] {
			continue
		}
		script := wrapTx(m.Up, fmt.Sprintf(
			"INSERT INTO schema_migrations (version, name) VALUES (%s, %s)",
			sqlString(m.Version), sqlString(m.Name)))
		if err := runScript(ctx, pool, script); err != nil {
			return done, fmt.Errorf("migrate: applying %s_%s: %w", m.Version, m.Name, err)
		}
		done = append(done, m.Version)
	}
	return done, nil
}

// Down rolls back the most recently applied migrations, newest first, up to
// steps of them (steps <= 0 means one). Each rollback runs in its own
// transaction and removes the bookkeeping row. Returns the versions reverted.
func Down(ctx context.Context, pool *pgxpool.Pool, migs []Migration, steps int) ([]string, error) {
	if steps <= 0 {
		steps = 1
	}
	if err := ensureBookkeeping(ctx, pool); err != nil {
		return nil, err
	}
	appliedOrder, err := appliedVersions(ctx, pool)
	if err != nil {
		return nil, err
	}

	byVer := make(map[string]Migration, len(migs))
	for _, m := range migs {
		byVer[m.Version] = m
	}

	var reverted []string
	for i := len(appliedOrder) - 1; i >= 0 && steps > 0; i-- {
		v := appliedOrder[i]
		m, ok := byVer[v]
		if !ok {
			return reverted, fmt.Errorf("migrate: applied version %s has no migration file to roll back", v)
		}
		script := wrapTx(m.Down, fmt.Sprintf(
			"DELETE FROM schema_migrations WHERE version = %s", sqlString(m.Version)))
		if err := runScript(ctx, pool, script); err != nil {
			return reverted, fmt.Errorf("migrate: rolling back %s_%s: %w", m.Version, m.Name, err)
		}
		reverted = append(reverted, v)
		steps--
	}
	return reverted, nil
}

// GetStatus reports every known migration and whether it is applied.
func GetStatus(ctx context.Context, pool *pgxpool.Pool, migs []Migration) ([]Status, error) {
	if err := ensureBookkeeping(ctx, pool); err != nil {
		return nil, err
	}
	applied, err := appliedSet(ctx, pool)
	if err != nil {
		return nil, err
	}
	out := make([]Status, 0, len(migs))
	for _, m := range migs {
		out = append(out, Status{Version: m.Version, Name: m.Name, Applied: applied[m.Version]})
	}
	return out, nil
}

// CurrentVersion returns the highest applied version, or "" if none.
func CurrentVersion(ctx context.Context, pool *pgxpool.Pool) (string, error) {
	if err := ensureBookkeeping(ctx, pool); err != nil {
		return "", err
	}
	order, err := appliedVersions(ctx, pool)
	if err != nil {
		return "", err
	}
	if len(order) == 0 {
		return "", nil
	}
	return order[len(order)-1], nil
}

// Create writes an empty up/down migration pair into dir with a fresh timestamp
// version and a sanitized name, returning the two file paths.
func Create(dir, name string) (upPath, downPath string, err error) {
	slug := sanitizeName(name)
	if slug == "" {
		return "", "", errors.New("migrate: migration name must contain a letter or digit")
	}
	version := time.Now().UTC().Format("20060102150405")
	base := version + "_" + slug
	upPath = filepath.Join(dir, base+".up.sql")
	downPath = filepath.Join(dir, base+".down.sql")

	if err := os.WriteFile(upPath, []byte(stub(base, "up")), 0o644); err != nil {
		return "", "", fmt.Errorf("migrate: writing %s: %w", upPath, err)
	}
	if err := os.WriteFile(downPath, []byte(stub(base, "down")), 0o644); err != nil {
		return "", "", fmt.Errorf("migrate: writing %s: %w", downPath, err)
	}
	return upPath, downPath, nil
}

// --- helpers ---

func parseFilename(name string) (version, label, direction string, ok bool) {
	m := filenameRE.FindStringSubmatch(name)
	if m == nil {
		return "", "", "", false
	}
	return m[1], m[2], m[3], true
}

func sanitizeName(name string) string {
	s := nameRE.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "_")
	return strings.Trim(s, "_")
}

func stub(base, dir string) string {
	return fmt.Sprintf("-- %s (%s migration)\n-- Write the %s SQL here.\n", base, dir, dir)
}

// wrapTx builds a single simple-protocol script that runs body and the
// bookkeeping statement inside one explicit transaction. A failure in body
// aborts the transaction, so nothing (including the bookkeeping row) is
// committed.
func wrapTx(body, bookkeeping string) string {
	body = strings.TrimRight(strings.TrimSpace(body), ";")
	return "BEGIN;\n" + body + ";\n" + bookkeeping + ";\nCOMMIT;"
}

func ensureBookkeeping(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, bookkeepingDDL); err != nil {
		return fmt.Errorf("migrate: creating schema_migrations: %w", err)
	}
	return nil
}

func appliedSet(ctx context.Context, pool *pgxpool.Pool) (map[string]bool, error) {
	order, err := appliedVersions(ctx, pool)
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(order))
	for _, v := range order {
		set[v] = true
	}
	return set, nil
}

func appliedVersions(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	rows, err := pool.Query(ctx, `SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, fmt.Errorf("migrate: reading applied versions: %w", err)
	}
	defer rows.Close()

	var order []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("migrate: scanning version: %w", err)
		}
		order = append(order, v)
	}
	return order, rows.Err()
}

// runScript executes a multi-statement script over the simple query protocol so
// a migration file can hold several statements. It acquires one pooled
// connection for the whole script.
func runScript(ctx context.Context, pool *pgxpool.Pool, script string) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("migrate: acquiring connection: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Conn().PgConn().Exec(ctx, script).ReadAll(); err != nil {
		return err
	}
	return nil
}

// sqlString single-quotes and escapes s for inline use. Versions and names are
// already constrained to [0-9a-z_], so this is defense in depth.
func sqlString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
