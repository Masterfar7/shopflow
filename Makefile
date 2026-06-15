# ==============================================================================
# ShopFlow Distributed Order Processing Platform Makefile
# ==============================================================================

SHELL := /usr/bin/env bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

# ------------------------------------------------------------------------------
# Configuration & Variables
# ------------------------------------------------------------------------------
BINARY_NAME       := shopflow
MIGRATE_NAME      := migrate
BUILD_DIR         := bin
CMD_SHOPFLOW      := ./cmd/shopflow
CMD_MIGRATE       := ./cmd/migrate
MIGRATIONS_DIR    := ./migrations

# Database Connection Settings (Default Local Development)
POSTGRES_USER     ?= shopflow
POSTGRES_PASSWORD ?= shopflow_secret
POSTGRES_DB       ?= shopflow
POSTGRES_HOST     ?= localhost
POSTGRES_PORT     ?= 5432
DATABASE_URL      ?= postgres://$(POSTGRES_USER):$(POSTGRES_PASSWORD)@$(POSTGRES_HOST):$(POSTGRES_PORT)/$(POSTGRES_DB)?sslmode=disable

# Go settings
GO                := go
GOFLAGS           ?= -v
TIMEOUT           ?= 60s
RACE_TIMEOUT      ?= 120s

# ANSI Colors
COLOR_RESET   := \033[0m
COLOR_INFO    := \033[36m
COLOR_SUCCESS := \033[32m
COLOR_WARN    := \033[33m
COLOR_ERROR   := \033[31m

# ==============================================================================
# Targets
# ==============================================================================

.PHONY: help
help: ## Show this help message and exit
	@echo "Usage: make [target]"
	@echo ""
	@echo "Available Targets:"
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

.PHONY: all
all: doctor build test ## Run doctor diagnostics, build all binaries, and run standard test suite

.PHONY: build
build: ## Compile shopflow and migrate binaries into bin/
	@echo -e "$(COLOR_INFO)Building ShopFlow binaries...$(COLOR_RESET)"
	@mkdir -p $(BUILD_DIR)
	$(GO) build $(GOFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) $(CMD_SHOPFLOW)
	$(GO) build $(GOFLAGS) -o $(BUILD_DIR)/$(MIGRATE_NAME) $(CMD_MIGRATE)
	@echo -e "$(COLOR_SUCCESS)Build complete: $(BUILD_DIR)/$(BINARY_NAME), $(BUILD_DIR)/$(MIGRATE_NAME)$(COLOR_RESET)"

.PHONY: test
test: ## Run unit and component test suites (in-memory & mock)
	@echo -e "$(COLOR_INFO)Running unit & component test suite...$(COLOR_RESET)"
	$(GO) test $(GOFLAGS) -timeout $(TIMEOUT) ./internal/...

.PHONY: test-race
test-race: ## Run all tests with the race detector enabled (-race)
	@echo -e "$(COLOR_INFO)Checking C compiler for -race support...$(COLOR_RESET)"
	@if ! command -v gcc >/dev/null 2>&1 && ! command -v clang >/dev/null 2>&1; then \
		echo -e "$(COLOR_WARN)Warning: No C compiler (gcc/clang) found in PATH.$(COLOR_RESET)"; \
		echo -e "$(COLOR_WARN)On Windows, install MinGW-w64 (e.g. scoop install mingw-w64) or run via WSL.$(COLOR_RESET)"; \
	fi
	@echo -e "$(COLOR_INFO)Running test suite with race detector (-race)...$(COLOR_RESET)"
	CGO_ENABLED=1 $(GO) test $(GOFLAGS) -race -timeout $(RACE_TIMEOUT) ./...

.PHONY: test-integration
test-integration: ## Run integration test suite using Testcontainers
	@echo -e "$(COLOR_INFO)Running integration test suite with Testcontainers...$(COLOR_RESET)"
	$(GO) test $(GOFLAGS) -tags=integration -timeout 180s ./test/...

.PHONY: docker-up
docker-up: ## Start local infrastructure (PostgreSQL, Kafka KRaft, Redis, Mailpit)
	@echo -e "$(COLOR_INFO)Starting local Docker Compose stack...$(COLOR_RESET)"
	docker compose up -d
	@echo -e "$(COLOR_INFO)Waiting for services to become healthy...$(COLOR_RESET)"
	@docker compose ps

.PHONY: docker-down
docker-down: ## Stop local infrastructure and remove volumes
	@echo -e "$(COLOR_INFO)Stopping Docker Compose stack and removing volumes...$(COLOR_RESET)"
	docker compose down -v --remove-orphans

.PHONY: docker-logs
docker-logs: ## Follow Docker Compose stack logs
	docker compose logs -f

.PHONY: migrate-up
migrate-up: ## Apply all pending database migrations via Goose
	@echo -e "$(COLOR_INFO)Applying database migrations to $(DATABASE_URL)...$(COLOR_RESET)"
	@if [ -f "$(BUILD_DIR)/$(MIGRATE_NAME)" ]; then \
		$(BUILD_DIR)/$(MIGRATE_NAME) -dir $(MIGRATIONS_DIR) -dsn "$(DATABASE_URL)" up; \
	else \
		$(GO) run $(CMD_MIGRATE) -dir $(MIGRATIONS_DIR) -dsn "$(DATABASE_URL)" up; \
	fi
	@echo -e "$(COLOR_SUCCESS)Migrations applied successfully.$(COLOR_RESET)"

.PHONY: migrate-down
migrate-down: ## Rollback the most recent database migration
	@echo -e "$(COLOR_INFO)Rolling back database migration...$(COLOR_RESET)"
	$(GO) run $(CMD_MIGRATE) -dir $(MIGRATIONS_DIR) -dsn "$(DATABASE_URL)" down

.PHONY: migrate-status
migrate-status: ## Check migration status
	@echo -e "$(COLOR_INFO)Checking migration status...$(COLOR_RESET)"
	$(GO) run $(CMD_MIGRATE) -dir $(MIGRATIONS_DIR) -dsn "$(DATABASE_URL)" status

.PHONY: doctor
doctor: ## Check toolchain versions, docker daemon, and dependency readiness
	@echo -e "$(COLOR_INFO)=======================================================$(COLOR_RESET)"
	@echo -e "$(COLOR_INFO)       ShopFlow Development Environment Doctor         $(COLOR_RESET)"
	@echo -e "$(COLOR_INFO)=======================================================$(COLOR_RESET)"
	@echo -n "1. Checking Go toolchain: "
	@if command -v go >/dev/null 2>&1; then \
		GO_VER=$$($(GO) version | awk '{print $$3}'); \
		echo -e "$(COLOR_SUCCESS)OK ($$GO_VER)$(COLOR_RESET)"; \
	else \
		echo -e "$(COLOR_ERROR)FAILED (Go not found on PATH)$(COLOR_RESET)"; \
	fi
	@echo -n "2. Checking Docker CLI: "
	@if command -v docker >/dev/null 2>&1; then \
		DOCKER_VER=$$(docker --version | awk '{print $$3}' | tr -d ','); \
		echo -e "$(COLOR_SUCCESS)OK ($$DOCKER_VER)$(COLOR_RESET)"; \
	else \
		echo -e "$(COLOR_ERROR)FAILED (Docker CLI not found)$(COLOR_RESET)"; \
	fi
	@echo -n "3. Checking Docker Daemon connectivity: "
	@if docker info >/dev/null 2>&1; then \
		echo -e "$(COLOR_SUCCESS)OK (Daemon running)$(COLOR_RESET)"; \
	else \
		echo -e "$(COLOR_WARN)WARNING (Docker daemon not reachable. Start Docker Desktop)$(COLOR_RESET)"; \
	fi
	@echo -n "4. Checking Docker Compose: "
	@if docker compose version >/dev/null 2>&1; then \
		COMPOSE_VER=$$(docker compose version | awk '{print $$4}'); \
		echo -e "$(COLOR_SUCCESS)OK ($$COMPOSE_VER)$(COLOR_RESET)"; \
	else \
		echo -e "$(COLOR_ERROR)FAILED (docker compose plugin missing)$(COLOR_RESET)"; \
	fi
	@echo -n "5. Checking C compiler for -race detector: "
	@if command -v gcc >/dev/null 2>&1; then \
		GCC_VER=$$(gcc --version | head -n1); \
		echo -e "$(COLOR_SUCCESS)OK ($$GCC_VER)$(COLOR_RESET)"; \
	elif command -v clang >/dev/null 2>&1; then \
		CLANG_VER=$$(clang --version | head -n1); \
		echo -e "$(COLOR_SUCCESS)OK ($$CLANG_VER)$(COLOR_RESET)"; \
	else \
		echo -e "$(COLOR_WARN)NOT FOUND (CGO/race detector requires gcc or clang)$(COLOR_RESET)"; \
	fi
	@echo -n "6. Checking local PostgreSQL port 5432: "
	@if nc -z localhost 5432 >/dev/null 2>&1 || (echo > /dev/tcp/localhost/5432) >/dev/null 2>&1; then \
		echo -e "$(COLOR_SUCCESS)OPEN (PostgreSQL reachable)$(COLOR_RESET)"; \
	else \
		echo -e "$(COLOR_INFO)CLOSED (Not running; start with 'make docker-up')$(COLOR_RESET)"; \
	fi
	@echo -n "7. Checking local Kafka port 9092: "
	@if nc -z localhost 9092 >/dev/null 2>&1 || (echo > /dev/tcp/localhost/9092) >/dev/null 2>&1; then \
		echo -e "$(COLOR_SUCCESS)OPEN (Kafka reachable)$(COLOR_RESET)"; \
	else \
		echo -e "$(COLOR_INFO)CLOSED (Not running; start with 'make docker-up')$(COLOR_RESET)"; \
	fi
	@echo -e "$(COLOR_INFO)=======================================================$(COLOR_RESET)"

.PHONY: tidy
tidy: ## Download module dependencies and prune go.mod
	@echo -e "$(COLOR_INFO)Tidying Go modules...$(COLOR_RESET)"
	$(GO) mod tidy

.PHONY: fmt
fmt: ## Format Go source code and organize imports
	@echo -e "$(COLOR_INFO)Formatting source files...$(COLOR_RESET)"
	gofmt -s -w .

.PHONY: lint
lint: ## Run golangci-lint static analysis
	@echo -e "$(COLOR_INFO)Running golangci-lint...$(COLOR_RESET)"
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo -e "$(COLOR_WARN)golangci-lint not installed. Run 'go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest'$(COLOR_RESET)"; \
	fi

.PHONY: generate
generate: ## Run code generation (sqlc, buf, mockgen)
	@echo -e "$(COLOR_INFO)Generating code via sqlc and buf...$(COLOR_RESET)"
	$(GO) tool sqlc generate || echo "sqlc generate skipped (check tool setup)"
	$(GO) tool buf generate || echo "buf generate skipped (check tool setup)"

.PHONY: clean
clean: ## Remove compiled binaries and test coverage output
	@echo -e "$(COLOR_INFO)Cleaning build artifacts...$(COLOR_RESET)"
	@rm -rf $(BUILD_DIR) coverage.out coverage.html
	@echo -e "$(COLOR_SUCCESS)Clean complete.$(COLOR_RESET)"
