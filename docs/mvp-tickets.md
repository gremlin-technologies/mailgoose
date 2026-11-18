# mailgoose MVP tickets

Approved by the owner on Nov 18, 2026. Each ticket is also a GitHub issue in
this repo (title `MG-NN: ...`, body = its section below), with one GitHub
milestone per milestone here. If a ticket changes, change both.

Requirement IDs (AUTH-1, CAM-4 ...) point to `docs/mvp-requirements.md`.

## How tickets work

- **Pick up** the lowest-numbered ticket whose dependencies are all merged.
  Tickets in the same milestone with no dependency between them can run in
  parallel.
- **One ticket, one PR.** Branch `mg-NN-short-name`, PR title `MG-NN: ...`, and
  the PR description copies the ticket's acceptance criteria as a checklist,
  each ticked with how it was checked (test name, command output or
  screenshot).
- **Merge.** The reviewer agent merges when CI is green, every acceptance
  criterion is met and the PR follows `CLAUDE.md`. Tickets marked
  **Owner merges** touch `infra/` or `.github/workflows/`; the reviewer
  reviews them and the owner merges.
- **Needs owner** means part of the ticket is something only the owner can do (an
  AWS apply, a Stripe or DNS setting, legal text). The code part still goes
  through a PR; the PR says exactly what the owner has to do.
- **Size:** S is about half a day of focused work, M about a day or two. Nothing
  is bigger; a ticket that grows past M gets split before it's started.

### Done for every ticket

These apply to every ticket on top of its own criteria, and the reviewer checks
them each time:

1. CI is green: build, lint, unit tests, integration tests against Postgres.
2. Every new or changed endpoint is in `openapi.yaml`, has a CLI command with
   `--json`, and the CLI parity check passes (from MG-04 on).
3. New behaviour has tests. Handlers have integration tests; pure logic has
   unit tests.
4. Errors use the shared error shape (from MG-01 on).
5. No secrets, keys or real customer data in the repo.
6. Migrations are additive or reversible.
7. If the work changes or settles a decision, `docs/decisions.md` gets a dated
   entry in the same PR. An open product question gets a reversible default,
   named in the PR, and is left open for the owner.

## Overview

| # | Ticket | Depends on | Size | Merge |
| --- | --- | --- | --- | --- |
| **M0 Foundation** | | | | |
| MG-00 | Go scaffold (in progress in another thread) | | M | reviewer (app PR), owner (CI workflow PR) |
| MG-01 | API conventions: errors, pagination, request IDs, idempotency, rate limits | 00 | M | reviewer |
| MG-02 | OpenAPI pipeline: lint, Go types, TS client, contract tests | 00 | M | reviewer |
| MG-03 | CLI framework: output, exit codes, config, no prompts | 00 | M | reviewer |
| MG-04 | CLI parity check in CI | 02, 03 | S | reviewer |
| MG-05 | Local dev environment with fake SES | 00 | S | reviewer |
| MG-06 | Mail sender interface: SES and fake adapters | 00 | S | reviewer |
| **M1 Accounts** | | | | |
| MG-07 | Sign up with an emailed code | 01, 05, 06 | M | reviewer |
| MG-08 | Web sign-in and sessions | 07 | S | reviewer |
| MG-09 | API keys, scopes and bearer auth | 08, 03 | M | reviewer |
| MG-10 | CLI device login, logout, whoami | 09 | M | reviewer |
| MG-11 | Account settings | 09 | S | reviewer |
| MG-12 | Credit ledger and free monthly grant | 07 | M | reviewer |
| **M2 Domains** | | | | |
| MG-13 | Sending domains: add, DNS records, verify | 09, 06 | M | reviewer |
| **M3 Contacts** | | | | |
| MG-14 | Contacts, statuses and suppression list | 09 | M | reviewer |
| MG-15 | Tags | 14 | S | reviewer |
| MG-16 | Contact limits | 14, 12 | S | reviewer |
| MG-17 | CSV import with report and review hold | 15, 16 | M | reviewer |
| MG-18 | CSV export and per-contact data requests | 15 | S | reviewer |
| **M4 Designer backend** | | | | |
| MG-19 | Email documents: block schema and CRUD | 09, 02 | M | reviewer |
| MG-20 | Block operations | 19 | S | reviewer |
| MG-21 | Renderer: blocks to email HTML and plain text | 19, 11 | M | reviewer |
| MG-22 | HTML import and validation | 19 | M | reviewer |
| MG-23 | Image storage bucket and CDN (Terraform) | | S | owner (infra), needs owner |
| MG-24 | Image upload | 23, 19 | S | reviewer |
| MG-25 | Preview | 21, 22 | S | reviewer |
| MG-26 | Email check | 21, 22, 13 | S | reviewer |
| **M5 Sending** | | | | |
| MG-27 | Campaigns: create, recipients, schedule, statuses | 19, 15 | M | reviewer |
| MG-28 | Unsubscribe links, one-click header endpoint and page | 21, 14 | M | reviewer |
| MG-29 | Test send | 27, 21, 13 | S | reviewer |
| MG-30 | Launch: summary, confirm code, credit reservation | 29, 26, 12 | M | reviewer |
| MG-31 | Send pipeline workers | 30, 28 | M | reviewer |
| MG-32 | Scheduled campaign runner, cancel and reschedule | 30 | S | reviewer |
| MG-33 | SES events: deliveries, bounces, complaints | 31 | M | reviewer |
| MG-34 | Send limits, account health auto-pause, kill switch, admin CLI | 31, 33 | M | reviewer |
| **M6 Forms** | | | | |
| MG-35 | Forms and embed code | 15 | S | reviewer |
| MG-36 | Form submissions, double opt-in, consent records | 35, 13, 16 | M | reviewer |
| **M7 Reports** | | | | |
| MG-37 | Open and click tracking (our own; SES tracking off) | 31 | M | reviewer; owner merges the infra PR |
| MG-38 | Campaign reports | 33, 37 | S | reviewer |
| **M8 Billing** | | | | |
| MG-39 | Buy credits with Stripe Checkout | 12 | M | reviewer, needs owner |
| MG-40 | Monthly account fee and credit expiry | 39, 16 | M | reviewer |
| **M9 Web app** | | | | |
| MG-41 | Web app shell and sign-in pages | 02, 08 | M | reviewer |
| MG-42 | Contacts and tags pages | 41, 17, 18 | M | reviewer |
| MG-43 | Domains, API keys and settings pages | 41, 13, 11 | S | reviewer |
| MG-44 | Designer: canvas, blocks, drag and drop | 41, 20, 24 | M | reviewer |
| MG-45 | Designer: properties panel, text editing, preview, autosave | 44, 25 | M | reviewer |
| MG-46 | HTML code editor with live preview | 41, 22, 25 | S | reviewer |
| MG-47 | Campaign pages: create, test, launch, schedule | 41, 30, 32 | M | reviewer |
| MG-48 | Report page | 47, 38 | S | reviewer |
| MG-49 | Forms pages | 41, 36 | S | reviewer |
| MG-50 | Credits and billing page | 41, 39 | S | reviewer |
| MG-51 | Dashboard and getting-started checklist | 43, 42, 45, 47 | S | reviewer |
| **M10 Launch** | | | | |
| MG-52 | Deploy pipeline to staging and prod | 00 | M | owner (workflows), needs owner |
| MG-53 | Monitoring and alarms | 52, 31 | M | owner (infra) |
| MG-54 | CLI releases: binaries, Homebrew, install script | 03 | S | owner (workflows), needs owner |
| MG-55 | Agent docs: `mailgoose docs`, help examples, API docs, llms.txt | 04, 30 | S | reviewer |
| MG-56 | Security scanning in CI | 00 | S | owner (workflows) |
| MG-57 | Policy pages and signup links | 41 | S | reviewer, needs owner |
| MG-58 | End-to-end tests: web and CLI-only, signup to delivered | 51, 55 | M | reviewer |
| MG-59 | Accessibility and browser pass | 51 | S | reviewer |
| MG-60 | Launch checklist | all | S | needs owner |

Critical path to a first CLI-only send: 00, 01, 02, 03, 05, 06, 07, 08, 09, 11, 12,
13, 14, 15, 19, 21, 22, 26, 27, 28, 29, 30, 31. The web app (M9) can start once 41's
dependencies land and runs alongside M5 to M8.

---

## M0 Foundation

### MG-00: Go scaffold

Being built tonight in another thread; listed so everything else can depend on
it. Expected to cover: Go module, `cmd/` for `mailgoose serve`,
`mailgoose worker` and the CLI, Dockerfiles, a first migration, an OpenAPI stub,
and a CI workflow. It is split into two PRs: the Go app PR goes through the
reviewer like any other ticket, and the CI workflow PR (`.github/workflows/`) is
left for the owner to merge.

### MG-01: API conventions

Requirements: API-3, API-4, API-5.

Shared HTTP plumbing every handler uses.

Acceptance criteria:

- [ ] Every error response has the shape `{"error": {"code", "message", "hint"}}` with a stable string `code`; a test asserts the shape for 400, 401, 403, 404, 409, 422, 429 and 500.
- [ ] A cursor pagination helper returns `{"data": [...], "next_cursor"}`; a test pages through 250 rows in pages of 100 with no gaps or repeats.
- [ ] Every response carries an `X-Request-Id` header, and the same ID is in the request's log line.
- [ ] Idempotency middleware: a repeated request with the same `Idempotency-Key` and body within 24 hours returns the stored response without running the handler again; the same key with a different body returns 422 `idempotency_key_reused`.
- [ ] Per-key rate limiting returns 429 with `RateLimit-Limit`, `RateLimit-Remaining` and `Retry-After` headers; limits are configurable.

### MG-02: OpenAPI pipeline

Requirements: API-1, API acceptance.

Acceptance criteria:

- [ ] `openapi.yaml` (OpenAPI 3.1) at a fixed path is the source of truth, and CI fails if it doesn't lint (Redocly or Spectral).
- [ ] Go server types are generated from it (oapi-codegen or equivalent) by one `make generate`, and CI fails if generated code is out of date.
- [ ] A TypeScript client is generated from it into the web app package by the same command.
- [ ] A contract test runs every operation in the spec against a test server and fails if any operation has no handler or returns a response that doesn't match its schema.

### MG-03: CLI framework

Requirements: CLI-3, CLI-4, CLI-7, AUTH-4 (env var).

Acceptance criteria:

- [ ] Commands are built on one framework (Cobra or equivalent) with a registry that lists every command and the API operation it calls.
- [ ] Every command supports `--json`; JSON output goes to stdout only, errors go to stderr as JSON with the API error shape when `--json` is set.
- [ ] Exit codes: 0 ok, 1 error, 2 usage, 3 auth, 4 not found, 5 needs confirmation, documented in `mailgoose help exit-codes`; a test covers each.
- [ ] With no TTY on stdin the CLI never prompts; a command that would prompt exits 2 naming the flag to use. A test runs every command with stdin closed and a 5-second timeout.
- [ ] `--api-url` and `MAILGOOSE_API_URL` set the API base URL; `MAILGOOSE_API_KEY` overrides any stored login.
- [ ] `mailgoose version` prints version, commit and build date.

### MG-04: CLI parity check

Requirements: CLI-1.

Acceptance criteria:

- [ ] A CI step compares the operations in `openapi.yaml` with the CLI command registry and fails, naming each operation, when one has no command.
- [ ] Operations that are web-only by design (session sign-in, Stripe webhook, public form submit, unsubscribe, tracking) are listed in one allowlist file with a reason per entry.
- [ ] Removing a command in a test branch makes the check fail.

### MG-05: Local dev environment

Requirements: OPS-9.

Acceptance criteria:

- [ ] `make dev` (or one documented command) starts Postgres, runs migrations and starts the API and worker locally.
- [ ] Outgoing mail goes to a local capture (a mail sink with a web inbox, or the fake sender from MG-06) and never to SES.
- [ ] The README's local setup section takes a new machine from clone to a running API in the documented steps.

### MG-06: Mail sender interface

Requirements: groundwork for CAM-7, AUTH-1, FRM-3.

Acceptance criteria:

- [ ] One Go interface sends a fully built message (From, To, headers, HTML, text) and returns the provider message ID.
- [ ] An SES v2 adapter uses the environment's configuration set and tags each message with account and campaign IDs; unit-tested with a mocked SES client.
- [ ] A fake adapter records messages in memory or to the local capture for tests and local dev.
- [ ] The adapter is chosen by config; tests never call AWS.

## M1 Accounts

### MG-07: Sign up with an emailed code

Requirements: AUTH-1, CMP-10 (acceptance checkbox).

Acceptance criteria:

- [ ] `POST /v1/signup` takes email, account name and `accepted_terms: true`; without terms acceptance it returns 422.
- [ ] It creates an inactive account and user and emails a 6-digit code from mailgoose's own sending address; the code is stored hashed.
- [ ] Using the code activates the account and starts a session; signing up again with an active address returns a generic "check your email" response without revealing that the account exists.
- [ ] New accounts start on the free plan.

### MG-08: Web sign-in and sessions

Requirements: AUTH-2.

Acceptance criteria:

- [ ] Requesting a code emails a 6-digit code; codes expire after 10 minutes and work once (tests for expired and reused codes).
- [ ] Code requests are rate limited per address and per IP; verify attempts lock after 5 wrong tries per code.
- [ ] A verified code sets an HttpOnly, Secure, SameSite=Lax session cookie valid 30 days; sessions are stored server-side.
- [ ] "Sign out everywhere" revokes all sessions for the user.
- [ ] Cookie-authenticated mutating requests require a CSRF token or same-origin check.

### MG-09: API keys, scopes and bearer auth

Requirements: AUTH-4, API-2.

Acceptance criteria:

- [ ] Create, list and revoke keys via API and `mailgoose keys create|list|revoke`; the secret (`mg_live_...` or `mg_test_...`) is shown once and stored hashed.
- [ ] Scopes `full` and `draft`; a draft key gets 403 `insufficient_scope` on launch, billing changes and key management, tested per endpoint.
- [ ] Bearer auth accepts API keys and CLI tokens; a revoked key fails on the next request.
- [ ] Keys show name, scope, created and last-used time.

### MG-10: CLI device login

Requirements: AUTH-3.

Acceptance criteria:

- [ ] `mailgoose login` prints a short user code and a URL, polls, and on approval stores a token in the OS keychain, falling back to a config file with 0600 permissions.
- [ ] The approval page requires a signed-in web session and shows the code to confirm.
- [ ] Device codes expire after 10 minutes; denied or expired requests exit 3 with a clear message.
- [ ] `mailgoose logout` revokes and removes the token; `mailgoose whoami` prints account, user and auth method.
- [ ] `mailgoose login` with no TTY exits 2 and tells the agent to use `MAILGOOSE_API_KEY`.

### MG-11: Account settings

Requirements: AUTH-5.

Acceptance criteria:

- [ ] Get and update account name, default From name and address, postal address and time zone via API and `mailgoose account get|update`.
- [ ] Postal address is validated as non-empty multi-line text; a missing postal address is reported by later checks (MG-26) and blocks launch (MG-30).
- [ ] Time zone accepts IANA names only.

### MG-12: Credit ledger and free monthly grant

Requirements: BIL-1, BIL-2, BIL-6 (balance and history).

Acceptance criteria:

- [ ] Ledger entries with types grant, purchase, reserve, release, spend, fee, expiry; balance is the sum, computed in one query, and never stored separately.
- [ ] Debits take a row lock so concurrent launches can't overdraw; a concurrency test with 20 parallel reservations never goes below zero.
- [ ] Free accounts get 1,000 credits on signup and on each monthly anniversary; unused free credits expire at the next grant.
- [ ] `GET /v1/credits` and `mailgoose credits` show balance and paginated history.
- [ ] Accounts have a plan field: free or paid.

## M2 Domains

### MG-13: Sending domains

Requirements: DOM-1, DOM-2, DOM-3, DOM-4.

Acceptance criteria:

- [ ] `mailgoose domain add example.com` creates an SES identity with Easy DKIM and a MAIL FROM subdomain, tagged with the environment and account, and returns the DNS records to add (3 DKIM CNAMEs, MAIL FROM MX and SPF TXT, a suggested DMARC TXT when the domain has none).
- [ ] `mailgoose domain verify` and `POST /v1/domains/{id}/verify` look up each record and report found, missing or wrong per record.
- [ ] A worker re-checks pending domains every 5 minutes for 72 hours, then marks them failed.
- [ ] `mailgoose domain list|get|delete`; deleting removes the SES identity.
- [ ] A helper used by test send and launch rejects any From address whose domain isn't verified for that account.
- [ ] Tests use a fake DNS resolver and the mocked SES client.

## M3 Contacts

### MG-14: Contacts, statuses and suppression list

Requirements: CON-1, CON-2, CON-3, CON-8.

Acceptance criteria:

- [ ] Contacts table with email (lowercased, unique per account), first and last name, status, source and timestamps.
- [ ] Create, get, update, delete and list via API and `mailgoose contacts ...`, with filters for email, name, status and tag, and cursor pagination.
- [ ] Deleting a contact leaves a hashed entry in an account suppression list if the contact was unsubscribed, bounced or complained; re-adding that address keeps the suppressed status.
- [ ] Only `subscribed` contacts are selectable as campaign recipients (helper with tests).

### MG-15: Tags

Requirements: CON-4.

Acceptance criteria:

- [ ] Create, rename, delete and list tags via API and `mailgoose tags ...`.
- [ ] Add and remove tags on one contact or in bulk (by contact IDs, or by filter), via API and CLI.
- [ ] Deleting a tag removes it from contacts and from saved campaign recipient lists, with a count in the response.

### MG-16: Contact limits

Requirements: CON-9.

Acceptance criteria:

- [ ] Counted contacts are unconfirmed plus subscribed.
- [ ] A free account can't exceed 500 counted contacts through create, import, form signup or status change; the API returns 402 `contact_limit` with the limit and count. One test per route.
- [ ] Paid accounts have no hard cap; the count feeds the monthly fee (MG-40).

### MG-17: CSV import

Requirements: CON-5, CON-6.

Acceptance criteria:

- [ ] Upload a CSV via API and `mailgoose import contacts.csv --map email=Email --tag x --permission-confirmed`; column mapping is explicit or auto-detected for common headers.
- [ ] Imports run as background jobs; `mailgoose import status <id>` shows progress and the result.
- [ ] The result lists every skipped row with its reason (invalid email, duplicate, suppressed, over limit).
- [ ] Without the permission confirmation, imported contacts are `unconfirmed`.
- [ ] A first import over 2,000 rows, or one that would exceed the contact limit, is held for review; held contacts can't be mailed until an admin approves (admin command in MG-34).
- [ ] A 10,000-row file with 3% bad rows finishes in under a minute in CI's integration test.

### MG-18: CSV export and data requests

Requirements: CON-7, CMP-9.

Acceptance criteria:

- [ ] Export all contacts or one tag to CSV via API and `mailgoose export`, including status, tags, source and consent fields.
- [ ] Re-importing an export reproduces the same contacts and tags (round-trip test).
- [ ] `mailgoose contacts export-data <email>` returns everything stored about one contact as JSON, and `contacts delete --erase` removes it, leaving only the hashed suppression entry.

## M4 Designer backend

### MG-19: Email documents

Requirements: DES-1, DES-2 (schema), DES-4 (whole document).

Acceptance criteria:

- [ ] A versioned JSON schema for block documents covers settings and every block type in DES-2, is part of `openapi.yaml`, and rejects unknown block types and fields.
- [ ] Emails have kind `blocks` or `html`, a name, settings and content; create, get, list, update, duplicate and delete via API and `mailgoose email ...`.
- [ ] `mailgoose email get --json` and `email replace <id> --file doc.json` round-trip a document unchanged.
- [ ] Every block gets a stable ID on creation that never changes on edits or moves.
- [ ] Updates use optimistic concurrency (a version number); a stale update returns 409.

### MG-20: Block operations

Requirements: DES-4.

Acceptance criteria:

- [ ] API and CLI to list, add (at an index or after a block ID), update fields of, move and remove blocks, including blocks inside two-column rows.
- [ ] `mailgoose email blocks add em_x --type heading --text "Hi"` works with flags; `--data '{...}'` accepts any block's fields as JSON.
- [ ] An integration test builds a four-block email from empty with only CLI commands and checks the resulting document.
- [ ] The locked footer block can't be removed or moved off the end.

### MG-21: Renderer

Requirements: DES-7, DES-8, CMP-2, part of CMP-1.

Acceptance criteria:

- [ ] Renders a block document to table-based HTML with inlined CSS and to a plain-text part.
- [ ] Merge tags `{{first_name|fallback}}`, `{{last_name}}`, `{{email}}` and `{{unsubscribe_url}}` are replaced per recipient; unknown tags are reported as errors by a lint function.
- [ ] The footer always contains the account postal address and the unsubscribe link.
- [ ] Golden-file tests for every block type, at desktop and mobile widths.
- [ ] Rendering a 20-block email takes under 20 ms (benchmark in the PR).

### MG-22: HTML import and validation

Requirements: DES-9, DES-11.

Acceptance criteria:

- [ ] `mailgoose email import-html em_x ./draft.html` and the API accept HTML for an email of kind `html`, replacing it whole.
- [ ] Errors, each with a line number: malformed markup, `<script>`, `<form>`, `<iframe>`, external stylesheets, inline event handlers, `javascript:` URLs, missing `{{unsubscribe_url}}`, size over 102 KB.
- [ ] Warnings for CSS major clients drop (a fixed list in the code, documented).
- [ ] The postal address footer is appended when missing.
- [ ] A test corpus of good and bad files covers every rule.

### MG-23: Image storage bucket and CDN

Requirements: DES-5. **Owner merges** (infra), **needs owner** (apply).

Acceptance criteria:

- [ ] Terraform adds a private S3 bucket per environment for uploaded images, served through CloudFront with origin access control, and grants the API task role put access to it.
- [ ] `terraform validate` and `fmt -check` pass for both environments.
- [ ] The PR says the exact `terraform apply` commands for the owner's local Claude Code.

### MG-24: Image upload

Requirements: DES-5.

Acceptance criteria:

- [ ] Upload via API and `mailgoose image upload ./pic.png` returns a CDN URL usable in image blocks.
- [ ] JPEG, PNG, GIF and WebP only, checked by content, not extension; files over 5 MB are refused.
- [ ] Uploads are stored under an account prefix with a random name.
- [ ] Local dev and tests use a local or fake store.

### MG-25: Preview

Requirements: DES-6.

Acceptance criteria:

- [ ] `GET /v1/emails/{id}/preview?width=desktop|mobile` returns rendered HTML with sample merge data, for both email kinds.
- [ ] `mailgoose email preview em_x --out file.html` writes it; `--open` opens a browser when there's a TTY.

### MG-26: Email check

Requirements: DES-12.

Acceptance criteria:

- [ ] `POST /v1/emails/{id}/check` and `mailgoose email check` return errors and warnings with codes: missing subject, missing unsubscribe, missing postal address, broken or non-HTTPS links, images without alt text, unknown merge tags, unverified From, size.
- [ ] Link checking does a HEAD request with a short timeout and a total time cap, and doesn't follow links to private IP ranges.
- [ ] Exit code is 1 when there are errors and 0 with only warnings.

## M5 Sending

### MG-27: Campaigns

Requirements: CAM-1, CAM-9.

Acceptance criteria:

- [ ] Create, get, list, update and delete campaigns via API and `mailgoose campaign ...`: name, email ID, recipients (`all` or a list of tags, matched as any), and send time (now or a timestamp).
- [ ] Recipient count is computed on request and in `campaign get`.
- [ ] Statuses draft, scheduled, sending, sent, cancelled, paused, failed, with allowed transitions enforced and tested.
- [ ] Only draft and scheduled campaigns can be edited.

### MG-28: Unsubscribe

Requirements: CMP-1.

Acceptance criteria:

- [ ] Each recipient gets a signed unsubscribe token; `{{unsubscribe_url}}` points to a hosted page that unsubscribes with one click and shows a confirmation, with no sign-in.
- [ ] A `POST` endpoint implements RFC 8058 (`List-Unsubscribe=One-Click`), and the renderer can emit `List-Unsubscribe` and `List-Unsubscribe-Post` headers.
- [ ] Unsubscribing takes effect immediately, is recorded with time and campaign, and is idempotent.
- [ ] Tampered or expired-format tokens get a friendly error page and change nothing.

### MG-29: Test send

Requirements: CAM-2.

Acceptance criteria:

- [ ] `POST /v1/campaigns/{id}/test` and `mailgoose campaign test cmp_x` send the rendered email to the account owner, or to up to 5 addresses on a verified domain of the account.
- [ ] The subject is prefixed `[Test]`; tests don't use credits; more than 50 a day returns 429.
- [ ] The campaign records when the last test went out and which email version it used.
- [ ] Sending from an unverified From fails with the fix in the hint.

### MG-30: Launch

Requirements: CAM-3, CAM-4, CAM-5, CAM-8, DOM-3, AUTH-4 (draft scope).

Acceptance criteria:

- [ ] `POST /v1/campaigns/{id}/launch` without a code returns 409 `confirmation_required` with a summary (subject, From, recipient count, send time, credit cost) and a one-time code; the CLI prints the summary and exits 5.
- [ ] `mailgoose campaign launch cmp_x --confirm <code>` launches; codes last 15 minutes, work once, and are invalid if the campaign or its email changed after they were issued.
- [ ] Launch is refused with a clear code when: no test send since the last edit, check errors exist, From is unverified, postal address missing, account paused, or credits short (the error says how many).
- [ ] Launch copies the rendered email into the campaign, reserves one credit per recipient, and enqueues the send in one transaction.
- [ ] Launching twice, or retrying with the same `Idempotency-Key`, is a no-op returning the same result.
- [ ] Draft-scoped keys get 403.
- [ ] `mailgoose docs` (MG-55) and `campaign launch --help` describe the agent flow: show the summary to the person, relaunch with the code only after they say yes.

### MG-31: Send pipeline

Requirements: CAM-5 (release), CAM-7, CAM-8, CMP-7 (enforcement hook).

Acceptance criteria:

- [ ] The launch job splits recipients into batches on the SQS send queue; workers render per recipient, add List-Unsubscribe headers and send through the sender interface.
- [ ] Each recipient has a send record with a unique key (campaign, contact); a worker claims it before sending and marks it after, so a retried message never sends twice. A test kills a worker mid-batch and checks for no duplicates.
- [ ] Recipients who are no longer subscribed at send time are skipped and their reserved credit released.
- [ ] Sending rate respects a per-account daily limit and a global per-second limit from config.
- [ ] A campaign's status moves to sent when all records are done; failures after retries land in the DLQ and mark records failed with the credit released.
- [ ] A load test in the PR shows 10,000 recipients processed in under an hour against the fake sender at the configured rate.

### MG-32: Scheduled campaigns

Requirements: CAM-6.

Acceptance criteria:

- [ ] A scheduler in the worker starts due scheduled campaigns within a minute of their time, at most once even with several workers running.
- [ ] `mailgoose campaign cancel` and `campaign reschedule --at ...` work until sending starts; cancelling releases reserved credits.
- [ ] Scheduled times are stored in UTC and shown in the account time zone.

### MG-33: SES events

Requirements: CMP-4, CMP-5, CMP-1 (feedback), REP data.

Acceptance criteria:

- [ ] A worker drains the `ses-events` SQS queue (SES to SNS to SQS) and stores delivery, bounce, complaint, reject and delivery-delay events against the send record.
- [ ] SES open and click events are not used for reports (opens and clicks come only from MG-37); if any arrive they are acknowledged and dropped.
- [ ] A hard bounce or a complaint sets the contact's status within a minute; three consecutive soft bounces suppress the contact.
- [ ] Malformed or unknown events are logged and acknowledged, not retried forever.
- [ ] Tests use recorded sample SES event payloads.

### MG-34: Send limits, account health, kill switch, admin CLI

Requirements: CMP-6, CMP-7, CMP-8, OPS-6, CON-6 (approval).

Acceptance criteria:

- [ ] New accounts can send to at most 1,000 recipients a day for their first 7 days, then the limit grows on a documented schedule while bounce and complaint rates stay under thresholds.
- [ ] After each campaign of 500 or more, the account is paused automatically if complaints exceed 0.3% or hard bounces exceed 5%, and an alert goes to the ops address.
- [ ] Admin commands (`mailgoose admin ...`, only for users with the admin role): find account, pause and unpause sending, stop a campaign in flight, approve a held import, grant credits. Every admin action is logged with who and why.
- [ ] Pausing stops in-flight campaigns before their next batch; a test covers it.

## M6 Forms

### MG-35: Forms and embed code

Requirements: FRM-1, FRM-2.

Acceptance criteria:

- [ ] Create, get, list, update and delete forms via API and `mailgoose form ...`: fields, tags, button text, success message or redirect URL, double opt-in (default on), confirmation email subject and text.
- [ ] `mailgoose form embed frm_x` prints the embed code: a plain HTML form that posts to the public endpoint and works without JavaScript, plus an optional small script for inline success.
- [ ] The embed includes the honeypot field and a signed timestamp.

### MG-36: Form submissions and double opt-in

Requirements: FRM-3, FRM-4, FRM-5, FRM-6, CMP-3.

Acceptance criteria:

- [ ] The public endpoint accepts posts from any origin, validates the email, and redirects or returns JSON depending on the request.
- [ ] Honeypot filled, submitted under 2 seconds after load, or over the per-IP and per-form rate limits means the submission is silently dropped or throttled; a test shows 100 posts a minute from one IP throttled.
- [ ] With double opt-in, a new contact is unconfirmed and gets a confirmation email from the account's verified domain, free of credits; the confirm link moves them to subscribed.
- [ ] A consent record stores time, IP, form ID, the form's wording, and confirmation time.
- [ ] Existing subscribed contacts get the form's tags with no second email; unsubscribed contacts can come back only through confirmation.
- [ ] Forms show signup and confirmation counts in `form get`.
- [ ] Contact limits (MG-16) apply.

## M7 Reports

### MG-37: Open and click tracking

Requirements: REP-3. Opens and clicks come from our own pixel and redirects
only, so the per-campaign off switch really turns tracking off. SES's own
tracking is switched off for the configuration set; that Terraform change is a
separate small PR that the owner merges and applies.

Acceptance criteria:

- [ ] When tracking is on (the default), the renderer adds an open pixel and rewrites links to signed redirect URLs; a campaign option turns both off.
- [ ] The redirect endpoint records the click and redirects in under 200 ms at p95 in a benchmark; tampered URLs get an error page, never an open redirect.
- [ ] Unsubscribe links are never rewritten.
- [ ] A test renders a campaign with tracking off and finds no pixel and no rewritten links.
- [ ] A separate PR removes `OPEN` and `CLICK` from `event_types` in `infra/modules/email` (owner merges and applies); the main PR doesn't touch `infra/`.

### MG-38: Campaign reports

Requirements: REP-1, REP-2, REP-4, REP-5.

Acceptance criteria:

- [ ] `GET /v1/campaigns/{id}/report` and `mailgoose campaign report` give recipients, delivered, bounced, unique opens (flagged as estimated), unique and total clicks, unsubscribes, complaints, credits used, and clicks per link.
- [ ] Numbers update within 5 minutes of events; the API, CLI and (later) UI read the same query.
- [ ] Raw events older than 12 months are deleted by a scheduled job.

## M8 Billing

### MG-39: Buy credits with Stripe Checkout

Requirements: BIL-3, BIL-5, BIL-7, BIL-6 (purchase link). **Needs owner:**
Stripe account, products and test keys in the app secret.

Acceptance criteria:

- [ ] `POST /v1/credits/checkout` with a pack (10k, 50k, 200k, 1M) returns a Stripe Checkout URL; `mailgoose credits buy 10k` prints it.
- [ ] A Stripe webhook (signature verified, idempotent on event ID) adds a purchase ledger entry with a 24-month expiry and marks the account paid.
- [ ] Prices live in config, matching the decided packs ($20, $75, $240, $900).
- [ ] Stripe Tax and receipts are enabled in the Checkout session.
- [ ] Tests use Stripe's test-mode fixtures or a mocked client; nothing in CI calls live Stripe.

### MG-40: Monthly account fee and credit expiry

Requirements: BIL-4, BIL-3 (expiry), BIL-2 (paid accounts stop getting grants).

Acceptance criteria:

- [ ] On each paid account's monthly billing date, a fee entry takes 2,500 credits plus 2,500 per extra 10,000 counted contacts over 2,500, from the oldest unexpired credits first. A partial block rounds up: 2,501 to 12,500 counted contacts is one extra block (default; open in the decisions log).
- [ ] If the balance can't cover the fee, sending pauses with reason `fee_unpaid` and resumes automatically after a purchase covers it.
- [ ] A daily job writes expiry entries for purchased credits past 24 months.
- [ ] Paid accounts stop receiving the free monthly grant.
- [ ] Tests with a fake clock cover fee, insufficient balance, expiry order and resume.

## M9 Web app

Shared criteria for every web app ticket: React and TypeScript, the generated
API client only (no hand-written endpoints), component tests for new pages,
and keyboard-reachable controls with labels.

### MG-41: Web app shell and sign-in pages

Requirements: WEB-1 (shell), AUTH-1, AUTH-2 (pages), AUTH-3 (approval page).

Acceptance criteria:

- [ ] Vite, React and TypeScript app in the repo, built in CI and served by the API at `/` (or as decided in the PR).
- [ ] Sign up, sign in with code, and device-login approval pages work against the API.
- [ ] Signed-in layout with navigation for every MVP page (empty placeholders are fine).
- [ ] Lint, type check and tests run in CI.

### MG-42: Contacts and tags pages

Requirements: CON-1 to CON-8 (UI).

Acceptance criteria:

- [ ] List with search, filters and pagination; add, edit and delete a contact; bulk tag and untag.
- [ ] Import flow with column mapping, permission checkbox, progress and the skipped-rows report; export button.
- [ ] Tag management page.

### MG-43: Domains, API keys and settings pages

Requirements: DOM-1, DOM-2, AUTH-4, AUTH-5, AUTH-6 (UI).

Acceptance criteria:

- [ ] Add a domain, see the DNS records with copy buttons, verify, and see per-record status.
- [ ] Create (secret shown once), list and revoke API keys, with scope choice.
- [ ] Edit account settings; delete account with a typed confirmation.

### MG-44: Designer canvas and drag and drop

Requirements: DES-3.

Acceptance criteria:

- [ ] A block palette and a canvas rendering the document; drag to add and reorder, plus duplicate and delete, including inside two-column rows.
- [ ] Every change goes through the block operations API (MG-20); a change made via the CLI appears on reload.
- [ ] The footer block is locked.

### MG-45: Designer properties, text editing, preview and autosave

Requirements: DES-3, DES-6, DES-7.

Acceptance criteria:

- [ ] A properties panel for the selected block and for email settings; inline rich text for text blocks (bold, italic, links, lists); image picker using upload (MG-24).
- [ ] Merge tag insertion menu.
- [ ] Desktop and mobile preview toggle using the server preview (MG-25).
- [ ] Autosave with a saved or saving indicator; a 409 conflict reloads with a notice.

### MG-46: HTML code editor with live preview

Requirements: DES-9, DES-10, DES-11.

Acceptance criteria:

- [ ] Paste or upload HTML; a code editor (CodeMirror or Monaco) with the preview updating as the person types (debounced).
- [ ] Validation errors and warnings show inline at their line numbers.
- [ ] Code is the only way to edit; the preview isn't editable.

### MG-47: Campaign pages

Requirements: CAM-1 to CAM-6, DES-12 (UI), REP-3 (toggle).

Acceptance criteria:

- [ ] Create a campaign: pick an email, recipients (all or tags with live count), schedule, tracking toggle.
- [ ] Run the email check; send a test; launch shows the summary with a confirm button and launches through the same API flow.
- [ ] Scheduled campaigns can be cancelled or rescheduled; status shows while sending.

### MG-48: Report page

Requirements: REP-1, REP-2.

Acceptance criteria:

- [ ] The campaign report shows the same numbers as `mailgoose campaign report`, with opens labeled as estimated and clicks per link.
- [ ] Readable on a phone-width screen.

### MG-49: Forms pages

Requirements: FRM-1, FRM-2, FRM-6 (UI).

Acceptance criteria:

- [ ] Create and edit forms with all options, preview the form, copy the embed code.
- [ ] Signup and confirmation counts per form.

### MG-50: Credits and billing page

Requirements: BIL-3, BIL-4, BIL-6 (UI).

Acceptance criteria:

- [ ] Balance, history with entry types, next fee date and amount, and pack purchase buttons that open Checkout.
- [ ] A paused-for-fee banner across the app when sending is paused for an unpaid fee.

### MG-51: Dashboard and getting-started checklist

Requirements: WEB-1.

Acceptance criteria:

- [ ] Dashboard shows the checklist (verify domain, add contacts, build an email, send a test, launch), each item ticked from real account state and linking to its page.
- [ ] Shows the credit balance and the latest campaigns.

## M10 Launch

### MG-52: Deploy pipeline

Requirements: OPS-2, OPS-3. **Owner merges** (workflows). **Needs owner:**
GitHub OIDC is switched off because the AWS project's policy denied creating
the OIDC provider (decisions log, Nov 17). This ticket waits until that is
lifted, or until the owner decides how CI deploys without AWS keys in GitHub.

Acceptance criteria:

- [ ] A GitHub Actions workflow builds images, pushes to ECR and deploys to staging on merge to `main`, using the OIDC deploy role; no AWS keys in GitHub secrets.
- [ ] Prod deploys on a version tag, with a manual approval environment.
- [ ] Migrations run as a one-off task before the new API version takes traffic; a failed migration stops the deploy.

### MG-53: Monitoring and alarms

Requirements: OPS-4. **Owner merges** (infra), **needs owner** (apply,
alert address).

Acceptance criteria:

- [ ] Structured JSON logs with request IDs from API and worker.
- [ ] Error tracking wired in (Sentry or CloudWatch), chosen in the PR as a reversible default.
- [ ] Terraform CloudWatch alarms on send queue age, DLQ depth, SES bounce and complaint rates, API 5xx rate and task health, notifying an SNS topic.

### MG-54: CLI releases

Requirements: CLI-6. **Owner merges** (workflows), **needs owner** (create
the Homebrew tap repo).

Acceptance criteria:

- [ ] GoReleaser builds static binaries for macOS arm64 and amd64, Linux arm64 and amd64, and Windows amd64 on a `cli-v*` tag, with checksums, to GitHub Releases.
- [ ] A Homebrew formula is published to a tap repo, and an install script downloads and verifies the right binary.
- [ ] The CLI prints an update notice at most once a day when a newer release exists, and never in `--json` output.

### MG-55: Agent docs

Requirements: CLI-5, API-6.

Acceptance criteria:

- [ ] Every command's `--help` has at least one example; a test fails if one is missing.
- [ ] `mailgoose docs` prints a Markdown guide for agents: auth with an API key, building an email with blocks, checks, test send, and the launch confirmation flow.
- [ ] API reference docs are generated from `openapi.yaml`, and an `llms.txt` is generated alongside them.

### MG-56: Security scanning in CI

Requirements: OPS-7. **Owner merges** (workflows).

Acceptance criteria:

- [ ] CI runs `govulncheck`, `npm audit` (or equivalent) and a secret scanner on every PR.
- [ ] Dependabot or Renovate is configured for Go, npm and GitHub Actions.

### MG-57: Policy pages

Requirements: CMP-10. **Needs owner:** the legal text.

Acceptance criteria:

- [ ] Terms, privacy policy, anti-spam policy and DPA pages exist with clearly marked placeholder text, and signup links to them.
- [ ] The PR lists what the owner needs to supply or approve.

### MG-58: End-to-end tests

Requirements: MVP done items 1 to 3, CLI acceptance.

Acceptance criteria:

- [ ] A CLI-only script goes from API key to a delivered campaign against a local or staging stack with the fake sender, with no prompts, and checks the unsubscribe and a simulated bounce.
- [ ] A browser test (Playwright) goes from signup to a launched campaign in the web app.
- [ ] Both run in CI against the local stack; the PR notes the timing of each flow.

### MG-59: Accessibility and browser pass

Requirements: WEB-2, WEB-3.

Acceptance criteria:

- [ ] Automated accessibility checks (axe) pass on every main page.
- [ ] Main flows checked in current Chrome, Safari, Firefox and Edge; findings fixed or filed as tickets.

### MG-60: Launch checklist

Requirements: MVP done item 4, OPS-5, OPS-8, DES-8 acceptance. **Needs
owner** throughout.

Acceptance criteria:

- [ ] Prod DNS decided (open in the decisions log: move mailgoose.app to Route 53, or delegate `api`, `app` and `mail`).
- [ ] Staging and prod applied and healthy; prod domain and mailgoose's own sending domain verified.
- [ ] SES production access granted for the account.
- [ ] Stripe live keys set in the prod app secret; one live purchase refunded end to end.
- [ ] A database restore from snapshot tested on staging.
- [ ] Starter blocks checked by hand or with a rendering service in Gmail, Apple Mail, Outlook and Yahoo.
- [ ] Policy text final (MG-57).
