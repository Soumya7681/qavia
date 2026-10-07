# Qavia
#
# Three languages, three native toolchains, one entry point. There is no
# monorepo orchestrator: go build, pnpm, and uv each keep their own tooling
# (tech-stack.md 11).

SHELL := /bin/bash
.DEFAULT_GOAL := help

API_DIR     := api
WEB_DIR     := web
AI_DIR      := services/ai
COMPOSE     := docker compose -f infra/docker/compose.yaml

# Loaded so migrate/test targets see DATABASE_URL without exporting by hand.
ifneq (,$(wildcard .env))
include .env
export
endif

GOOSE_DRIVER := postgres

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

# ---------------------------------------------------------------- environment

.PHONY: up
up: ## Start Postgres, Redis, and MinIO
	$(COMPOSE) up -d --wait

.PHONY: down
down: ## Stop the local stack
	$(COMPOSE) down

.PHONY: reset
reset: ## Stop the local stack and delete its data
	$(COMPOSE) down -v

.PHONY: dev
dev: up migrate-up ## Start the stack, migrate, and run the API, worker, and AI service
	@echo "starting api, worker, and ai service; ctrl-c to stop"
	@trap 'kill 0' EXIT; \
	 ( cd $(API_DIR) && go run ./cmd/api ) & \
	 ( cd $(API_DIR) && go run ./cmd/worker ) & \
	 ( cd $(AI_DIR) && uv run uvicorn src.main:app --port 8000 ) & \
	 wait

# ---------------------------------------------------------------- generation

# oapi-codegen is a tool dependency in api/go.mod, so `go tool` runs the pinned
# version and it is deliberately absent here.
.PHONY: tools
tools: ## Install the pinned code generators
	cd $(API_DIR) && go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0
	cd $(API_DIR) && go install github.com/pressly/goose/v3/cmd/goose@v3.26.0
	cd $(API_DIR) && go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.7.1

.PHONY: gen
gen: gen-sql gen-ai-schema gen-api ## Regenerate everything. CI fails on a diff

.PHONY: gen-sql
gen-sql: ## Regenerate sqlc query code
	cd $(API_DIR) && sqlc generate

.PHONY: gen-ai-schema
gen-ai-schema: ## Dump the FastAPI schema the Go AI client is generated from
	./scripts/dump-ai-schema.sh

.PHONY: gen-api
gen-api: ## Regenerate the Go server and the AI service client
	cd $(API_DIR) && go generate ./openapi/...

# Joins `gen` at FE-S.3, when CI gains a Node job that can enforce a clean diff on
# the generated client. Until then it is run on demand to prove the spec generates.
.PHONY: gen-web
gen-web: ## Regenerate the TypeScript client from the same spec
	cd $(WEB_DIR) && pnpm run gen

# ---------------------------------------------------------------- migrations

.PHONY: migrate-up
migrate-up: ## Apply all migrations
	cd $(API_DIR) && goose -dir migrations $(GOOSE_DRIVER) "$(DATABASE_URL)" up

.PHONY: migrate-down
migrate-down: ## Roll back one migration
	cd $(API_DIR) && goose -dir migrations $(GOOSE_DRIVER) "$(DATABASE_URL)" down

.PHONY: migrate-status
migrate-status: ## Show migration status
	cd $(API_DIR) && goose -dir migrations $(GOOSE_DRIVER) "$(DATABASE_URL)" status

.PHONY: migrate-new
migrate-new: ## Create a migration: make migrate-new name=add_widgets
	cd $(API_DIR) && goose -dir migrations create $(name) sql

# Local Postgres runs in compose, so psql and pg_dump come from the container by
# default; a host client older than the server refuses to dump. CI installs a
# matching client and sets this to 0.
QAVIA_PG_IN_DOCKER ?= 1

.PHONY: schema
schema: ## Rebuild api/schema.sql from the migrations. Reviewed in every migration PR
	QAVIA_PG_IN_DOCKER=$(QAVIA_PG_IN_DOCKER) ./scripts/dump-schema.sh

# ---------------------------------------------------------------------- build

.PHONY: build
build: ## Build both binaries
	cd $(API_DIR) && go build -o bin/api ./cmd/api
	cd $(API_DIR) && go build -o bin/worker ./cmd/worker

# ----------------------------------------------------------------------- test

.PHONY: test
test: test-go test-python test-web ## Run every test suite

.PHONY: test-go
test-go: ## Go tests with the race detector. Not optional (tech-stack.md 13)
	cd $(API_DIR) && go test -race ./...

.PHONY: test-short
test-short: ## Go tests, skipping the container-backed integration suites
	cd $(API_DIR) && go test -race -short ./...

.PHONY: test-python
test-python: ## Python agent tests against recorded fixtures
	cd $(AI_DIR) && uv run pytest

.PHONY: test-web
test-web: ## Frontend component tests
	@[ -f $(WEB_DIR)/package.json ] && cd $(WEB_DIR) && pnpm test --run || echo "web: not scaffolded yet (FE-S.1)"

# ----------------------------------------------------------------------- lint

.PHONY: lint
lint: lint-go lint-python lint-web ## Lint everything

.PHONY: lint-go
lint-go:
	cd $(API_DIR) && golangci-lint run ./...
	cd $(API_DIR) && go vet ./...

.PHONY: lint-python
lint-python:
	cd $(AI_DIR) && uv run ruff check .

.PHONY: lint-web
lint-web:
	@[ -f $(WEB_DIR)/package.json ] && cd $(WEB_DIR) && pnpm lint || echo "web: not scaffolded yet (FE-S.1)"

.PHONY: tidy
tidy: ## go mod tidy. CI fails on a diff
	cd $(API_DIR) && go mod tidy

# ------------------------------------------------------------------ frontend support

.PHONY: seed
seed: ## Seed the demo project (BE-X.2)
	cd $(API_DIR) && go run ./cmd/seed

.PHONY: mock
mock: ## Serve the OpenAPI contract as a mock API (BE-X.3)
	npx --yes @stoplight/prism-cli mock $(API_DIR)/openapi/qavia.yaml --port 4010

.PHONY: rotate-key
rotate-key: ## Re-encrypt every stored secret under a new key. Run with both processes stopped
	cd $(API_DIR) && go run ./cmd/rotatekey -new-key "$(new_key)"

.PHONY: rotate-key-dry-run
rotate-key-dry-run: ## Report which secrets would be rotated, writing nothing
	cd $(API_DIR) && go run ./cmd/rotatekey -dry-run
