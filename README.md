# mailgoose

A deliberately small email marketing app: design and send emails, build signup
forms, and drive it all from a CLI that works with any AI agent.

## Layout

```
cmd/mailgoose/     Customer CLI (single static binary).
cmd/mailgoosed/    Server: `serve` (HTTP API), `worker` (SQS), `migrate`.
internal/api/      HTTP handlers. /healthz is the load balancer's check.
internal/api/apigen/  Go types and server interface generated from the spec.
internal/worker/   SQS consumer for the send and SES events queues (stub handlers).
internal/db/       Postgres pool, Secrets Manager credentials, migrations.
internal/config/   Server settings from environment variables.
internal/mail/     Sends built messages: SES v2, or a fake for tests and local runs.
internal/cli/      CLI commands and its config file.
api/openapi.yaml   The API contract. Every endpoint gets a CLI command.
web/               Web app package; for now only the generated TypeScript API client.
infra/             Terraform for AWS (see infra/README.md).
docs/              PRD, pricing, decisions.
```

One container image runs both ECS services. Its entrypoint is `mailgoosed`, and
ECS passes `serve` or `worker` (see `infra/modules/stack`). The customer CLI is
a separate binary, so no server code ships to customers.

## Run locally

From a fresh clone to a running API, on macOS or Linux:

1. **Install** Go 1.27 or later and Docker (Docker Desktop on macOS), and start
   Docker.
2. **Clone and start everything:**
   ```sh
   git clone https://github.com/gremlin-technologies/mailgoose
   cd mailgoose
   make dev
   ```
   `make dev` starts Postgres in Docker, applies migrations, then runs the API
   on :8080 and the worker as local processes. Ctrl-C stops the API and worker;
   `make down` stops Postgres.
3. **Check it** from another terminal:
   ```sh
   curl localhost:8080/readyz       # {"status":"ok",...} once Postgres answers
   make build && ./bin/mailgoose status --api-url http://localhost:8080
   ```

Mail never leaves your machine: locally the server uses the fake sender, which
writes every message to `.local/mail/` as an `.eml` file. `make mail` lists the
newest; open one in any mail client. Nothing locally has AWS settings, and the
worker idles because it has no queue URLs. To use a Postgres you already run,
set `DATABASE_URL` before `make dev`.

Other ways to run it:

```sh
docker compose up --build        # everything in containers; mail is kept in memory
make run-api                     # just the API, against the compose Postgres
make run-worker                  # just the worker (idles: no queue URLs locally)
```

The API applies pending migrations when it starts (`MIGRATE_ON_START`, default
`true`). A Postgres advisory lock stops two tasks from migrating at once. To
run them by hand: `mailgoosed migrate up|down|status`.

### Tests and checks

```sh
make test       # unit tests; database tests skip
make test-db    # also runs the database tests against Postgres
make vet lint   # go vet, golangci-lint v2
make build      # bin/mailgoose and bin/mailgoosed
make image      # server image for linux/arm64
```

### Changing the API

`api/openapi.yaml` is the source of truth. After editing it:

```sh
make generate   # Go types (internal/api/apigen) and the TS client (web/src/api)
make lint-api   # Redocly lint (recommended-strict, settings in api/redocly.yaml)
```

`make generate` and `make lint-api` need Node 22+. A new operation doesn't
compile until `api.Server` has its handler, and `TestContract` in
`internal/api/contract_test.go` needs a case for each response the operation
documents. Every operation also needs a `mailgoose` command that declares it
with `calls()` (`internal/cli/registry.go`), unless
`api/cli-parity-allowlist.yaml` lists it with a reason; `TestCLIParity` checks
that. CI's `go test` runs the lint, checks that both generated files match
the spec, and validates every contract case's response against the spec.

### CLI

```sh
./bin/mailgoose status --api-url http://localhost:8080
./bin/mailgoose config set api-url https://api.staging.mailgoose.app
printf '%s' "$KEY" | ./bin/mailgoose config set api-key -
./bin/mailgoose config show --json
```

Every command takes `--json`: output goes to stdout, and errors go to stderr
in the API's error shape. Nothing prompts; with no value for something, a
command exits 2 and names the flag to pass. `MAILGOOSE_API_KEY` and
`MAILGOOSE_API_URL` override the config file, so agents can run headless. Exit
codes (`mailgoose help exit-codes`): 0 ok, 1 error (including an unreachable
API), 2 usage, 3 auth, 4 not found, 5 needs confirmation.

Each command declares the API operation it calls with `calls("operationId",
cmd)`, or `local(cmd)` if it calls none; `mailgoose commands --json` lists
them, and a test fails on any command that declares neither. API requests go
through `App.call`, which maps the API's error responses to exit codes.

## Server configuration

The server reads the environment variables Terraform sets on the ECS tasks
(`infra/modules/stack`, `common_env`):

| Variable | Use |
| --- | --- |
| `APP_ENV`, `AWS_REGION`, `PORT`, `PUBLIC_BASE_URL` | Basics. `PORT` defaults to 8080. |
| `DATABASE_HOST`, `DATABASE_PORT`, `DATABASE_NAME`, `DATABASE_SECRET_ARN` | Postgres on RDS. The user and password come from the RDS-managed secret, re-read when a connection fails (RDS rotates it). |
| `DATABASE_URL` | Local only. Takes precedence over the variables above. |
| `DATABASE_SSLMODE` | Defaults to `require` (RDS forces TLS). |
| `SEND_QUEUE_URL`, `SES_EVENTS_QUEUE_URL` | Queues the worker drains. Unset means idle. |
| `SES_CONFIGURATION_SET`, `SYSTEM_SENDING_DOMAIN`, `SES_IDENTITY_TAG` | Read now, used once sending lands. |
| `APP_SECRETS_JSON` | The `<env>/app` secret, a JSON object. |
| `MIGRATE_ON_START` | `false` to skip migrations at API start. |
| `RATE_LIMIT_PER_MINUTE` | Requests per minute per API key (or per IP without one), with bursts up to the same number. Default 600; `0` turns limiting off. Counted per API task. |
| `MAIL_SENDER` | `ses` or `fake`. Defaults to `fake` when `APP_ENV=local` and `ses` otherwise; prod refuses `fake`. The fake records mail and never calls AWS. |
| `MAIL_CAPTURE_DIR` | With the fake sender, write each message as an `.eml` file here. |

## API conventions

Every endpoint shares the plumbing in `internal/api` (MG-01):

- **Errors** are `{"error": {"code", "message", "hint"}}`. `code` is stable;
  branch on it, not on the message. Build them with `api.BadRequest`,
  `api.NotFound` and friends and write them with `api.WriteError`; any other
  error becomes a generic 500 and is only logged.
- **Lists** take `?limit=` (1 to 100, default 50) and `?cursor=`, and return
  `{"data": [...], "next_cursor": "..." | null}`. Handlers fetch `limit + 1`
  rows in keyset order after `PageRequest.After` and call `api.NewPage`.
- **Request IDs:** every response has `X-Request-Id`, and every log line made
  with the request's context carries it as `request_id`.
- **Idempotency:** POST, PUT, PATCH and DELETE with an `Idempotency-Key`
  header replay the stored response for 24 hours; the same key with a
  different request is a 422 `idempotency_key_reused`. Keys live in the
  `idempotency_keys` table.
- **Rate limits:** `RateLimit-Limit` and `RateLimit-Remaining` on every
  response, and a 429 `rate_limited` with `Retry-After` past the limit.

## First staging image

CI builds and tests but never deploys: GitHub OIDC deploys are off until the AWS
project allows the OIDC provider (see `docs/decisions.md`). Until then, push
images by hand from a machine signed in to AWS.

1. **Give the app secret a value.** Terraform created `mailgoose-staging/app`
   without one, and ECS can't start a task whose injected secret has no value.
   ```sh
   aws secretsmanager put-secret-value --region us-east-2 \
     --secret-id mailgoose-staging/app --secret-string '{}'
   ```
2. **Push the first image** with the tag Terraform's task definitions expect
   (`initial_image_tag`, default `bootstrap`). Tags in the ECR repository are
   immutable, so this tag can be pushed once.
   ```sh
   make push-ecr IMAGE_TAG=bootstrap
   ```
3. **Start the services.** In `infra/envs/staging/main.tf`, set
   `api_desired_count` and `worker_desired_count` to 1, then plan and apply.
4. **Check it.**
   ```sh
   curl https://api.staging.mailgoose.app/healthz
   ./bin/mailgoose status --api-url https://api.staging.mailgoose.app
   aws logs tail /ecs/mailgoose-staging-api --region us-east-2 --since 10m
   ```
   The API log should show `migrations applied` with version 1.

Later images get their own tags (`make push-ecr` defaults to the commit). With
deploys off, rolling one out means registering a new task definition revision
with that image and updating the service, which the deploy workflow will do
once OIDC is allowed.
