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
