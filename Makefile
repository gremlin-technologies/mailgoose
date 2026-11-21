# mailgoose developer tasks. `make help` lists them.

GO          ?= go
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null)
BUILD_DATE  ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS     := -s -w \
  -X github.com/gremlin-technologies/mailgoose/internal/version.Version=$(VERSION) \
  -X github.com/gremlin-technologies/mailgoose/internal/version.Commit=$(COMMIT) \
  -X github.com/gremlin-technologies/mailgoose/internal/version.BuildDate=$(BUILD_DATE)

# Image settings. Fargate runs ARM64 (infra/modules/service, cpu_architecture).
PLATFORM    ?= linux/arm64
IMAGE       ?= mailgoose
IMAGE_TAG   ?= $(COMMIT)

# ECR (only for push-ecr, run where AWS credentials are available).
AWS_REGION  ?= us-east-2
ECR_REPO    ?= mailgoose

TEST_DB_URL ?= postgres://mailgoose:mailgoose@localhost:5432/mailgoose_test?sslmode=disable

.PHONY: help dev e2e-cli e2e-web web web-dev web-check mail build release-snapshot test test-db vet lint lint-api generate docs fmt tidy run-api run-worker migrate up down image push-ecr clean

help: ## List targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-12s %s\n", $$1, $$2}'

dev: ## Run Postgres, migrations, the API and the worker locally; mail goes to .local/mail
	./scripts/dev.sh

e2e-cli: ## End-to-end test of the CLI against the stack from make dev
	./scripts/e2e-cli.sh

e2e-web: web/node_modules ## End-to-end test of the web app (Playwright) against make dev, with the app built (make web)
	cd web && npx playwright test

web: web/node_modules ## Build the web app into web/dist (make dev serves it)
	cd web && npm run -s build

web-dev: web/node_modules ## Run the web app with live reload on :5173, against the API from make dev
	cd web && npm run dev

web-check: web/node_modules ## Lint, type check and test the web app
	cd web && npm run -s lint && npm run -s typecheck && npm run -s test

mail: ## List the newest mail the local fake sender captured
	@if ls .local/mail/*.eml >/dev/null 2>&1; then ls -1t .local/mail/*.eml | head -10; else echo "No mail captured yet."; fi

build: ## Build bin/mailgoose (CLI) and bin/mailgoosed (server)
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/mailgoose ./cmd/mailgoose
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/mailgoosed ./cmd/mailgoosed

release-snapshot: ## Build every CLI release artifact into dist/ without publishing (needs GoReleaser v2)
	goreleaser release --snapshot --clean --skip=publish

test: ## Unit tests (database tests skip without a database)
	$(GO) test -race ./...

test-db: ## All tests, including migrations against the compose Postgres
	docker compose up -d --wait postgres
	docker compose exec -T postgres psql -U mailgoose -d mailgoose -tc \
	  "SELECT 1 FROM pg_database WHERE datname = 'mailgoose_test'" | grep -q 1 || \
	  docker compose exec -T postgres createdb -U mailgoose mailgoose_test
	MAILGOOSE_TEST_DATABASE_URL="$(TEST_DB_URL)" $(GO) test -race ./...

vet: ## go vet
	$(GO) vet ./...

lint: ## golangci-lint (v2)
	golangci-lint run ./...

lint-api: web/node_modules ## Lint api/openapi.yaml with Redocly
	cd web && npm run -s lint:api

generate: web/node_modules ## Regenerate the Go API types and the TypeScript client from api/openapi.yaml, and the web app's policy versions
	$(GO) tool oapi-codegen -config api/oapi-codegen.yaml api/openapi.yaml
	cd web && npm run -s generate
	$(GO) run ./internal/policy/gen

web/node_modules: web/package.json web/package-lock.json
	cd web && npm ci --no-audit --no-fund
	@touch web/node_modules

docs: ## Regenerate docs/api (API reference, llms.txt, agent guide) from api/openapi.yaml and the CLI
	$(GO) run ./internal/apidocs/gen

fmt: ## Format code
	golangci-lint fmt ./...

tidy: ## Tidy go.mod
	$(GO) mod tidy

run-api: ## Run the API against the compose Postgres
	docker compose up -d --wait postgres
	APP_ENV=local MAIL_SENDER=fake MAIL_CAPTURE_DIR=$(CURDIR)/.local/mail SHARED_SENDING_DOMAIN=shared.localhost \
	DATABASE_URL="postgres://mailgoose:mailgoose@localhost:5432/mailgoose?sslmode=disable" $(GO) run ./cmd/mailgoosed serve

run-worker: ## Run the worker locally (idles with no queue URLs)
	APP_ENV=local MAIL_SENDER=fake MAIL_CAPTURE_DIR=$(CURDIR)/.local/mail SHARED_SENDING_DOMAIN=shared.localhost $(GO) run ./cmd/mailgoosed worker

migrate: ## Apply migrations to the compose Postgres
	docker compose up -d --wait postgres
	DATABASE_URL="postgres://mailgoose:mailgoose@localhost:5432/mailgoose?sslmode=disable" $(GO) run ./cmd/mailgoosed migrate up

up: ## Start Postgres, API and worker in Docker
	docker compose up --build

down: ## Stop the compose stack
	docker compose down

image: ## Build the server image for $(PLATFORM) as $(IMAGE):$(IMAGE_TAG)
	docker buildx build --platform $(PLATFORM) \
	  --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg BUILD_DATE=$(BUILD_DATE) \
	  -t $(IMAGE):$(IMAGE_TAG) --load .

# Builds and pushes straight to ECR. The repository's tags are immutable, so
# each tag can be pushed once. Needs AWS credentials, so run it locally, never in CI.
push-ecr: ## Build and push $(PLATFORM) image to ECR as $(ECR_REPO):$(IMAGE_TAG)
	@set -e; \
	account=$$(aws sts get-caller-identity --query Account --output text); \
	registry="$$account.dkr.ecr.$(AWS_REGION).amazonaws.com"; \
	aws ecr get-login-password --region $(AWS_REGION) | docker login --username AWS --password-stdin "$$registry"; \
	docker buildx build --platform $(PLATFORM) --provenance=false \
	  --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg BUILD_DATE=$(BUILD_DATE) \
	  -t "$$registry/$(ECR_REPO):$(IMAGE_TAG)" --push .; \
	echo "Pushed $$registry/$(ECR_REPO):$(IMAGE_TAG)"

clean: ## Remove build output, npm packages and captured local mail
	rm -rf bin web/node_modules web/dist .local
