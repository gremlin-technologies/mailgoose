# Decisions log

Newest first. Each entry says who decided and whether it's confirmed. Anything
marked **recommended** is a proposal Carlton hasn't confirmed yet; treat it as
the default until he does, but don't build anything hard to undo on it.

## 2026-11-18: Reviewer agent merges PRs

Decided by Carlton: "i want this to be as autonomous as possible. delegate a
reviewer agent and if it passes their review, allow them to merge it."

- A separate reviewer agent reviews every PR. It merges when CI is green, the PR
  meets its ticket's acceptance criteria, and it follows `CLAUDE.md`. Otherwise
  it sends the PR back with comments. The author never merges its own PR.
- PRs touching `infra/` (Terraform) or `.github/workflows/` (CI and deploy
  credentials) wait for Carlton to merge. Carlton: "Keep those for me". The
  reviewer still reviews them.

## 2026-11-18: MVP decisions

Decided by Carlton ("All defaults" to the ten proposals). Full requirements, with
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

## 2026-11-17: AWS Region

Decided by Carlton.

- **Region: us-east-2.** Carlton's AWS project is assigned us-east-2, and the
  project allows Regional resources only there. All Terraform (state bucket,
  backends, provider defaults) now uses us-east-2. Global services such as
  CloudFront, and WAF or CloudWatch Logs tied to them, may use us-east-1 when
  required. Cross-Region features (replication, multi-Region keys, backups to a
  second Region, Route 53 latency/geo/failover routing) are not available.

## 2026-11-17

Decided by Carlton in the PRD scoping call and follow-ups.

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
- Cloud Claude writes code on branches and opens PRs; Carlton's local Claude
  Code does anything needing his AWS sign-in; Carlton merges. See `CLAUDE.md`.
  (Merging changed 2026-11-18: a reviewer agent merges.)

## Open questions

- Anything in the earlier mailgoose repo worth reusing? (Default: no, fresh
  start.)
