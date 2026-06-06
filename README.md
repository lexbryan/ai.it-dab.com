# DAB AI — Server

> Stub README. The project overview, architecture diagram, and run instructions
> are written in a later ticket. **This file currently owns only the
> "Repository layout" section below.**

## Repository layout

This is a monorepo with exactly two top-level application folders, plus root
metadata:

| Path | Stack | Purpose |
| --- | --- | --- |
| [`backend/`](backend) | Go 1.22+ | API gateway in front of the self-hosted vLLM model. Entry point: `cmd/server`. Internal packages: `internal/{config,httpserver,db,version}`. |
| [`frontend/`](frontend) | Next.js | Admin UI for managing API keys and personas (placeholder scaffold for now). |

Root metadata — `.gitignore`, this `README.md`, `.github/` (CI), and the
`.env.example` environment contract — lives at the repository root. Docker and
Compose files are introduced in later tickets.

### Backend Go module path

The backend Go module path is:

```
github.com/lexbryan/ai.it-dab.com/backend
```

It mirrors the GitHub remote (`github.com/lexbryan/ai.it-dab.com`) with a
`/backend` suffix for the monorepo. Every backend import uses this prefix, e.g.
`github.com/lexbryan/ai.it-dab.com/backend/internal/version`.

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
