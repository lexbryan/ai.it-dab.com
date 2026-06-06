# syntax=docker/dockerfile:1

# ---- build stage ----
FROM golang:1.22-alpine AS build
WORKDIR /src

# Download modules first so this layer caches across source-only changes.
COPY go.mod go.sum ./
RUN go mod download

# Build a fully static binary (CGO disabled) so it runs on distroless/static.
COPY . .
ARG VERSION=docker
# Build all three commands: the server plus the migrate and createsuperuser
# operator CLIs, so a single image can serve AND run migrations / bootstrap the
# first admin inside the compose network (where Postgres is reachable only by
# service name). With -o pointing at a directory, each is emitted as /out/<cmd>.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w -X github.com/lexbryan/ai.it-dab.com/backend/internal/version.Version=${VERSION}" \
    -o /out/ ./cmd/server ./cmd/migrate ./cmd/createsuperuser

# ---- runtime stage ----
# distroless/static-debian12:nonroot has no shell, package manager, or secrets —
# only the static binary is added, and it runs as the nonroot user (uid 65532).
FROM gcr.io/distroless/static-debian12:nonroot AS runtime

# All configuration (PORT, DATABASE_URL, VLLM_URL, VLLM_API_KEY, JWT_SECRET,
# CORS_ALLOWED_ORIGINS, ...) is supplied via the environment at runtime. No
# secrets are baked into any image layer.
COPY --from=build /out/server /server
COPY --from=build /out/migrate /migrate
COPY --from=build /out/createsuperuser /createsuperuser

EXPOSE 8080
USER nonroot:nonroot
# The server is the default; migrate/createsuperuser are run via
# `docker compose run --rm --entrypoint /migrate backend ...` (see the Makefile).
ENTRYPOINT ["/server"]
