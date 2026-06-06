# DAB AI — Server

The DAB AI gateway is a small API gateway in front of a **self-hosted vLLM**
(OpenAI-compatible) model. Other DAB projects — hub, dab-it-admin, accounting,
and so on — call this gateway instead of the model directly: it authenticates
each project, retains conversation context **server-side**, injects a per-key
persona, and proxies the call to vLLM. A Next.js admin UI manages the API
credentials and their personas.

[![CI](https://github.com/lexbryan/ai.it-dab.com/actions/workflows/ci.yml/badge.svg)](https://github.com/lexbryan/ai.it-dab.com/actions/workflows/ci.yml)
[![coverage gate](https://img.shields.io/badge/coverage-%E2%89%A580%25-brightgreen)](#running-tests--ci)

## Architecture

```mermaid
flowchart LR
    projects["Calling DAB projects<br/>(hub, accounting, dab-it-admin)"]
    browser["Admin browser"]
    frontend["Next.js frontend"]
    gw["Go gateway"]
    pg[("Postgres")]
    vllm[("vLLM — qwen")]

    browser -->|admin JWT cookie| frontend
    frontend -->|admin API, credentialed CORS| gw
    projects -->|"two keys: X-DAB-Key-Id + X-DAB-Secret"| gw
    gw -->|"Authorization: Bearer VLLM_API_KEY"| vllm
    gw <-->|sessions + history| pg
```

There are two credential hops, and **two trust boundaries** that must never be
crossed:

- **Project → Gateway (public hop).** Each calling project presents a **two-key**
  credential — a public key id (`dab_pk_…`) and a secret (`dab_sk_…`) — on every
  request, and the gateway injects that key's **persona** as the leading system
  message. **Project keys never reach vLLM.**
- **Gateway → vLLM (internal hop).** The gateway authenticates upstream with a
  single shared `VLLM_API_KEY`. **The `VLLM_API_KEY` never leaves the gateway** —
  it is never returned downstream, logged, or exposed to a project.

The detailed public contract a caller uses (exact headers, endpoint path, request
body, SSE streaming format, and the gateway-issued session id) lives in the
[connecting guide](docs/CONNECTING.md). The admin UI session is a separate JWT
cookie, distinct from the project two-key credentials.

## Repository layout

This is a monorepo with exactly two top-level application folders, plus root
metadata:

| Path | Stack | Purpose |
| --- | --- | --- |
| [`backend/`](backend) | Go 1.22+ | API gateway in front of the self-hosted vLLM model. Commands: `cmd/{server,migrate,createsuperuser}`. Domain/infra packages under `internal/` (config, httpserver, db, auth, token, user, apikey, conversation, gateway, gatewaycore, vllm, ratelimit, …); SQL migrations under `migrations/`. |
| [`frontend/`](frontend) | Next.js | Admin UI for managing API keys and personas. |

Root metadata lives at the repository root: this `README.md`, `.gitignore`,
`.github/` (CI), the `.env.example` environment contract, the
[`docker-compose.yml`](docker-compose.yml) that wires the full stack, the
`Makefile` of developer/operator targets, and [`docs/`](docs) (the connecting
guide).

### Backend Go module path

The backend Go module path is:

```
github.com/lexbryan/ai.it-dab.com/backend
```

It mirrors the GitHub remote (`github.com/lexbryan/ai.it-dab.com`) with a
`/backend` suffix for the monorepo. Every backend import uses this prefix, e.g.
`github.com/lexbryan/ai.it-dab.com/backend/internal/version`.

## Getting started

**Prerequisites:** Docker and Docker Compose.

```sh
# 1. Create your env file and fill in the secrets (see Configuration below).
make setup        # copies .env.example -> .env
$EDITOR .env      # set POSTGRES_PASSWORD, VLLM_API_KEY, JWT_SECRET (at least)

# 2. Bring up the stack (postgres + backend + frontend) on one network.
make up           # == docker compose up -d --build

# 3. Apply database migrations, then create the first admin.
make migrate
make bootstrap-admin email=admin@example.com   # prompts for the password

# 4. Confirm it's healthy.
make health       # GET gateway /healthz -> 200
```

The frontend is then at <http://localhost:3000> and the gateway at
<http://localhost:8080> (host ports configurable via `GATEWAY_HOST_PORT` /
`FRONTEND_HOST_PORT`). Log in with the superuser you just created. **Postgres and
vLLM are intentionally not reachable from the host** (see the network model
below).

`make migrate` and `make bootstrap-admin` run the backend image's `/migrate` and
`/createsuperuser` binaries **inside the Compose network**. On the **host** (for
local Go development) the same tools run with `cd backend && go run ./cmd/migrate
up` / `go run ./cmd/createsuperuser -email …`, pointing `DATABASE_URL` at a
host-reachable database. The server can also apply migrations itself at startup
when `AUTO_MIGRATE=true` (default off).

**Data persistence.** Postgres data lives in a named volume, so `make down`
followed by `make up` keeps your superuser, keys, and conversations. The
**destructive** `make reset` runs `docker compose down -v`, which **wipes that
volume** and all data, then recreates the stack clean. Use `make logs` to follow
output and `make down` to stop.

## Docker network model

All services join one user-defined bridge network and find each other by service
name — the backend reaches Postgres at `postgres:5432` and vLLM at
`http://qwen:8000`. Port publishing is deliberately minimal:

- **Published to the host:** the gateway (`8080`) and the frontend (`3000`) only.
- **Internal-only (never host-published):** Postgres and vLLM (`qwen`). They are
  reachable only from inside the network.

vLLM (`qwen`) is **external** — this repo does not build a model image; in
production it is the real vLLM server attached to this network (a `vllm` Compose
profile is provided as a placeholder). Postgres uses a named volume at its data
directory, so its data survives container restarts and `down`/`up`.

## Configuration

Copy the committed contract and fill in real values:

```
cp .env.example .env
```

`.env` is git-ignored — only `.env.example` is tracked. `.env.example` lists
every variable the backend reads, and a drift test in `internal/config` fails
the build if a consumed variable is left undocumented. The backend validates
configuration on startup via `config.Load()` and **fails fast**, returning a
single error that names every missing or invalid variable. `internal/config` is
the *only* place environment variables are validated; other packages consume the
already-validated `Config`.

### Two distinct kinds of credentials

These are easy to confuse, so to be explicit:

- **`VLLM_API_KEY`** is the gateway → vLLM upstream secret. It is supplied to the
  gateway through the environment only, is never exposed to projects, and vLLM
  is the only component that ever receives it.
- **Project two-key credentials** (`dab_pk_*` public id + `dab_sk_*` secret) are
  how downstream projects authenticate *to* the gateway. They are **not**
  environment variables — they are minted and stored per the API-key model
  (separate tickets), not configured here.

Secrets (`VLLM_API_KEY`, `JWT_SECRET`, and the `DATABASE_URL` password) are
masked by `Config.String()`, so a stringified config is safe to log.

## Connecting a project to the gateway

How another DAB project calls the gateway — the two-key model, per-key persona,
the gateway-issued session id flow, the `POST /v1/gateway/chat` contract (auth
headers, request body, SSE streaming format, error codes), and copy-paste
`curl`/Node examples — is documented in **[docs/CONNECTING.md](docs/CONNECTING.md)**.
That guide is the single source of truth for the public credential and endpoint
contract.

## HTTP server

`internal/httpserver` builds the gateway's HTTP layer: the router, the base
middleware stack, and a configured `*http.Server`.

**Router — standard-library `net/http.ServeMux`.** Go 1.22 method-based patterns
(`"GET /version"`) cover the small routing surface (admin API + one gateway
endpoint) with zero third-party dependencies. Crucially the stdlib mux does not
buffer responses, so Server-Sent Events from the gateway stream through
immediately and `http.Flusher`/`http.Hijacker` survive the whole middleware
chain. Domains attach handlers through the `Router.Handle` / `Router.HandleFunc`
seam without editing the core; a `/version` route proves the wiring.

**No global write timeout.** The server sets read and idle timeouts
(`ReadHeaderTimeout` for slowloris protection, `ReadTimeout`, `IdleTimeout`) but
**deliberately leaves `http.Server.WriteTimeout` unset**. A global write timeout
caps the time from end-of-request-headers to end-of-response-write, which would
truncate long-lived SSE streams. Streaming is bounded by request context /
per-route deadlines instead.

**Base middleware order** (outermost → innermost): request ID + structured
logging → CORS → panic recovery. Logging is outermost so it records the final
status (including a 500 synthesized by recovery); recovery is innermost so it
wraps only handler execution. No layer re-wraps the `ResponseWriter`, so the
flusher reaches the handler. Panic recovery logs the value and stack, returns a
generic 500 (never leaking internals), and does not append a body to a response
a handler already started streaming.

### CORS

CORS is configured from `CORS_ALLOWED_ORIGINS` (comma-separated). The admin
browser app calls the API cross-origin **with credentials** (the admin JWT,
whether carried as a cookie or an `Authorization` header), so the middleware:

- reflects the **specific** matched origin and sets
  `Access-Control-Allow-Credentials: true`. Per the Fetch spec a credentialed
  response may never use `Access-Control-Allow-Origin: *`, so the wildcard is
  never emitted;
- answers `OPTIONS` preflight directly (it never reaches application handlers),
  advertising the allowed methods/headers and a `Vary: Origin` so caches key on
  the origin;
- grants nothing to origins outside the allowlist (an empty allowlist denies all
  cross-origin browser access).

This is **browser-enforced**: requests without an `Origin` header are unaffected.
The public gateway endpoint is therefore not protected by CORS — its callers are
server-to-server API-key clients that send no `Origin`, and it relies on its
two-key authentication, not CORS, for access control.

## Containers

Both apps have production Dockerfiles (multi-stage, non-root, no secrets baked
into any layer). They build from a clean checkout with **no `.env` present**;
each folder has a `.dockerignore` excluding `node_modules`, build caches, env
files, VCS, and test artifacts.

```bash
# Backend → tiny distroless static image, runs as nonroot (uid 65532).
docker build -t dab-backend ./backend
docker run --rm -p 8080:8080 dab-backend          # GET /healthz → 200

# Frontend → Next.js standalone, runs as the node user (uid 1000).
docker build -t dab-frontend \
  --build-arg NEXT_PUBLIC_API_BASE_URL=http://localhost:8080 ./frontend
docker run --rm -p 3000:3000 dab-frontend          # GET / → 200
```

The **backend** image carries the static Go binaries — the server (default
entrypoint) plus the `migrate` and `createsuperuser` operator CLIs, so the same
image can serve and run migrations / bootstrap the first admin inside the
network. All configuration (`PORT`, `DATABASE_URL`, `VLLM_URL`, `VLLM_API_KEY`,
`JWT_SECRET`, `CORS_ALLOWED_ORIGINS`, …) is read from the environment at runtime,
so no secret is ever baked in.

### `NEXT_PUBLIC_API_BASE_URL`: build-time vs. runtime

`NEXT_PUBLIC_*` values are inlined into the browser bundle at **build** time, not
read at runtime. This project resolves that by passing the gateway URL as a
Docker **build arg** (`ARG NEXT_PUBLIC_API_BASE_URL`); Compose supplies it from
`.env`. The value must be the URL the **user's browser** uses to reach the
gateway — e.g. `http://localhost:8080` (the published host port) in local
Compose — **not** the internal Compose service name (`http://backend:8080`),
which only resolves inside the Compose network and is unreachable from the
browser. The browser calls the gateway cross-origin; the gateway's credentialed
CORS policy (above) permits it.

**Tradeoff:** because the URL is baked at build time, the frontend image is
environment-specific and must be rebuilt per environment (or per release with the
target URL). The alternative — keeping the browser same-origin via a Next.js
route-handler/server-side proxy that forwards to the backend service name at
runtime — trades that rebuild for an extra hop and is deferred; the build-arg
approach is chosen here for simplicity and to keep the gateway the single CORS
surface.

## Development

Common workflows are wrapped in the root `Makefile` — run `make help` to list
them. Every target that touches the stack fails fast with a clear message if
`.env` is missing (run `make setup` first).

| Target | What it does |
| --- | --- |
| `make setup` | Copy `.env.example` → `.env` if missing; set the secrets before `make up`. |
| `make build` | Build all Docker images. |
| `make up` | Start the stack (postgres, backend, frontend) in the background. |
| `make down` | Stop the stack (the Postgres volume is kept). |
| `make logs` | Follow logs for all services. |
| `make ps` | Service status and published ports. |
| `make health` | Check the gateway is healthy (`GET /healthz`). |
| `make migrate` | Apply all pending DB migrations. |
| `make migrate-status` | List every migration and whether it is applied. |
| `make bootstrap-admin email=a@b.com [password=...]` | Create the first superuser. |
| `make reset` | **Destructive:** wipe the Postgres volume, then recreate the stack clean. |

`migrate` and `bootstrap-admin` run the backend image's `/migrate` and
`/createsuperuser` binaries **inside the Compose network**, because Postgres is
internal-only (never host-published). A typical first run:

```sh
make setup     # then edit .env: set POSTGRES_PASSWORD, VLLM_API_KEY, JWT_SECRET
make up
make migrate
make bootstrap-admin email=admin@example.com
make health
```

## Running tests / CI

The CI pipeline (`.github/workflows/ci.yml`) runs on every PR and push to `main`:

| Job | What it checks | Run locally |
| --- | --- | --- |
| `backend-lint` | `gofmt`, `go vet`, `golangci-lint` | `cd backend && make lint` |
| `frontend-lint` | ESLint | `cd frontend && npm run lint` |
| `backend-test` | `go test -race` (excl. e2e) + the coverage gate | `cd backend && make cover` |
| `images` | both production Docker images build | `make build` (root) |

The end-to-end suite is **not** part of CI; run it locally with `make e2e`. The
`backend-test` job and both local DB commands need a throwaway Postgres. CI starts
one as a service; the local commands expect `DAB_TEST_DATABASE_URL` to point at
one (e.g. a disposable container or the Compose `db` service):

```sh
cd backend
export DAB_TEST_DATABASE_URL='postgres://postgres:postgres@localhost:5432/dab_test?sslmode=disable'
make cover   # race tests (excl. e2e) + coverage gate
make e2e     # end-to-end gateway suite against a stub vLLM (local only)
```

**Coverage gate.** `make cover` fails if backend statement coverage drops below
`COVER_THRESHOLD` (currently **80%**; actual is ~88%), excluding the SQL
migrations and the `cmd/*` entrypoint mains from the denominator. Ratchet the
floor up as coverage grows: `make cover COVER_THRESHOLD=85`.

**Merge gates.** `backend-lint` and `frontend-lint` are the established required
checks. To gate merges on the full pipeline, add `backend-test` and `images` to
the branch's required status checks in the repository settings.
