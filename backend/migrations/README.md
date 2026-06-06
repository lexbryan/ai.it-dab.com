# Database migrations

Ordered SQL migrations for the gateway database, applied by the pgx-native
runner in [`internal/db/migrate`](../internal/db/migrate) and the
[`migrate` CLI](../cmd/migrate).

## Naming

Each migration is a pair of files:

```
<version>_<name>.up.sql     -- forward change
<version>_<name>.down.sql    -- rollback
```

- **`<version>`** is a 14-digit UTC timestamp `YYYYMMDDHHMMSS`. Timestamp keys
  are globally unique, so migrations authored in parallel never collide on a
  number, and they still sort into chronological apply order. **Do not** use
  sequential integers (`0001`, `0002`, …).
- **`<name>`** is lowercase `a-z 0-9 _` (e.g. `create_users`).

Generate a correctly-named pair with:

```
make migrate-create name="create users"
# or, from ./backend:
go run ./cmd/migrate create "create users"
```

Every version must have **both** an `.up.sql` and a `.down.sql`; the runner
fails fast otherwise. The files are embedded into the binary (`migrations.go`),
so the runner and CLI work inside the container with no source tree present.

## Writing a migration

- Put one logical change per migration. Use `IF NOT EXISTS` / `IF EXISTS` where
  it makes the migration safely re-runnable.
- A file may contain multiple statements; the whole file runs inside one
  transaction (the runner wraps it in `BEGIN … COMMIT`), so a failure rolls the
  entire file back. **Do not** put your own `BEGIN`/`COMMIT` in a file.
- `down.sql` must reverse `up.sql`. `down` runs newest-first.

## Running

The runner records applied versions in a `schema_migrations` table it manages
itself (not a migration). Commands:

```
make migrate            # apply all pending migrations (alias: migrate up)
make migrate-down       # roll back the most recent migration
make migrate-status     # list every migration and whether it is applied
go run ./cmd/migrate version        # current (highest applied) version
go run ./cmd/migrate down 2         # roll back the last 2
```

`up`/`down`/`status`/`version` load the same configuration the server uses, so
`DATABASE_URL` (and the other required env vars) must be set — e.g. `source`
your `.env`, or run inside the container.

### Host vs container

- **Host:** from `./backend`, with the env set: `make migrate`.
- **Container:** `docker compose run --rm backend migrate up` (the image ships
  the embedded migrations and the `migrate` binary).

### Auto-migrate

The server can apply pending migrations in-process at startup when
`AUTO_MIGRATE=true` (default **false**). It calls `migrate.Migrate(ctx, pool)`,
which loads the embedded set and runs `up`. Leave it off in production and run
migrations as a deliberate step.

## Testing

Unit tests (parsing, ordering, stub generation) need no database. The
round-trip test (`up` → idempotent re-`up` → `down`) runs only when
`DAB_TEST_DATABASE_URL` points at a reachable Postgres:

```
DAB_TEST_DATABASE_URL=postgres://dab:dab@localhost:5432/dab?sslmode=disable \
  go test ./internal/db/...
```
