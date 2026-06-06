# Backend developer tasks for the DAB AI gateway.
# Module path: github.com/lexbryan/ai.it-dab.com/backend (see root README).

MODULE  := github.com/lexbryan/ai.it-dab.com/backend
BINARY  := bin/server
PKG     := ./...
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "0.0.0-dev")
LDFLAGS := -X $(MODULE)/internal/version.Version=$(VERSION)

# Starting backend coverage gate (statements, excluding migrations + cmd mains).
# Current coverage is ~88%; ratchet this upward as coverage grows. CI fails a PR
# that drops below it.
COVER_THRESHOLD ?= 80

.PHONY: build test cover e2e run lint tidy migrate migrate-down migrate-status migrate-create

build: ## Compile the server binary into bin/server
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/server

test: ## Run the Go test suite
	go test $(PKG)

e2e: ## Run the end-to-end gateway suite against a throwaway Postgres (needs DAB_TEST_DATABASE_URL)
	@test -n "$(DAB_TEST_DATABASE_URL)" || { echo "set DAB_TEST_DATABASE_URL to a throwaway Postgres (e.g. the compose 'db' service)"; exit 1; }
	go test -race -count=1 ./internal/e2e/...

cover: ## Run unit + integration tests with -race + coverage and enforce the gate (needs DAB_TEST_DATABASE_URL; run the e2e suite separately via `make e2e`)
	@test -n "$(DAB_TEST_DATABASE_URL)" || { echo "set DAB_TEST_DATABASE_URL to a throwaway Postgres (e.g. the compose 'db' service)"; exit 1; }
	# -p 1 runs packages serially: the integration tests share one database and
	# reset its schema, so they must not run concurrently (see internal/dbtest).
	# internal/e2e is excluded here and run on its own via `make e2e`.
	go test $$(go list ./... | grep -v '/internal/e2e$$') -race -p 1 -covermode=atomic -coverprofile=coverage.out -coverpkg=./...
	@bash scripts/coverage-gate.sh coverage.out $(COVER_THRESHOLD)

run: ## Run the server from source
	go run -ldflags "$(LDFLAGS)" ./cmd/server

lint: ## gofmt check + go vet + golangci-lint (mirrors the CI backend-lint gate)
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "These files are not gofmt-clean:"; echo "$$unformatted"; exit 1; \
	fi
	go vet $(PKG)
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "golangci-lint not installed; skipping (CI runs it). See https://golangci-lint.run"; \
	fi

tidy: ## Tidy go.mod / go.sum
	go mod tidy

migrate: ## Apply all pending DB migrations (needs DATABASE_URL etc. in env)
	go run ./cmd/migrate up

migrate-down: ## Roll back the most recent migration
	go run ./cmd/migrate down

migrate-status: ## List every migration and whether it is applied
	go run ./cmd/migrate status

migrate-create: ## Scaffold a timestamped migration pair: make migrate-create name="create users"
	go run ./cmd/migrate create "$(name)"
