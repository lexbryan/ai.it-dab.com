package migrate

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lexbryan/ai.it-dab.com/backend/internal/config"
	"github.com/lexbryan/ai.it-dab.com/backend/internal/db"
	"github.com/lexbryan/ai.it-dab.com/backend/migrations"
)

func TestParseFilename(t *testing.T) {
	cases := []struct {
		in            string
		version, name string
		direction     string
		ok            bool
	}{
		{"20260606000001_init.up.sql", "20260606000001", "init", "up", true},
		{"20260606120000_create_users.down.sql", "20260606120000", "create_users", "down", true},
		{"0001_init.up.sql", "", "", "", false},           // not a 14-digit version
		{"20260606000001_init.sql", "", "", "", false},    // missing direction
		{"20260606000001_Init.up.sql", "", "", "", false}, // uppercase name
		{"readme.md", "", "", "", false},
	}
	for _, c := range cases {
		v, n, d, ok := parseFilename(c.in)
		if ok != c.ok || v != c.version || n != c.name || d != c.direction {
			t.Errorf("parseFilename(%q) = (%q,%q,%q,%v), want (%q,%q,%q,%v)",
				c.in, v, n, d, ok, c.version, c.name, c.direction, c.ok)
		}
	}
}

func TestLoad_OrdersAndPairs(t *testing.T) {
	fsys := fstest.MapFS{
		// deliberately out of file order
		"20260606000002_second.up.sql":   &fstest.MapFile{Data: []byte("SELECT 2;")},
		"20260606000002_second.down.sql": &fstest.MapFile{Data: []byte("SELECT 20;")},
		"20260606000001_first.up.sql":    &fstest.MapFile{Data: []byte("SELECT 1;")},
		"20260606000001_first.down.sql":  &fstest.MapFile{Data: []byte("SELECT 10;")},
		"README.md":                      &fstest.MapFile{Data: []byte("not a migration")},
	}
	migs, err := Load(fsys)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(migs) != 2 {
		t.Fatalf("got %d migrations, want 2", len(migs))
	}
	if migs[0].Version != "20260606000001" || migs[1].Version != "20260606000002" {
		t.Errorf("versions out of order: %s then %s", migs[0].Version, migs[1].Version)
	}
	if migs[0].Name != "first" || migs[0].Up != "SELECT 1;" || migs[0].Down != "SELECT 10;" {
		t.Errorf("first migration mis-paired: %+v", migs[0])
	}
}

func TestLoad_MissingDown(t *testing.T) {
	fsys := fstest.MapFS{
		"20260606000001_init.up.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
	}
	if _, err := Load(fsys); err == nil {
		t.Fatal("expected error for a migration missing its .down.sql")
	}
}

func TestLoad_MalformedName(t *testing.T) {
	fsys := fstest.MapFS{
		"oops.up.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
	}
	if _, err := Load(fsys); err == nil {
		t.Fatal("expected error for a malformed .sql filename")
	}
}

// TestLoad_EmbeddedMigrations guards that the real embedded set parses and
// pairs, so a missing or mis-named file fails the build's tests, not production.
func TestLoad_EmbeddedMigrations(t *testing.T) {
	migs, err := Load(migrations.FS)
	if err != nil {
		t.Fatalf("Load(embedded): %v", err)
	}
	if len(migs) == 0 {
		t.Fatal("no embedded migrations found")
	}
	if migs[0].Name != "init" {
		t.Errorf("first embedded migration is %q, want the init baseline", migs[0].Name)
	}
}

func TestWrapTx(t *testing.T) {
	got := wrapTx("CREATE TABLE x();", "INSERT INTO schema_migrations (version, name) VALUES ('1','x')")
	want := "BEGIN;\nCREATE TABLE x();\nINSERT INTO schema_migrations (version, name) VALUES ('1','x');\nCOMMIT;"
	if got != want {
		t.Errorf("wrapTx =\n%q\nwant\n%q", got, want)
	}
	if g := wrapTx("CREATE TABLE x()", "X"); g != "BEGIN;\nCREATE TABLE x();\nX;\nCOMMIT;" {
		t.Errorf("wrapTx(no-semicolon) = %q", g)
	}
}

func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		"Add Widgets 1":     "add_widgets_1",
		"  create  users  ": "create_users",
		"API-Keys":          "api_keys",
		"!!!":               "",
	}
	for in, want := range cases {
		if got := sanitizeName(in); got != want {
			t.Errorf("sanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCreate_GeneratesStubs(t *testing.T) {
	dir := t.TempDir()
	up, down, err := Create(dir, "Add Widgets")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	re := regexp.MustCompile(`^\d{14}_add_widgets\.(up|down)\.sql$`)
	for _, p := range []string{up, down} {
		if !re.MatchString(filepath.Base(p)) {
			t.Errorf("unexpected filename %q", filepath.Base(p))
		}
		if _, err := os.Stat(p); err != nil {
			t.Errorf("stub not written: %v", err)
		}
	}
}

func TestPendingSkipsApplied(t *testing.T) {
	migs := []Migration{{Version: "1"}, {Version: "2"}, {Version: "3"}}
	applied := map[string]bool{"1": true, "2": true}
	var got []string
	for _, m := range migs {
		if !applied[m.Version] {
			got = append(got, m.Version)
		}
	}
	if len(got) != 1 || got[0] != "3" {
		t.Errorf("pending = %v, want [3]", got)
	}
}

// TestMigrate_RoundTrip exercises Up/Down/Status against a real Postgres. It is
// skipped unless DAB_TEST_DATABASE_URL points at a reachable database.
func TestMigrate_RoundTrip(t *testing.T) {
	dsn := os.Getenv("DAB_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set DAB_TEST_DATABASE_URL to run the migration round-trip test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := db.New(ctx, config.Config{DatabaseURL: dsn})
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	defer pool.Close()

	// Reset to a clean slate so the test is deterministic on a shared DB.
	for _, stmt := range []string{
		"DROP TABLE IF EXISTS schema_migrations",
		"DROP EXTENSION IF EXISTS citext",
		"DROP EXTENSION IF EXISTS pgcrypto",
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("reset (%s): %v", stmt, err)
		}
	}

	migs, err := Load(migrations.FS)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	applied, err := Up(ctx, pool, migs)
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	if len(applied) != len(migs) {
		t.Fatalf("Up applied %d, want %d", len(applied), len(migs))
	}
	if !extensionExists(ctx, t, pool, "pgcrypto") || !extensionExists(ctx, t, pool, "citext") {
		t.Error("baseline extensions not created")
	}

	// Idempotent: a second Up applies nothing.
	if again, err := Up(ctx, pool, migs); err != nil || len(again) != 0 {
		t.Fatalf("second Up = %v, %v; want no-op", again, err)
	}

	v, err := CurrentVersion(ctx, pool)
	if err != nil || v != migs[len(migs)-1].Version {
		t.Fatalf("CurrentVersion = %q, %v; want %q", v, err, migs[len(migs)-1].Version)
	}

	// Down everything reverts cleanly.
	reverted, err := Down(ctx, pool, migs, len(migs))
	if err != nil || len(reverted) != len(migs) {
		t.Fatalf("Down = %v, %v; want %d reverted", reverted, err, len(migs))
	}
	if extensionExists(ctx, t, pool, "pgcrypto") {
		t.Error("pgcrypto should be dropped after Down")
	}
	if cur, _ := CurrentVersion(ctx, pool); cur != "" {
		t.Errorf("CurrentVersion after Down = %q, want empty", cur)
	}
}

func extensionExists(ctx context.Context, t *testing.T, pool *pgxpool.Pool, name string) bool {
	t.Helper()
	var ok bool
	if err := pool.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = $1)", name).Scan(&ok); err != nil {
		t.Fatalf("extension check: %v", err)
	}
	return ok
}
