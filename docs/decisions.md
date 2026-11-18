# Decisions log

Newest first. Each entry says who decided and whether it's confirmed. Anything
marked **recommended** is a proposal the owner hasn't confirmed yet; treat it as
the default until the owner does, but don't build anything hard to undo on it.

## 2026-11-18: API key details (MG-09, reversible defaults)

Picked while building MG-09; easy to change.

- **`mg_live_` means production, `mg_test_` means staging and local.** The
  ticket names both prefixes without saying what "test" means; with no test
  mode in the MVP, the prefix shows which environment a key works in.
  CLI tokens start `mg_cli_`.
- **Secrets are 160 random bits, stored as SHA-256**, not Argon2id: a random
  secret that long can't be brute-forced from its hash, and a fast hash keeps
  every request's lookup cheap.
- **50 active API keys per account.** CLI tokens don't count.
- **`last_used_at` is updated at most once a minute per key**, so a busy key
  isn't a write on every request. Keys list it "to the minute".
- **Revoking a key from the CLI needs `--yes`**, since it can't be undone and
  the CLI never prompts.
- **Idempotency-Keys are scoped to the account and the credential** (API key,
  CLI token or signed-in person), not the account alone, so a draft key can
  never replay a response made for a full key, such as a new key's secret.
- **Rate limits stay per credential** (a hash of the `Authorization` header,
  which is per key without a database lookup); web sessions are limited per
  session. Wrong bearer secrets are limited to 30 a minute per IP, after which
  the API answers 429 without looking them up.
- **A draft key gets 403 `insufficient_scope` on every operation marked
  `x-required-scope: full`** in the spec. Today that's the three key
  operations; launch and billing operations get the mark when they're built.
- **Key management needs a full-scope key or a web session**, so the web app
  can manage keys too.

## 2026-11-18: Web sign-in details (MG-08, reversible defaults)

Picked while building MG-08; easy to change.

- **Code-request limits:** 5 an hour per address and 20 an hour per IP,
  counted together for sign-up and sign-in and kept in Postgres so they hold
  across API tasks. Addresses with no account count too, so the limit can't
  be used to tell which addresses have one.
- **Wrong codes are capped per address across codes:** 10 an hour, counted
  for every address whether or not it has an account or a live code. Over
  the cap, even the right code gets 429 with `Retry-After` until the oldest
  wrong one is an hour old. The per-code lock (5 tries) alone would allow 25
  guesses an hour with 5 fresh codes. (Asked for in review.)
- **Code requests for one address take a Postgres advisory lock**, so two at
  the limit can't both pass. The per-IP count can still be off by a few in a
  burst.
- **Sign-in never says whether an address has an account:** the same 202
  either way, and no email to an unknown address. An account that hasn't
  finished signing up gets its sign-up code.
- **Cross-site protection is a same-origin check, not a token.** Browser
  requests that change something and carry no `Authorization` header must come
  from the API's own origin or one in `WEB_ORIGINS` (Go's
  `http.CrossOriginProtection`, which reads `Sec-Fetch-Site` and `Origin`).
  API keys and non-browser clients aren't affected. The public signup-form
  endpoint (MG-36) will need an exception when it lands, since it takes posts
  from any site.
- **Sign-in and the session endpoints have no CLI command** and are in the
  CLI parity allowlist; the CLI signs in with `mailgoose login` (MG-10).

## 2026-11-18: Sign-up details (MG-07, reversible defaults)

Picked while building MG-07; easy to change, and the owner may want to.

- **Codes come from `signin@` the system sending domain**, named "mailgoose",
  with the code in the subject so it shows in a notification.
- **Signing up with an address that already has an active account** gets the
  same 202 and an email with a sign-in code instead of a new account. A
  pending account that signs up again gets a new code and takes the newer
  account name. One email a minute per address at most.
- **Codes are hashed with Argon2id** (OWASP minimum settings, per-code salt).
  A six-digit code has only a million values, so a plain hash would fall to
  brute force if the table leaked.
- **Sign-up and the web session endpoint have no CLI command** and are in
  `api/cli-parity-allowlist.yaml`: signing up accepts the terms on the web
  page, and the CLI gets access through `mailgoose login` (MG-10). Open for the
  owner: should agents be able to start a sign-up from the CLI?
- **A wrong code locks after 5 tries** already in MG-07, since the endpoint
  exists now; MG-08 adds the per-address and per-IP limits on requesting codes.

## 2026-11-18: OpenAPI pipeline (MG-02, reversible defaults)

Picked while building MG-02; technical and easy to change.

- **Tools:** Redocly CLI lints the spec (`recommended-strict`, so warnings fail
  too; settings in `api/redocly.yaml`), oapi-codegen generates the Go types and
  a `net/http` server interface, and openapi-typescript plus openapi-fetch make
  the TypeScript client. The contract test validates responses with
  libopenapi-validator, which handles OpenAPI 3.1.
- **CI runs the checks as Go tests**, not as new workflow steps, so this PR
  doesn't touch `.github/workflows` and stays within what the reviewer can
  merge. `internal/api/apigen` tests run the lint and the two freshness checks;
  they install the `web/` npm packages with `npm ci` when needed and fail in CI
  if Node is missing. A later workflow PR (the owner merges those) can move
  them to their own steps.
- **The TypeScript client lives in a minimal `web/` package** (package.json,
  the generated `schema.d.ts` and a small `client.ts`). MG-41 builds the web
  app around it.

## 2026-11-18: API conventions (MG-01, reversible defaults)

Picked while building MG-01; technical and easy to change.

- **Rate limits are counted in memory on each API task**, 600 requests a
  minute per API key (or per IP without credentials) by default
  (`RATE_LIMIT_PER_MINUTE`). With N tasks a key can get up to N times that;
  fine while staging and prod run one or two tasks. Move the buckets to
  Postgres if it needs to be exact.
- **Idempotency keys live in Postgres** (`idempotency_keys`), so every task sees
  them. Until API keys exist (MG-09) they are scoped by a hash of the
  `Authorization` header, and requests without credentials skip idempotency;
  MG-09 switches the scope to the account.

## 2026-11-18: No personal names in the repo

Decided by the owner: "as a rule, let's not refer to me ... in the repo. just
gremlin technologies is fine". Docs, code, commit messages, PRs and issues say
"Gremlin Technologies", or "the owner" where a role reads better. Earlier
mentions were replaced.

## 2026-11-18: Open and click tracking source (default)

Picked as a reversible default while writing the tickets, after review of
PR 7. Opens and clicks come only from our own pixel and signed redirects
(MG-37). SES's own open and click tracking is turned off for the configuration
set, because with both on, links would be wrapped twice, opens counted twice,
and the per-campaign off switch wouldn't turn tracking off. SES events are used
for delivery, bounce, complaint, reject and delay only (MG-33).

## 2026-11-18: Reviewer agent merges PRs

Decided by the owner: "i want this to be as autonomous as possible. delegate a
reviewer agent and if it passes their review, allow them to merge it."

- A separate reviewer agent reviews every PR. It merges when CI is green, the PR
  meets its ticket's acceptance criteria, and it follows `CLAUDE.md`. Otherwise
  it sends the PR back with comments. The author never merges its own PR.
- PRs touching `infra/` (Terraform) or `.github/workflows/` (CI and deploy
  credentials) wait for the owner to merge. The owner: "Keep those for me". The
  reviewer still reviews them.

## 2026-11-18: MVP decisions

Decided by the owner ("All defaults" to the ten proposals). Full requirements, with
acceptance criteria, are in `docs/mvp-requirements.md`.

- **Billing ships in the MVP.** Stripe Checkout credit packs, and the $5 account
  fee taken from credits. Yearly card billing comes after the MVP.
- **Web sign-in:** emailed one-time codes, no passwords. The CLI signs in with a
  browser device flow; agents use API keys.
- **One person per account.** No team members in the MVP.
- **HTML emails are edited as code only.** No click-to-edit in the preview.
- **Agent launch confirmation:** `campaign launch` without confirmation returns a
  summary and a one-time code; `campaign launch --confirm <code>` launches after
  the person says yes in the agent's chat.
- **Send queue: SQS** (already in the Terraform), not River on Postgres.
- **One audience per account,** organized with tags. No separate lists.
- **CLI only.** No MCP server in the MVP.
- **Open and click tracking** on by default, with a per-campaign switch off.
- **Tech confirmed:** RDS Postgres, ECS Fargate, SES with SNS events, Terraform,
  one REST API with an OpenAPI spec shared by the web app, CLI and agents, and
  designs stored as JSON blocks rendered server-side.

## 2026-11-17: Domain, DNS, and first apply

Decided by the owner.

- **Domain: mailgoose.app** (registered at GoDaddy).
- **DNS stays at GoDaddy** for the root domain, which carries live Microsoft 365
  email and the website. Only `staging.mailgoose.app` is delegated to Route 53.
- **Staging only for now.** Prod waits until there's an app to run. The staging
  ECS services start at 0 tasks until the first image is in ECR.

Open: how prod gets DNS (move all of mailgoose.app to Route 53, or delegate
`api`, `app` and `mail` individually). See `infra/README.md`, "DNS".

Found during the first apply, not decided:

- **No GitHub OIDC deploys yet.** The AWS project's managed service control
  policy denied `iam:CreateOpenIDConnectProvider` (explicit deny, policy
  p-6tzxgdae), though AWS's published project SCPs don't list it. The OIDC
  provider and the staging deploy role are switched off
  (`create_github_oidc_provider`, `enable_github_deploy`). Reversible default:
  turn them back on once the project allows it (AWS Support, or activating
  advanced features). Open: how CI deploys until then, without storing AWS keys
  in GitHub.
- **AWS plan: Free** ($100 credits, expires 2027-05-18). Everything staging uses
  is on the Free plan's service list.

## 2026-11-17: AWS Region

Decided by the owner.

- **Region: us-east-2.** The Gremlin Technologies AWS project is assigned us-east-2, and the
  project allows Regional resources only there. All Terraform (state bucket,
  backends, provider defaults) now uses us-east-2. Global services such as
  CloudFront, and WAF or CloudWatch Logs tied to them, may use us-east-1 when
  required. Cross-Region features (replication, multi-Region keys, backups to a
  second Region, Route 53 latency/geo/failover routing) are not available.

## 2026-11-17

Decided by the owner in the PRD scoping call and follow-ups.

### Product

- **Fresh start.** An earlier mailgoose repo exists; it is not binding.
- **Surfaces.** Web app and a full CLI, both at launch, at parity.
- **Scope.** Designer, contacts with basic tags, campaign sending, signup forms,
  basic reports. Everything else out of scope at first. Very simple automations
  (such as a welcome email) may come later.
- **Tagging.** Basic tags only.
- **Designer.** Drag-and-drop block editor, plus bring-your-own HTML that gets
  parsed and validated. Imported HTML is edited in a code editor whose preview
  updates live.
- **CLI and blocks.** Designer blocks can be created and edited through the CLI,
  so LLMs can build emails block by block.
- **Signup forms.** We generate embed code; double opt-in on by default.
- **Launching.** Test send and launch from the CLI, with a confirm step. An agent
  may launch a campaign, but only after a quick confirmation from the person in
  the agent's chat window.
- **Reports.** Basic per-campaign numbers only.

### Pricing

- Free tier: 500 contacts and 1,000 credits a month, sending only from a
  verified domain.
- Paid accounts: $5 a month (includes the first 2,500 contacts, then $5 per
  extra 10,000 contacts) plus credits at $20 per 10,000.
- Credits last 24 months from purchase.
- The $5 fee comes out of credits by default; yearly card billing ($60) is the
  alternative.
- The first plan ($15 per 10,000, no account fee) was dropped as not viable once
  fully loaded costs were counted (about $0.70 per 1,000 emails). The chosen plan
  models at about 56% margin at 1,000 paying customers. See `docs/pricing.md`.

### Tech

- **Languages.** Go for backend, workers and CLI. TypeScript and React for the
  web app and editor (chosen over Rust).
- **Hosting.** AWS from day one (chosen over Render).
- **Recommended at the time, confirmed on 2026-11-18** (see that entry):
  - Postgres on RDS
  - ECS Fargate for the API and workers
  - SQS for the send queue
  - SES for sending, with bounce and complaint events through SNS
  - Terraform for infrastructure
  - One REST API with an OpenAPI spec, shared by the editor, CLI and agents
  - Designs stored as JSON blocks, rendered server-side to email-safe HTML

- **One AWS account** for staging and prod. The two are kept apart by name
  prefixes, separate state keys and VPCs, and `Environment` tags that scope each
  environment's SES permissions. Separate accounts can come later if needed.
- **No standby (Multi-AZ) for the production database for now.** Turn it on once
  there are paying customers; it roughly doubles the database cost.

### Process

- Dev plan and ticketing come after the PRD, not yet. (Superseded 2026-11-18:
  MVP tickets are in `docs/mvp-tickets.md`.)
- Cloud Claude writes code on branches and opens PRs; the owner's local Claude
  Code does anything needing the owner's AWS sign-in; the owner merges. See `CLAUDE.md`.
  (Merging changed 2026-11-18: a reviewer agent merges.)

## Open questions

- How does prod get DNS: move mailgoose.app to Route 53, or delegate only
  `api`, `app` and `mail`? (See 2026-11-17. Blocks MG-60.)
- How does CI deploy without AWS keys in GitHub while the AWS project blocks
  the OIDC role? (See 2026-11-17. Blocks MG-52.)
- Monthly fee: does a partial block of 10,000 contacts round up? (Default: yes,
  2,501 to 12,500 counted contacts is one extra block. MG-40.)
- Anything in the earlier mailgoose repo worth reusing? (Default: no, fresh
  start.)
