# mailgoose developer tasks. `make help` lists them.

GO          ?= go
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null)
LDFLAGS     := -s -w \
  -X github.com/gremlin-technologies/mailgoose/internal/version.Version=$(VERSION) \
  -X github.com/gremlin-technologies/mailgoose/internal/version.Commit=$(COMMIT)

# Image settings. Fargate runs ARM64 (infra/modules/service, cpu_architecture).
PLATFORM    ?= linux/arm64
IMAGE       ?= mailgoose
IMAGE_TAG   ?= $(COMMIT)

# ECR (only for push-ecr, run where AWS credentials are available).
AWS_REGION  ?= us-east-2
ECR_REPO    ?= mailgoose

TEST_DB_URL ?= postgres://mailgoose:mailgoose@localhost:5432/mailgoose_test?sslmode=disable

.PHONY: help build test test-db vet lint fmt tidy run-api run-worker migrate up down image push-ecr clean

help: ## List targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-12s %s\n", $$1, $$2}'

build: ## Build bin/mailgoose (CLI) and bin/mailgoosed (server)
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/mailgoose ./cmd/mailgoose
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/mailgoosed ./cmd/mailgoosed

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

fmt: ## Format code
	golangci-lint fmt ./...

tidy: ## Tidy go.mod
	$(GO) mod tidy

run-api: ## Run the API against the compose Postgres
	docker compose up -d --wait postgres
	DATABASE_URL="postgres://mailgoose:mailgoose@localhost:5432/mailgoose?sslmode=disable" $(GO) run ./cmd/mailgoosed serve

run-worker: ## Run the worker locally (idles with no queue URLs)
	$(GO) run ./cmd/mailgoosed worker

migrate: ## Apply migrations to the compose Postgres
	docker compose up -d --wait postgres
	DATABASE_URL="postgres://mailgoose:mailgoose@localhost:5432/mailgoose?sslmode=disable" $(GO) run ./cmd/mailgoosed migrate up

up: ## Start Postgres, API and worker in Docker
	docker compose up --build

down: ## Stop the compose stack
	docker compose down

image: ## Build the server image for $(PLATFORM) as $(IMAGE):$(IMAGE_TAG)
	docker buildx build --platform $(PLATFORM) \
	  --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) \
	  -t $(IMAGE):$(IMAGE_TAG) --load .

# Builds and pushes straight to ECR. The repository's tags are immutable, so
# each tag can be pushed once. Needs AWS credentials, so run it locally, never in CI.
push-ecr: ## Build and push $(PLATFORM) image to ECR as $(ECR_REPO):$(IMAGE_TAG)
	@set -e; \
	account=$$(aws sts get-caller-identity --query Account --output text); \
	registry="$$account.dkr.ecr.$(AWS_REGION).amazonaws.com"; \
	aws ecr get-login-password --region $(AWS_REGION) | docker login --username AWS --password-stdin "$$registry"; \
	docker buildx build --platform $(PLATFORM) --provenance=false \
	  --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) \
	  -t "$$registry/$(ECR_REPO):$(IMAGE_TAG)" --push .; \
	echo "Pushed $$registry/$(ECR_REPO):$(IMAGE_TAG)"

clean: ## Remove build output
	rm -rf bin
