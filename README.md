# mailgoose

A deliberately small email marketing app: design and send emails, build signup
forms, and drive it all from a CLI that works with any AI agent.

## Layout

```
cmd/mailgoose/     Customer CLI (single static binary).
cmd/mailgoosed/    Server: `serve` (HTTP API), `worker` (SQS), `migrate`.
internal/api/      HTTP handlers. /healthz is the load balancer's check.
internal/worker/   SQS consumer for the send and SES events queues (stub handlers).
internal/db/       Postgres pool, Secrets Manager credentials, migrations.
internal/config/   Server settings from environment variables.
internal/cli/      CLI commands and its config file.
api/openapi.yaml   The API contract. Every endpoint gets a CLI command.
infra/             Terraform for AWS (see infra/README.md).
docs/              PRD, pricing, decisions.
```

One container image runs both ECS services. Its entrypoint is `mailgoosed`, and
ECS passes `serve` or `worker` (see `infra/modules/stack`). The customer CLI is
a separate binary, so no server code ships to customers.

## Run locally

Needs Go 1.27+ and Docker.

```sh
docker compose up --build        # Postgres, API on :8080, worker
curl localhost:8080/healthz      # {"status":"ok",...}
curl localhost:8080/readyz       # also checks Postgres
```

Or run the Go processes directly against the compose Postgres:

```sh
make run-api                     # migrates, then serves on :8080
make run-worker                  # idles: no queue URLs locally
```

The API applies pending migrations when it starts (`MIGRATE_ON_START`, default
`true`). A Postgres advisory lock stops two tasks from migrating at once. To
run them by hand: `mailgoosed migrate up|down|status`.

### Tests and checks

```sh
make test       # unit tests; database tests skip
make test-db    # also runs migrations and credential rotation against Postgres
make vet lint   # go vet, golangci-lint v2
make build      # bin/mailgoose and bin/mailgoosed
make image      # server image for linux/arm64
```

### CLI

```sh
./bin/mailgoose status --api-url http://localhost:8080
./bin/mailgoose config set api-url https://api.staging.mailgoose.app
printf '%s' "$KEY" | ./bin/mailgoose config set token -
./bin/mailgoose config show --json
```

Every command takes `--json`. Nothing prompts. `MAILGOOSE_TOKEN` and
`MAILGOOSE_API_URL` override the config file, so agents can run headless. Exit
codes: 0 ok, 1 error, 2 usage, 3 API unreachable or unhealthy.

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
