# Decisions log

Newest first. Each entry says who decided and whether it's confirmed. Anything
marked **recommended** is a proposal Carlton hasn't confirmed yet; treat it as
the default until he does, but don't build anything hard to undo on it.

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
- **Recommended, not yet confirmed:**
  - Postgres on RDS
  - ECS Fargate for the API and workers
  - SQS for the send queue (River on Postgres is the alternative)
  - SES for sending, with bounce and complaint events through SNS
  - Terraform for infrastructure
  - One REST API with an OpenAPI spec, shared by the editor, CLI and agents
  - Designs stored as JSON blocks, rendered server-side to email-safe HTML

### Process

- Dev plan and ticketing come after the PRD, not yet.
- Cloud Claude writes code on branches and opens PRs; Carlton's local Claude
  Code does anything needing his AWS sign-in; Carlton merges. See `CLAUDE.md`.

## Open questions

- Can people click into the HTML preview to edit text, or is the code the only
  way in?
- Is the one-time-code check (see PRD, CLI requirements) the right way to
  enforce agent launch confirmation?
- One AWS account, or separate accounts per environment?
- Standby (Multi-AZ) for the production database?
- AWS region: Carlton's AWS profile uses us-east-2; the Terraform sketch in PR #1
  defaults to us-east-1.
- SQS or River on Postgres for the send queue?
- One audience with tags, or separate lists per account?
- Ship an MCP server alongside the CLI, or CLI only?
- Anything in the earlier mailgoose repo worth reusing?
