# Backend developer tasks for the DAB AI gateway.
# Module path: github.com/lexbryan/ai.it-dab.com/backend (see root README).

MODULE  := github.com/lexbryan/ai.it-dab.com/backend
BINARY  := bin/server
PKG     := ./...
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "0.0.0-dev")
LDFLAGS := -X $(MODULE)/internal/version.Version=$(VERSION)

.PHONY: build test run lint tidy

build: ## Compile the server binary into bin/server
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/server

test: ## Run the Go test suite
	go test $(PKG)

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
