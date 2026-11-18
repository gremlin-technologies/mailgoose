# Decisions log

Newest first. Each entry says who decided and whether it's confirmed. Anything
marked **recommended** is a proposal Carlton hasn't confirmed yet; treat it as
the default until he does, but don't build anything hard to undo on it.

## 2026-11-17: Domain, DNS, and first apply

Decided by Carlton.

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
- **Recommended, not yet confirmed:**
  - Postgres on RDS
  - ECS Fargate for the API and workers
  - SQS for the send queue (River on Postgres is the alternative)
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

- Dev plan and ticketing come after the PRD, not yet.
- Cloud Claude writes code on branches and opens PRs; Carlton's local Claude
  Code does anything needing his AWS sign-in; Carlton merges. See `CLAUDE.md`.

## Open questions

- Can people click into the HTML preview to edit text, or is the code the only
  way in?
- Is the one-time-code check (see PRD, CLI requirements) the right way to
  enforce agent launch confirmation?
- SQS or River on Postgres for the send queue?
- One audience with tags, or separate lists per account?
- Ship an MCP server alongside the CLI, or CLI only?
- Anything in the earlier mailgoose repo worth reusing?
