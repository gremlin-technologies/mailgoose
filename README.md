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

### End-to-end tests

Two tests go from sign-up to a sent campaign against the stack `make dev`
runs, reading sign-in codes and campaign mail from `.local/mail`. Contacts
are on Amazon SES's mailbox simulator (`success@`, `bounce@` and
`complaint@simulator.amazonses.com`); locally the worker makes up the
delivery, bounce or complaint event SES would send for each, so bounces are
handled end to end with no AWS. CI runs both on every pull request.

```sh
make web dev    # in one terminal: build the app, then run the stack
make e2e-cli    # the CLI, from an API key to a delivered campaign, an unsubscribe and a bounce
make e2e-web    # the web app in Chromium (Playwright), from sign-up to a launched campaign
```

`make e2e-web` needs Playwright's Chromium once: `cd web && npx playwright
install chromium`.

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
| `SEND_QUEUE_URL`, `SES_EVENTS_QUEUE_URL` | Queues the worker drains. Without `SEND_QUEUE_URL` the worker batches and sends campaigns itself, through a queue in memory (local). |
| `SEND_RATE_PER_SECOND` | Campaign emails sent a second, across every worker. Default 10. |
| `ACCOUNT_DAILY_SEND_LIMIT`, `NEW_ACCOUNT_DAILY_SEND_LIMIT` | The most campaign emails an account may send in any 24 hours (10,000 by default), and its limit in its first 7 days (1,000). In between the limit grows on a schedule (see [Account health](#account-health-and-send-limits)). An account's own `daily_send_limit` overrides both. |
| `IMPORT_HOLD_RISKY_PERCENT`, `IMPORT_HOLD_INVALID_PERCENT`, `IMPORT_HOLD_SHARED_PERCENT`, `IMPORT_HOLD_SHARED_MIN` | When an import is held for review: role plus disposable addresses over 10% of its valid rows, invalid rows over 5% of all rows, or at least 50% of its addresses, and at least 200, in other accounts' imports. Percentages are 1 to 100. |
| `OPS_ALERT_EMAIL` | Where to email an alert when an account is paused automatically, or a payment needs refunding or cancelling by hand in Stripe. Unset, these are only logged (as errors). |
| `ABUSE_EMAIL` | Where a suspended account's owner appeals: the Reply-To of the email that tells them about the suspension, and named in its errors. Defaults to `abuse@` the `SYSTEM_SENDING_DOMAIN`; that mailbox has to exist. |
| `SIGNUP_REVIEW` | Which new accounts wait for an admin to approve them before they can launch campaigns or take form signups (see [Sign-up review](#account-health-and-send-limits)): `all` (the default, for the beta and launch month), `flagged` or `off`. |
| `SYSTEM_SENDING_DOMAIN` | mailgoose's own sending domain; sign-in codes come from `signin@` it. Defaults to `mail.localhost` when mail is captured (`MAIL_SENDER=fake`). Unset with SES, sign-up and sign-in answer 503. |
| `WEB_DIR` | The built web app (`web/dist`) the API serves at `/`. The image sets it to `/srv/web`; unset, the API serves no web app. |
| `WEB_ORIGINS` | Comma-separated origins, besides the API's own, that may send cookie-authenticated requests that change something, such as `https://app.staging.mailgoose.app`. Empty by default. |
| `SES_CONFIGURATION_SET`, `SES_IDENTITY_TAG` | Read now, used once sending lands. |
| `APP_SECRETS_JSON` | The `<env>/app` secret, a JSON object. |
| `MIGRATE_ON_START` | `false` to skip migrations at API start. |
| `RATE_LIMIT_PER_MINUTE` | Requests per minute per API key (or per IP without one), with bursts up to the same number. Default 600; `0` turns limiting off. Counted per API task. |
| `MAIL_SENDER` | `ses` or `fake`. Defaults to `fake` when `APP_ENV=local` and `ses` otherwise; prod refuses `fake`. The fake records mail and never calls AWS. |
| `MAIL_CAPTURE_DIR` | With the fake sender, write each message as an `.eml` file here. |
| `STRIPE_SECRET_KEY`, `STRIPE_WEBHOOK_SECRET` | Stripe's secret key and the webhook endpoint's signing secret, from the `<env>/app` secret (or the environment, locally). Only prod may use a live key, and prod only a live one; a wrong key, or a key without the webhook secret, turns buying credits off and says why in the log, rather than stopping the server. |
| `CREDIT_PACKS` | The credit packs as `name=credits:cents,...`. Empty means the decided packs: `10k=10000:2000,50k=50000:7500,200k=200000:24000,1m=1000000:90000`. |
| `PLAN_PRICES` | One paid-plan unit's price in cents, as `month:cents,year:cents`. Empty means the decided prices: `month:500,year:5000`. |
| `WEB_RISK_API_KEY` | The Google Web Risk API key the email check looks links up with, from the `<env>/app` secret (or the environment, locally). Unset, links aren't looked up and the check warns (`link_safety_not_checked`); locally a fake lists only Google's test pages. |
| `IMAGES_BUCKET`, `IMAGES_BASE_URL` | The S3 bucket uploaded images go in, and the CDN address they're served from. Without `IMAGES_BASE_URL` the API serves them at `PUBLIC_BASE_URL/images/`, redirecting to short-lived presigned URLs (staging until it has a CDN). With no bucket outside local, image uploads answer 503. |
| `IMAGES_DIR` | Local only: a directory images go in instead of S3, served by the API at `/images/`. Defaults to `.local/images` when `APP_ENV=local` and there's no bucket; its URLs then start `PUBLIC_BASE_URL/images`. Prod refuses it. |

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
(`/device`), Domains (add, DNS records with copy buttons, check now), API keys
(create with a scope, secret shown once, revoke) and Settings (including
deleting the account), in the signed-in layout with a placeholder for every
other MVP page. Colors come from `docs/brand.md`; `web/src/styles.test.ts`
checks every text and background pair against WCAG 2.2 AA.
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

`DELETE /v1/account?confirm=<name>` (or `mailgoose account delete --confirm
<name>`, or Settings in the web app) deletes the account at once. `confirm`
must be the account's exact name, and only a session or a full-scope key can
do it. Emails, contacts, tags, imports, the suppression list, domains (and
their SES identities), API keys, CLI logins and sessions go; the credit ledger
stays, and the account row stays as `closed` with its name and settings
cleared, so payments and refunds can still be traced. The address can sign up
again afterwards.

```sh
mailgoose account delete --confirm "Pat's Bakery"
```

### Terms versions

The terms of service, privacy policy, anti-spam policy and data processing
agreement share one version, a date such as `2026-11-19`, kept with its
effective date and what changed in `internal/policy/policy.go`. `make
generate` copies them to the web app (`web/src/policyVersions.gen.ts`), where
each policy page shows the version and `/policies/changes` lists every
version. Sign-up records the version accepted, with the time and how (`web`,
`cli` or `api`), in `terms_acceptances`; each acceptance is a new row, never
overwritten, and the rows stay when an account is deleted.

To change the policies, add a version at the end of the list in
`internal/policy/policy.go` with its effective date and a line for each
change, then run `make generate`. Every account then has to accept it: the web
app asks before showing any page, `GET /v1/account` shows
`terms.acceptance_needed`, and launching is refused with
`new_terms_not_accepted` until the account accepts it, in the web app or with:

```sh
mailgoose account accept-terms --version 2026-11-19
```

Only the current version can be accepted. `mailgoose admin account` shows the
version an account accepted.

## Credits

One credit sends one email to one recipient. `internal/credits` is the ledger:
every change is a row in `credit_transactions` (grant, purchase, reserve,
release, spend, expiry) and the balance is their sum, never stored.
Debits lock the account row, so parallel reservations can't overdraw it. Free
accounts get 1,000 credits on activation and on each monthly anniversary, and
unused free credits expire at the next grant; both happen lazily when the
account is next read or debited.

```sh
curl -s localhost:8080/v1/credits -H "Authorization: Bearer $MAILGOOSE_API_KEY"
mailgoose credits
mailgoose credits history --limit 20
```

### The paid plan

The paid plan is a Stripe card subscription (`internal/billing`): $5 a month,
or $50 a year, chosen at checkout, plus any sales tax, which Stripe Tax works
out. That covers up to 2,500 counted contacts (subscribed or unconfirmed);
each further 10,000, or part of them, is another unit at the same price, so
2,501 to 12,500 contacts is $10 a month. `GET /v1/plan` shows the plan,
`POST /v1/plan/checkout` (full scope) makes a Checkout page that starts the
subscription, and `POST /v1/plan/portal` (full scope) links to Stripe's
customer portal, where the card is changed, invoices downloaded and the plan
cancelled.

```sh
mailgoose plan
mailgoose plan upgrade --yearly
mailgoose plan manage
```

Stripe then sends `customer.subscription.created`, `updated` and `deleted` to
`POST /hooks/stripe`. A live subscription (active, or past due while Stripe
retries the card) makes the account paid: no free monthly grant, no contact
cap. Past due or unpaid pauses sending with code `subscription_past_due` (a
launch is refused with that problem code) until the payment goes through,
when the pause lifts by itself; a pause for any other reason stays. A
cancelled plan runs to the end of its period, then the account is free again
and keeps its bought credits. Events older than the last one applied change
nothing. Deleting an account cancels its subscription at once, first.

The worker moves each subscription to the units its contacts make, hourly,
with no proration: growth is billed from the next renewal. The same job
writes the expiry of credits past their 24 months for accounts nobody has
looked at; the ledger also does it whenever an account is read.

### Buying credits

Paid accounts buy credits in packs through Stripe Checkout: 10k for $20, 50k
for $75, 200k for $240 and 1m for $900, plus any sales tax.
`POST /v1/credits/checkout` (full scope) makes a Checkout page for a pack and
returns its link; a free account gets 409 `plan_required`. The person pays
in the browser, and Stripe emails the receipt to the account's address.

```sh
mailgoose credits buy 10k
```

For a paid `checkout.session.completed` (or `async_payment_succeeded`) the
webhook adds the pack's credits as a purchase lasting 24 months. A paid
session whose amount, currency or credits don't match its pack adds nothing
and is logged as an error. The webhook checks the signature (5 minutes'
tolerance) and handles each event id once.

### Setting up Stripe

Put `STRIPE_SECRET_KEY` and `STRIPE_WEBHOOK_SECRET` in the `<env>/app`
secret. In the Stripe dashboard: add a webhook endpoint at
`<PUBLIC_BASE_URL>/hooks/stripe` for `checkout.session.completed`,
`checkout.session.async_payment_succeeded` and
`customer.subscription.created`, `.updated` and `.deleted`; turn on Stripe
Tax; set up the customer portal to allow updating the payment method and
cancelling (at the end of the period); and turn on email receipts. Prices
come from the app (`PLAN_PRICES`, `CREDIT_PACKS`), so no products need
making in Stripe.

To try it locally with Stripe's test mode, put a test key and the CLI's
webhook secret in the environment and forward events:

```sh
stripe listen --forward-to localhost:8080/hooks/stripe   # prints whsec_...
STRIPE_SECRET_KEY=sk_test_... STRIPE_WEBHOOK_SECRET=whsec_... go run ./cmd/mailgoosed serve
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
the count the paid plan's units (MG-40) are based on.

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
`--permission-confirmed`. `--list-source` (where the people came from) is
required and is kept as each contact's consent record. Imports run in the
worker (`contacts.Service.RunImports`), and the result lists every skipped row
with its reason and counts invalid, role and disposable-domain addresses. An
import is held when those counts are too high (`IMPORT_HOLD_RISKY_PERCENT`,
default 10, and `IMPORT_HOLD_INVALID_PERCENT`, default 5), when at least
`IMPORT_HOLD_SHARED_PERCENT` (50) of its addresses, and at least
`IMPORT_HOLD_SHARED_MIN` (200), are in other accounts' imports (compared as
hashes), when it's a first import over 2,000 rows, or when it would pass the
free limit: its contacts aren't campaign recipients until an admin approves it.

```sh
mailgoose import contacts.csv --list-source form --map email=Email --tag newsletter --permission-confirmed --wait
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

### Block operations

`internal/emails/blocks.go` changes one block at a time: list, add, update,
move and remove, at the top level or inside a columns block's columns. Each
works on the document's JSON and reads the result back through
`ParseDocument`, so a single change obeys every rule a whole document does.
A place is `after` a block, an `index` at the top level, or `parent_id` and
`column` (0 left, 1 right) with an optional `index`. With no place a block
goes last, which at the top level is just before the footer. The footer
can't be removed, moved or added, and nothing goes after it
(`footer_locked`); a place that can't be used is `invalid_position`.

```sh
mailgoose email blocks list <id>
mailgoose email blocks add <id> --type heading --text "Hi"
mailgoose email blocks add <id> --type columns --index 1
mailgoose email blocks add <id> --type image --src https://cdn.example/a.jpg --in <columns-id> --column 0
mailgoose email blocks add <id> --type social --data '{"links": [{"network": "instagram", "url": "https://instagram.com/x"}]}'
mailgoose email blocks update <id> <block-id> --text "Hello" --level 2
mailgoose email blocks move <id> <block-id> --after <other-block-id>
mailgoose email blocks remove <id> <block-id>
```

### Rendering

`internal/render` turns a block document into the email that goes out: a
table-based HTML part with inline styles (plus a small `<style>` block that
stacks columns and shrinks images on screens narrower than the content
width) and a plain-text part. `render.Compile` does the work once per email
and returns a template; `Template.Personalize` fills the merge tags for one
recipient, so a campaign compiles once and personalizes per contact.

Merge tags are `{{first_name}}`, `{{last_name}}`, `{{email}}` and
`{{unsubscribe_url}}`. The first three take a fallback for an empty value,
as in `{{first_name|there}}`. Values are HTML-escaped in the HTML part.
`render.Lint` reports unknown or malformed tags and unsafe links with the
block path, and `Compile` refuses a document with any. Every footer carries
the account's postal address and an unsubscribe link the customer can't
remove; `Compile` refuses an account without a postal address.
`CompileHTML` does the same job for an html email (below).

Golden files in `internal/render/testdata` show every block type at 600 and
480 pixels. After an intended change to the output, rewrite them with
`go test ./internal/render -update` and review the diff.

### HTML emails

An html email (`email create --kind html`) holds the customer's own markup,
replaced whole with `mailgoose email import-html <id> <file.html>` (`-` reads
standard input) or `PUT /v1/emails/{id}/html`. `emails.CheckHTML` reads it
first and any error stops the import, each with its line:

- markup that doesn't parse, an element that never closes, or an end tag
  that closes nothing
- `<script>`, `<form>`, frames, and embedded objects (`object`, `embed`,
  `applet`)
- external stylesheets: `<link rel="stylesheet">` and `@import`
- inline event handlers such as `onclick`, and `javascript:` or
  `vbscript:` addresses, in attributes or a `<style>` element
- links to `data:` addresses
- `<base>` and `<meta http-equiv="refresh">`, which send the reader elsewhere
- no link whose address is `{{unsubscribe_url}}` (`<a href="{{unsubscribe_url}}">`);
  one inside a comment, or the tag as plain text, doesn't count
- more than 102 KB, where Gmail starts clipping

Creating an html email or replacing its document runs the same check; empty
markup is allowed until something is imported.

CSS that major clients drop is a warning, and the import goes ahead. The
list is `emails.UnsupportedCSS`: `position`, flexbox and grid, transforms
and animation, CSS variables, filters and masks, `object-fit`, `@font-face`
and `@supports`.

`render.CompileHTML` prepares one for sending. It refuses markup with any
of these errors, so an email that was never imported can't be sent; then it adds the preview text as
a hidden line after `<body>` and, when the markup doesn't show every line of
the account's postal address, a footer with it before `</body>`. The plain
text part is made from the markup. The corpus in
`internal/emails/testdata/html` has a file for every rule; each starts with
the errors and warnings it should give.

```sh
mailgoose email create --name "Spring menu" --kind html
mailgoose email import-html 3f1c... ./spring.html
```

### Preview

`GET /v1/emails/{id}/preview?width=desktop|mobile` returns the email as it
will be sent, for both kinds, filled in with a sample contact (Alex Rivera,
alex@example.com). `mobile` wraps it in a page with a 375 pixel frame so its
phone layout applies. Until the account has a postal address, the footer
shows a placeholder where it goes. Merge tag problems come back as 422
`invalid_merge_tags` with each one's path; an html email with no markup yet
is 422 `empty_email`. The page carries a Content-Security-Policy that blocks
scripts, forms and requests other than images, styles and fonts.

`mailgoose email preview <id>` writes the HTML to standard output, or to
`--out file.html`. `--open` opens it in the browser, only at a terminal;
without `--out` it writes a file in the temporary directory first.

### Email check

`POST /v1/emails/{id}/check` and `mailgoose email check <id>` look an email
over before it's sent (`internal/emailcheck`). The answer lists `errors`,
which block a launch, and `warnings`, which don't, each with a code and,
where it applies, the block path, markup line or link:

- errors: `missing_subject`, `missing_from`, `unverified_from`,
  `missing_postal_address`, `empty_email`, `merge_tag`,
  `missing_unsubscribe`, `broken_link`, `too_large`, `invalid_html`,
  `unsafe_link`
- warnings: `insecure_link`, `link_unreachable`, `link_not_checked`,
  `link_safety_not_checked`, `missing_alt`, `near_size_limit`,
  `unsupported_css`

The CLI exits 1 when there are errors and 0 otherwise.

Links are fetched by `emailcheck.HTTPLinks`: a HEAD request each (GET when
a site refuses HEAD), 3 seconds per link, 10 seconds in all, 8 at a time,
at most 50 links and 5 redirects. It dials without a proxy and checks the
address of every connection, so a link, a hostname or a redirect that leads
to a private, loopback, link-local or other internal address, or to a port
other than 80 or 443, is never fetched. One account's checks fetch links at
most 10 times a minute (`linkChecksPerMinute`); checks past that still run,
with their links reported as not checked. Only `cmd/mailgoosed` wires it; a test server with no `Links` looks
at the addresses without fetching them.

### Link safety

Every web link is also looked up on Google Web Risk's malware, social
engineering (phishing) and unwanted software lists (`internal/linkrisk`), by
where it goes: one of mailgoose's own tracked `/t/c/` links is read for its
target. A listed link is an `unsafe_link` error in the check; it stops a test
send, and stops a launch and pauses the account's sending, with a reason
naming the link, an `auto_pause` entry in the admin log and an alert to
`OPS_ALERT_EMAIL`. Answers are cached in memory (a listed link for the time
Web Risk gives, at most a day; a clean one for 30 minutes), so a campaign's
links are looked up once.

The lookup never holds sending up: without `WEB_RISK_API_KEY`, or when Web
Risk errors or takes more than 2 seconds, links pass with a
`link_safety_not_checked` warning, and the miss is logged. Tests and local
runs use `linkrisk.Fake`; locally, without a key, it lists Google's test pages
(`https://testsafebrowsing.appspot.com/s/malware.html`, `phishing.html` and
`unwanted.html`).

#### Setting up Web Risk

1. In the Google Cloud console, create a project for mailgoose (or pick one)
   with billing turned on; Web Risk's first 100,000 lookups a month are free.
2. Enable the **Web Risk API** (APIs & Services, Library, "Web Risk API").
3. Create an API key (APIs & Services, Credentials, Create credentials, API
   key) and restrict it to the Web Risk API.
4. Add it to the `<env>/app` secret as `"WEB_RISK_API_KEY": "<key>"`, next to
   the Stripe keys, then start new API tasks (force a new deployment of the
   API service) so they read it.

## Images

`internal/images` stores the pictures used in image blocks. An upload is
checked by what the file holds, not its name: JPEG, PNG, GIF and WebP up to
5 MB, and up to 1,000 per account. It's stored as
`<account id>/<32 random hex>.<ext>` and served from the CDN in front of the
store (`IMAGES_BASE_URL`), cached for a year since a name never changes what
it holds. Without a CDN the API serves them at `/images/`, redirecting each
request to a presigned URL that lasts five minutes. The answer's `url` goes straight into an image block's `src`.

```sh
mailgoose image upload ./logo.png        # prints the URL
mailgoose email blocks add <id> --type image --src <url> --alt "Our logo"
mailgoose image list
```

The API takes the file base64-encoded in JSON (`POST /v1/images`, body up to
7 MB), like every other request. Images can't be deleted one at a time,
since sent emails keep pointing at them; deleting the account deletes them
all. Locally they go in `.local/images` and the API serves them itself.

## Campaigns

`internal/campaigns` keeps an account's campaigns; the API has
`/v1/campaigns` and the CLI `mailgoose campaign create|get|list|update|delete|count|report`.
A campaign is a name, an email, recipients and a send time:

```
mailgoose campaign count --tag VIP
mailgoose campaign create --name "VIP preview" --email <email-id> --tag VIP --send-at "2026-12-01 09:00"
```

Recipients are every subscribed contact (`--all`, `{"all": true}`) or the
subscribed contacts with any of some tags (`--tag`, `{"tag_ids": [...]}`).
They're picked and counted only through `contacts.Recipients` and
`contacts.CountRecipients`. `recipient_count` on a campaign, and
`POST /v1/campaigns/recipient-count`, count them now.

Statuses are draft, scheduled, sending, sent, cancelled, paused and failed.
`campaigns.CanMove` holds the allowed moves and `campaigns.SetStatus`
enforces them; only drafts can be edited, tested or deleted, since a
launched campaign was confirmed as it is. Every edit moves `version` on.

### Test sends

`POST /v1/campaigns/{id}/test` and `mailgoose campaign test <campaign-id>`
send one copy of the campaign's email to the account owner, or with `to` /
`--to` to up to 5 addresses that are the owner's or on a verified sending
domain of the account (subdomains count). The subject starts `[Test] `,
merge tags are filled from the contact with that address when there is one,
and no credits are used. An account can send 50 test emails in any rolling
24 hours (`test_sends`); past that it's a 429 with `Retry-After`.

`internal/sending` does the work. `sending.Prepare` is the step tests share
with launch and the send workers: it works out the From (the email's own,
else the account's), checks it's on a verified domain, checks the subject,
postal address and merge tags, and compiles the email. When anything's
wrong it lists every problem with its fix (`422 not_ready`,
`details.problems`), and the CLI prints them all. Only drafts can be
tested (409 otherwise).

A test's unsubscribe link and `List-Unsubscribe` header point at `/u/test`,
a page that says it was a test and changes nothing. After a test the
campaign's `last_test` holds when it went out and the campaign and email
versions it was made from, for MG-30's "tested since the last edit" check.

### Launching

`POST /v1/campaigns/{id}/launch` and `mailgoose campaign launch <campaign-id>`
take two steps. Without `confirm` the API checks the campaign can go and
answers `409 confirmation_required` with a summary (subject, From,
Reply-To, recipient count, send time, credits and balance, check warnings)
and a one-time code like `K7QM-2PXD`; nothing is sent. The CLI prints the
summary and the code and exits 5. With `confirm` set to the code (`--confirm`
in the CLI) it launches.

A code lasts 15 minutes, allows 5 wrong tries, is stored hashed, and is for
the campaign and email versions it was issued for: an edit to either makes
it stale. There's one code per campaign, so a new summary replaces the old
code. Case, dashes and spaces don't matter when typing it. An agent shows
the summary to its person and confirms only after they say yes.

A launch is refused (`422 not_ready`, every problem listed with its fix)
when the campaign hasn't been tested since its last edit (`untested`), the
email has check errors (`check_failed`), the From, subject, postal address
or merge tags aren't ready (as for tests), the account's sending is paused
(`account_paused`), it reaches nobody (`no_recipients`), the credits fall
short (`credits_short`, with `needed`, `balance` and `short` in its
details), or its send time has passed (`send_at_passed`). A wrong, expired
or stale code is `422 invalid_confirmation` with `details.reason`.

Confirming does everything in one transaction: it locks the campaign,
checks the code and the campaign again, fixes the recipients
(`campaign_recipients`, one row per contact), reserves one credit each
(`reserve` under `campaign:<id>`), keeps a copy of the compiled email, From
and Reply-To in `campaigns.launch`, moves the campaign to `sending` (or
`scheduled` when it has a send time) and writes a `send_queue` row for the
send pipeline (MG-31) to pick up at `run_at`. Launching a launched campaign
again returns the same result, and so does a retry with the same
`Idempotency-Key`. Draft keys get 403. After a launch the campaign can't be
edited or tested; its `launch` field shows when, to how many and the credits
held.

### Sending

The send pipeline (`internal/sending/pipeline.go`) takes it from there:

- **Dispatcher.** Every 2 seconds it takes the due `send_queue` rows (each
  locked by one dispatcher at a time). It starts a `scheduled` campaign at
  its time, then marks the next recipients enqueued, as many as the
  account's daily limit leaves, and puts them on the send queue in batches
  of about 10 seconds' sending (10 to 100). It runs in the API when
  `SEND_QUEUE_URL` is set (only the API's role may send to the queue), and
  in the worker otherwise.
- **Workers.** Each takes one batch at a time and, per recipient, waits for
  the next send slot (`send_pace`, shared by every worker), claims the row
  (`sending`) in its own transaction, renders the copy with its
  unsubscribe link and `List-Unsubscribe` headers, sends it and marks it
  `sent` with the message id. A contact no longer subscribed is `skipped`
  and its credit given back.
- **Exactly once.** A row is sent only from `queued`, so a batch delivered
  twice sends nothing twice. A row left `sending` (the worker died
  mid-send) might have gone out, so it's never sent again: after 10 minutes
  it ends `failed` with its credit back.
- **Failures.** A refused send puts the row back to `queued` and the batch
  is retried. On the fifth try the rows that still fail end `failed` with
  their credits back, and the message goes to the dead-letter queue.
- **Finishing.** When no row is `queued` or `sending`, the campaign moves
  to `sent` (or `failed` if nothing went out). A paused account or
  campaign holds its rows until it's resumed.

### Scheduled campaigns

A campaign launched with a send time is `scheduled`. The worker checks
every 15 seconds and starts each one whose time has come (the dispatcher
also starts one it reaches first); the update only takes campaigns still
`scheduled`, so each starts once however many workers run.

Until then it can be cancelled or moved, by API or CLI (full-scope keys):

- `POST /v1/campaigns/{id}/cancel`, `mailgoose campaign cancel <id> --yes`:
  nobody gets it, its recipients are `skipped` and the credits reserved at
  launch come back. Cancelling again changes nothing.
- `POST /v1/campaigns/{id}/reschedule` with `send_at`,
  `mailgoose campaign reschedule <id> --at <time>`: a new time in the
  future, at most a year ahead.

Once it's `sending`, both answer `409 campaign_locked`. Times are stored
and returned in UTC. The CLI shows them in the account's time zone
(`mailgoose account update --time-zone`), and reads a `--send-at` or `--at`
without an offset (`"2026-12-01 09:00"`) in that zone.

### Bounces and complaints

SES publishes each message's events to SNS, which delivers them raw to the
`ses-events` queue. The worker (`internal/sesevents`) stores deliveries,
bounces, complaints, rejects and delivery delays in `send_events`, against
the send record (`campaign_recipients`, matched by SES's message id), and
acts on them in the same transaction, as soon as the worker receives the
event:

- **Hard bounce** (`Permanent`): the contact becomes `bounced` at once.
- **Complaint:** the contact becomes `complained` at once. A complained
  contact stays so, whatever bounces later.
- **Soft bounce** (`Transient` or `Undetermined`): counted in
  `contacts.soft_bounces`; the third in a row makes the contact `bounced`.
  A delivery sets the count back to 0.
- **Rejects and delivery delays** are stored and change no contact.

Only a campaign copy or a double opt-in confirmation changes a contact;
bounces of test sends and of mailgoose's own mail are stored and nothing
more. Each event is stored once, by a hash of its body, so a redelivered
event isn't counted twice. Open and click events from SES are dropped
(opens and clicks come only from mailgoose's own tracking), and so are
events that can't be read, with a warning in the log; only a database
error leaves a message on the queue to be tried again.

### Open and click tracking

Tracking is on for a new campaign; `tracking: false` (CLI
`--tracking=false`) turns it off, and it's fixed at launch like the rest.
A tracked copy gets, after its merge tags are filled in:

- **An open pixel:** a 1x1 image at `<app URL>/t/o/<token>`, just before
  `</body>`.
- **Tracked links:** every `<a href>` to an `http` or `https` address
  becomes `<app URL>/t/c/<token>`. The unsubscribe link, `mailto:` and
  other links, and links over 2,000 characters are left as they are, and so
  is the plain-text part. Test sends are never tracked.

A token (`internal/tracking`) names the account, campaign and send record,
and for a click the link's position in the email and where it goes, signed
with the account's unsubscribe key under another label. The redirect needs
no database lookup but that key (cached), so it can't be pointed anywhere
else: a changed token gets a 400 page and no `Location`. `GET /t/c/<token>`
records a click (and an open, if the copy has none yet, since the pixel may
have been blocked) in `engagement_events` and answers 302. `GET /t/o/<token>`
always answers with the pixel, recording only a valid token. Nothing about
the person's IP or browser is stored. Like `/u/`, these are recipient pages
outside the spec and the API's sign-in, with their own per-IP limit of
20,000 a minute (mail providers fetch images through a few proxies).
`TestClickLatency` and `BenchmarkTrackedClick` (`internal/api`) time the
redirect.

### Reports

`GET /v1/campaigns/{id}/report` and `mailgoose campaign report <id>`
(`--json` for the API's shape) show a campaign's copies sent, delivered,
hard bounced, complaints, unsubscribes, unique opens (always flagged
estimated), unique and total clicks, credits used, and clicks per link.
Both read `reports.Service.Campaign`, one query over `campaign_recipients`,
`send_events`, `engagement_events`, `unsubscribes` and the credit ledger,
run when asked, so the numbers move as soon as an event is stored. Counts
of people count each copy once. A link whose address differs per copy
(merge tags) shows only its site, so nobody's own link is shown.

Raw events are kept 12 months (REP-5). The worker's pruner runs hourly: a
campaign launched more than 12 months ago (and finished) has its report
saved in `campaign_reports` and its raw events deleted; after that its
report is the saved one, with `archived: true`. Any other open, click or
SES event older than 12 months is deleted too.

### Account health and send limits

**Send limits.** A new account may send 1,000 campaign emails in any 24
hours for its first 7 days. After that its limit doubles each week while
its history is clean (2,000 in week 2, 4,000 in week 3, 8,000 in week 4),
up to `ACCOUNT_DAILY_SEND_LIMIT` (10,000). History is clean when the
account's campaigns over the last 30 days are under both thresholds below
(judged only once they've gone to 500 or more), and none of them paused it.
An account that isn't clean is held at 1,000 until it is. An account's own
`daily_send_limit` replaces the schedule.

**Automatic pause.** Once a campaign has gone to 500 or more people, its
account's sending is paused if more than 0.3% of them complained or more
than 5% hard bounced. The check runs on each complaint and hard bounce, so a
campaign is stopped while it's still sending, and one whose complaints come
days later is still caught. A campaign pauses its account once; after the
pause is lifted it doesn't pause it again. The account is told why (the
reason is shown when it tries to launch), the pause is logged, and an alert
goes to `OPS_ALERT_EMAIL`.

**Pausing mid-send.** A paused account's campaigns stop before their next
email: the rows not yet sent are handed back and wait, and the campaign
carries on where it stopped when the account is unpaused.

**Suspension.** For abuse, an admin suspends an account (`mailgoose admin
suspend`), and an account paused automatically for the second time in 30
days is suspended automatically. Its API keys and CLI logins are revoked,
web sessions end, scheduled and sending campaigns are cancelled (unsent
reservations come back; other credits are untouched), and its forms stop
taking signups. Its owner can still sign in to a page that says why and,
for 30 days, downloads their contacts and campaigns; every other request is
403 `account_suspended`. The owner is emailed why, with `ABUSE_EMAIL` as the
Reply-To for appeals, and an alert goes to `OPS_ALERT_EMAIL`. Unsuspending
restores sign-in and sending; old keys stay revoked.

**Sign-up review.** New accounts can wait for an admin to approve them:
every one (`SIGNUP_REVIEW=all`, the default), only flagged ones (`flagged`),
or none (`off`). A sign-up is flagged when its address is on the vendored
disposable-domain list (`internal/emaillists`), when its IP made more than 3
accounts in 24 hours, or when its IP or company domain (not a free-mail one
like gmail.com) belongs to a suspended account. Sign-up IPs are kept only for
this and deleted after 90 days. A waiting account can sign in, build emails,
import contacts and send tests to its own addresses, and sees a note that
it's being reviewed; it can't launch (`account_in_review`) or take form
signups. When it confirms its address, `OPS_ALERT_EMAIL` gets an email.
`mailgoose admin signups` lists waiting accounts with their flags;
`approve-signup` lets one go, and `reject-signup` suspends it.

### Admin tools

mailgoose staff use `mailgoose admin` (the `/v1/admin/*` endpoints). They
need a person signed in with `mailgoose login` (or the web app) whose user is
an admin; API keys are refused, even an admin's. Every action takes a
`--reason` and is logged in `admin_actions` with who, why and when
(`mailgoose admin log`); the log is kept when the account is deleted.

| Command | Does |
| --- | --- |
| `mailgoose admin find <text>` | Finds accounts by id, or part of the owner's address, the name or a sending domain. |
| `mailgoose admin account <id>` | Status, pause, daily limit, contacts, credits, 30-day rates, held imports and live campaigns. |
| `mailgoose admin pause <id> --reason` | Pauses sending (campaigns stop before their next email). The reason is shown to the account. |
| `mailgoose admin unpause <id> --reason` | Lifts a pause; paused campaigns carry on. |
| `mailgoose admin suspend <id> --reason` | Suspends the account (see [Suspension](#account-health-and-send-limits)) and emails the owner why and how to appeal. |
| `mailgoose admin unsuspend <id> --reason` | Lifts a suspension; API keys stay revoked and a pause stays. |
| `mailgoose admin signups` | New accounts waiting for their sign-up review, oldest first, with what flagged them. |
| `mailgoose admin approve-signup <id> [--reason]` | Approves one: it can launch campaigns and take form signups. |
| `mailgoose admin reject-signup <id> --reason` | Rejects one and suspends it, emailing the owner why and how to appeal. |
| `mailgoose admin stop-campaign <id> --reason --yes` | The kill switch: nobody else gets it, unused credits come back, it ends `cancelled`. |
| `mailgoose admin approve-import <id> --reason` | Lets a held import's contacts be mailed. |
| `mailgoose admin grant-credits <id> --amount --reason` | Adds up to 1,000,000 credits, lasting 24 months. |
| `mailgoose admin log [--account <id>]` | The log, newest first. |

The admin role is granted only from the server, by someone who can reach the
database: `mailgoosed admin grant <email> --reason "..."` (and `revoke`).
There's no API for it.

## Unsubscribing

Every campaign email's `{{unsubscribe_url}}` is `<app URL>/u/<token>`, and
`render` adds the RFC 8058 `List-Unsubscribe` and `List-Unsubscribe-Post`
headers for the same address (`render.UnsubscribeHeaders`). The token
(`internal/unsubscribe`) names the account, contact and campaign, signed
with a key the account gets the first time it needs one; it never expires.

- `GET /u/<token>` shows a page asking to confirm, and changes nothing, so a
  link checker that opens it doesn't unsubscribe anyone.
- `POST /u/<token>` unsubscribes: the page's button, or a mail app's
  one-click POST of `List-Unsubscribe=One-Click`. It's immediate, needs no
  sign-in, records the time, campaign and source in `unsubscribes`, and
  doing it again changes nothing.
- A changed, cut-off or unknown-format token gets a friendly 400 page and
  changes nothing.

These are pages for recipients, not API operations: they're outside the
OpenAPI spec and the API's sign-in, cross-origin and idempotency layers,
with their own per-IP limit of 1,200 a minute (one-click POSTs come from a
mail provider's few servers). Request logs show the path as
`/u/[token]` (and `/t/o/[token]`, `/t/c/[token]` for tracking).

## Signup forms

`internal/forms` keeps an account's signup forms; the API has
`/v1/forms` and the CLI `mailgoose form create|get|list|update|delete|embed`.
A form asks for an email and, optionally, a first and last name, puts its
tags on everyone who signs up, and shows a success message or sends people
to an https redirect. Double opt-in is on by default, with an editable
confirmation subject and text.

```
mailgoose form create --name Footer --first-name optional --tag Newsletter
mailgoose form embed <form-id> > signup.html
```

`form embed` (`GET /v1/forms/{id}/embed`) gives a plain HTML form that posts
to `/f/{id}` on the app's URL and works without JavaScript, and an optional
script that posts in the page and shows the result there. The form carries
a hidden honeypot field (`website`) and `_mg_ts`, a timestamp signed with
the form's own key (`forms.Stamp`), which submissions are checked against.
`form get --json` output can be edited and passed back with `form update
--file`.

### Submissions and double opt-in

`internal/signups` takes posts to `/f/{form_id}` (a public page outside the
spec, like unsubscribing; `internal/api/signups.go`). It answers any origin
(CORS `*`), takes a form post or JSON, and answers JSON when asked
(`Accept: application/json`, which the embed script sends) or else
redirects to the form's redirect URL or shows a success page.

- **Bots:** a filled honeypot, or a post under 2 seconds after the page
  opened (when the script says), gets the normal success answer and is
  dropped. A missing or forged `_mg_ts` is a 400.
- **Limits:** 10 posts a minute per IP and 600 a minute per form (429 with
  Retry-After), plus the free plan's contact limit (409 `list_full`).
- **Double opt-in:** a new address is added unconfirmed and gets a
  confirmation email from the account's From on its verified domain, free of
  credits. The link (`/confirm/{token}`, 7 days) shows a page whose button
  confirms, so mail scanners opening it change nothing. A double opt-in form
  with no verified From refuses signups (503 `form_not_ready`) and `form
  get` says what to fix.
- **Existing contacts:** a subscriber just gets the form's tags. An
  unsubscribed address comes back only by confirming, even through a form
  without double opt-in. Bounced and complained addresses get no email.
  The answer is the same whatever happened, so a form doesn't show who's on
  a list.
- **Consent records:** each signup stores the time, IP, form and the form's
  wording, and the confirmation time. `contacts export-data` lists them;
  the CSV export carries the latest in its `signup_` columns. `form get`
  shows the form's signup and confirmation counts.

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
