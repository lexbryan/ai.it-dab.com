# DAB AI gateway — developer / operator entry points.
#
# These targets wrap docker-compose and the backend operator CLIs (server,
# migrate, createsuperuser) behind ergonomic names. They delegate; they do not
# reimplement. See the README "Development" section for usage.

COMPOSE := docker compose

.DEFAULT_GOAL := help
.PHONY: help setup build up down logs ps health reset migrate migrate-status bootstrap-admin

help: ## Show this help
	@grep -E '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

setup: ## Create .env from .env.example if missing, then print next steps
	@if [ -f .env ]; then \
		echo ".env already exists — leaving it untouched."; \
	else \
		cp .env.example .env; \
		echo "Created .env from .env.example."; \
		echo "Set the secrets (POSTGRES_PASSWORD, VLLM_API_KEY, JWT_SECRET) before 'make up'."; \
	fi

# require-env fails fast with an actionable message when .env is absent.
define require-env
	@test -f .env || { echo "No .env found. Run 'make setup' first."; exit 1; }
endef

build: ## Build all images
	$(require-env)
	$(COMPOSE) build

up: ## Start the stack in the background (building images as needed)
	$(require-env)
	$(COMPOSE) up -d --build

down: ## Stop the stack (the Postgres volume is kept)
	$(COMPOSE) down

logs: ## Follow logs for all services
	$(COMPOSE) logs -f

ps: ## Show service status and published ports
	$(COMPOSE) ps

health: ## Check the gateway is healthy (GET /healthz)
	$(require-env)
	@port=$$(grep -E '^GATEWAY_HOST_PORT=' .env | cut -d= -f2); \
	curl -fsS "http://localhost:$${port:-8080}/healthz" && echo " <- gateway healthy" \
		|| { echo "gateway not healthy (is the stack up?)"; exit 1; }

reset: ## DESTRUCTIVE: wipe the Postgres volume, then recreate the stack clean
	@echo "This deletes the Postgres named volume (all data). Recreating clean..."
	$(COMPOSE) down -v
	$(COMPOSE) up -d --build

migrate: ## Apply all pending DB migrations (runs inside the compose network)
	$(require-env)
	$(COMPOSE) run --rm --entrypoint /migrate backend up

migrate-status: ## List every migration and whether it is applied
	$(require-env)
	$(COMPOSE) run --rm --entrypoint /migrate backend status

bootstrap-admin: ## Create the first superuser: make bootstrap-admin email=a@b.com [password=...]
	$(require-env)
	@test -n "$(email)" || { echo "usage: make bootstrap-admin email=admin@example.com [password=...]"; exit 1; }
	$(COMPOSE) run --rm \
		-e SUPERUSER_EMAIL="$(email)" -e SUPERUSER_PASSWORD="$(password)" \
		--entrypoint /createsuperuser backend
