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

Root metadata — `.gitignore`, this `README.md`, and `.github/` (CI) — lives at
the repository root. Docker, Compose, and environment files are introduced in
later tickets, not here.

### Backend Go module path

The backend Go module path is:

```
github.com/lexbryan/ai.it-dab.com/backend
```

It mirrors the GitHub remote (`github.com/lexbryan/ai.it-dab.com`) with a
`/backend` suffix for the monorepo. Every backend import uses this prefix, e.g.
`github.com/lexbryan/ai.it-dab.com/backend/internal/version`.
