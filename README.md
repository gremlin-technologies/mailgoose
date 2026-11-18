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
internal/web/      Serves the built web app (WEB_DIR) at /.
web/               Web app: React, TypeScript, Vite, and the generated API client.
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
make web-check  # web app: ESLint, type check, Vitest
make image      # server image for linux/arm64, web app included
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
cmd)`, `callsAll([]string{...}, cmd)` when it calls several (as `login`
does), or `local(cmd)` if it calls none; `mailgoose commands --json` lists
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
| `SYSTEM_SENDING_DOMAIN` | mailgoose's own sending domain; sign-in codes come from `signin@` it. Defaults to `mail.localhost` when mail is captured (`MAIL_SENDER=fake`). Unset with SES, sign-up and sign-in answer 503. |
| `WEB_DIR` | The built web app (`web/dist`) the API serves at `/`. The image sets it to `/srv/web`; unset, the API serves no web app. |
| `WEB_ORIGINS` | Comma-separated origins, besides the API's own, that may send cookie-authenticated requests that change something, such as `https://app.staging.mailgoose.app`. Empty by default. |
| `SES_CONFIGURATION_SET`, `SES_IDENTITY_TAG` | Read now, used once sending lands. |
| `APP_SECRETS_JSON` | The `<env>/app` secret, a JSON object. |
| `MIGRATE_ON_START` | `false` to skip migrations at API start. |
| `RATE_LIMIT_PER_MINUTE` | Requests per minute per API key (or per IP without one), with bursts up to the same number. Default 600; `0` turns limiting off. Counted per API task. |
| `MAIL_SENDER` | `ses` or `fake`. Defaults to `fake` when `APP_ENV=local` and `ses` otherwise; prod refuses `fake`. The fake records mail and never calls AWS. |
| `MAIL_CAPTURE_DIR` | With the fake sender, write each message as an `.eml` file here. |

## Sign up and sessions

`POST /v1/signup` makes a pending account on the free plan and emails a
six-digit code; `POST /v1/sessions` with that code activates the account and
sets the `mg_session` cookie (HttpOnly, Secure, SameSite=Lax, 30 days). Codes
are stored as Argon2id hashes, last 10 minutes, work once and stop after 5
wrong tries. Locally the code lands in `.local/mail` (`make mail`):

```sh
curl -s localhost:8080/v1/signup -H 'Content-Type: application/json' \
  -d '{"email":"pat@example.com","account_name":"Pat","accepted_terms":true}'
make mail   # read the code
curl -si localhost:8080/v1/sessions -H 'Content-Type: application/json' \
  -d '{"email":"pat@example.com","code":"123456"}'
```

Signing in later is the same: `POST /v1/sign-in` emails a code to an address
with an account, and `POST /v1/sessions` turns it into a session.
`GET /v1/sessions/current` says who is signed in, `DELETE /v1/sessions/current`
signs out and `DELETE /v1/sessions` signs out everywhere. Code requests are
limited to 5 an hour per address and 20 an hour per IP, and a browser request
that changes something with the cookie must come from the API's own origin or
one listed in `WEB_ORIGINS`.

## Web app

`web/` is the web app: React and TypeScript, built with Vite, talking to the
API through the typed client in `web/src/api`. The API serves the built app
from its own origin (`WEB_DIR`): every GET that no API route matches gets the
app, so `/v1`, `/healthz` and `/readyz` stay the API's.

```sh
make web        # build web/dist; make dev then serves it at :8080
make web-dev    # live reload on :5173, passing /v1 to the API from make dev
make web-check  # ESLint, type check and Vitest
```

Pages so far: sign up, sign in with an emailed code, the CLI approval page
(`/device`), and the signed-in layout with a placeholder for every MVP page.
In development, sign-in codes land in `.local/mail` (`make mail`). CI runs
the web checks and a production build through `go test ./internal/web`.

## API keys

Scripts, agents and the CLI authenticate with `Authorization: Bearer <key>`.
`POST /v1/api-keys` (or `mailgoose keys create --name … --scope full|draft`)
makes a key and shows its secret once; only a SHA-256 of it is stored. Keys
made in production start `mg_live_`, everywhere else `mg_test_`; CLI tokens
from `mailgoose login` (MG-10) start `mg_cli_`. A `draft` key can do
everything except launch campaigns, change billing and manage keys: operations
that need more are marked `x-required-scope: full` in the spec, and the
handler calls `requireFull`. `TestDraftScope` sends a draft key to every
operation and checks the two agree.

```sh
curl -s localhost:8080/v1/api-keys -H "Authorization: Bearer $MAILGOOSE_API_KEY"
mailgoose keys list
mailgoose keys revoke <id> --yes
```

## CLI sign-in

People sign the CLI in through the browser; agents and scripts use an API key
in `MAILGOOSE_API_KEY` instead.

```sh
mailgoose signup --email pat@example.com --account-name "Pat's Bakery" --accept-terms
mailgoose signup --email pat@example.com --code 042917   # the code from the email
mailgoose login     # prints a code and a link; approve it in the browser
mailgoose whoami    # account, user, and how this CLI is signed in
mailgoose logout    # revokes the token and removes it from this computer
```

`login` calls `POST /v1/device-logins`, prints the `XXXX-XXXX` code and the
`/device` link on the web app (`PUBLIC_BASE_URL`), then polls
`POST /v1/device-logins/token` every 5 seconds. The person approves on the web
approval page (`/device?code=…`, which calls
`/v1/device-logins/{code}/approve`; signed-in web session only). Requests expire after 10 minutes;
denied or expired exits 3. The token (`mg_cli_…`, full scope) goes in the OS
keychain under the service `mailgoose` with the API URL as the account name,
or in the config file (mode 0600) where there is no keychain. Logins are kept
per API URL, so staging and production can both be signed in. Without a
terminal, `login` exits 2 and points to `MAILGOOSE_API_KEY`.

`signup` makes an account without a browser. Step 1 calls `POST /v1/signup`
and emails a six-digit code; step 2 trades it at `POST /v1/cli-tokens` for the
same kind of token `login` stores, and activates the account. A person has to
read the code from their email, so an agent can run both steps but needs the
code from them. Neither step prompts.

The CLI picks its credential in this order: `MAILGOOSE_API_KEY`, then the
login for the API URL in use, then `api_key` in the config file.
`GET /v1/whoami` answers for any credential, and
`DELETE /v1/cli-tokens/current` revokes the CLI token making the request.

## Account settings

`GET /v1/account` and `PATCH /v1/account` (or `mailgoose account get|update`)
read and change the account name, default From name and address, postal
address and IANA time zone. A PATCH changes only the fields sent, and `null`
removes an optional one. `missing_for_send` lists the settings a campaign
can't launch without, which today is the postal address.

```sh
mailgoose account update --postal-address-file address.txt --time-zone America/Chicago
```

## Credits

One credit sends one email to one recipient. `internal/credits` is the ledger:
every change is a row in `credit_transactions` (grant, purchase, reserve,
release, spend, fee, expiry) and the balance is their sum, never stored.
Debits lock the account row, so parallel reservations can't overdraw it. Free
accounts get 1,000 credits on activation and on each monthly anniversary, and
unused free credits expire at the next grant; both happen lazily when the
account is next read or debited.

```sh
curl -s localhost:8080/v1/credits -H "Authorization: Bearer $MAILGOOSE_API_KEY"
mailgoose credits
mailgoose credits history --limit 20
```

## Contacts

`internal/contacts` holds each account's one audience. Addresses are stored
in lower case and are unique per account. Only subscribed contacts are
campaign recipients (`contacts.Recipients`). Bounced and complained come only
from Amazon SES, and an unsubscribed, bounced or complained contact can't be
subscribed again through the API. Deleting such a contact leaves a hash of
its address on the suppression list, so adding it again keeps the status.

```sh
mailgoose contacts add pat@example.com --first-name Pat
mailgoose contacts list --status subscribed --name pat
mailgoose contacts update pat@example.com --status unsubscribed
mailgoose contacts delete pat@example.com --yes
```

A free account holds at most 500 counted contacts (subscribed or
unconfirmed); one more gets 402 `contact_limit`, with `details.limit` and
`details.count`. Paid accounts have no cap; `contacts.CountedContacts` gives
the count the monthly fee (MG-40) is based on.

Tags are basic labels; a campaign can go to the contacts with any of the
chosen tags. Put one on contacts by name, or on every contact matching a filter:

```sh
mailgoose tags create vip
mailgoose tags add vip pat@example.com sam@example.com
mailgoose tags add customers --email @example.com --status subscribed
mailgoose contacts list --tag vip
mailgoose tags delete vip --yes   # says how many contacts lost it
```

Import a CSV with `mailgoose import`. Columns are detected from common headers
or named with `--map`; contacts are `unconfirmed` unless you pass
`--permission-confirmed`. Imports run in the worker (`contacts.Service.RunImports`), and
the result lists every skipped row with its reason. A first import over 2,000
rows, or one that would pass the free limit, is held: its contacts aren't
campaign recipients until an admin approves it.

```sh
mailgoose import contacts.csv --map email=Email --tag newsletter --permission-confirmed --wait
mailgoose import status <id>      # progress, then skipped rows
mailgoose import list
```

Export to CSV with `mailgoose export` (all contacts, or `--tag`, `--status`
...). The file imports back as the same contacts and tags. For a person's
data request, `mailgoose contacts export-data <email>` prints everything
stored about them as JSON, and `mailgoose contacts delete <email> --erase
--yes` removes it all, keeping only a hash on the suppression list.

```sh
mailgoose export --output contacts.csv
mailgoose contacts export-data pat@example.com
mailgoose contacts delete pat@example.com --erase --yes
```

## Emails

`internal/emails` stores each account's emails. An email is a block document
(kind `blocks`, the default) or hand-written HTML (kind `html`), plus settings
both kinds share: subject, preview text, From, reply-to, background color and
content width. The kind is fixed at creation.

A block document is versioned JSON (`schema_version: 1`); its schema is
`BlockDocument` in `api/openapi.yaml`. Block types are heading, text (a small
Markdown subset), image, button, divider, spacer, columns (two columns of
blocks), social and footer. `emails.ParseDocument` is the only way in: it
refuses unknown block types and fields with a path such as
`blocks[2].columns[0].blocks[1].href`, fills defaults, gives blocks without an
id a new one (`b_` and ten characters), and adds the locked footer at the end
when it's missing. Ids never change on edits or moves.

Every save names the version it read; a stale one gets 409
`version_conflict` with `details.current_version`.

```sh
mailgoose email create --name "Spring menu" --file spring.json
mailgoose email get <id> --json > spring.json   # edit, then:
mailgoose email replace <id> --file spring.json  # uses the file's version
mailgoose email update <id> --subject "Spring is here" --from-email news@bakery.example
mailgoose email duplicate <id> --name "Summer menu"
mailgoose email list
mailgoose email delete <id> --yes
```

## Sending domains

Campaigns go out from an address at a domain the customer owns.
`internal/domains` makes the SES identity (Easy DKIM, MAIL FROM at
`bounce.<domain>`, tagged with the environment and account) and returns the
DNS records to add: three DKIM CNAMEs, an MX and SPF TXT for the MAIL FROM
subdomain, and a suggested DMARC TXT when the domain has none. The API
service re-checks pending domains every 5 minutes for 72 hours, then marks
them failed. `domains.RequireVerifiedFrom` is the check test send and launch
use on the From address.

```sh
mailgoose domain add example.com
mailgoose domain verify example.com   # exit 0 once verified
mailgoose domain list
mailgoose domain delete example.com --yes
```

With `MAIL_SENDER=fake` (the local default) SES is in memory and DNS answers
every record, so local domains verify on the first check.

**Not yet checked against real AWS.** Before relying on sending domains in
staging, add one domain there from the owner's machine and confirm IAM allows
`ses:CreateEmailIdentity` with `ConfigurationSetName` set. Some SES actions
also check the configuration-set resource, which the stack's IAM statements
don't list yet; if the call is denied, that resource needs adding in `infra/`.

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
