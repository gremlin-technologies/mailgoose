# mailgoose

A deliberately small email marketing SaaS: design an email, send it to a list,
collect subscribers with signup forms. Nothing else. It ships with a CLI at full
parity with the web app, so customers can drive it from whichever AI agent they
already use.

## Where context lives

- `docs/prd.md`: product requirements (scope, CLI, deliverability, risks).
- `docs/pricing.md`: pricing research and the chosen plan.
- `docs/mvp-requirements.md`: what the MVP must do, with requirement IDs
  (AUTH-1, CAM-4 ...) and acceptance criteria. Tickets point back to these.
- `docs/mvp-tickets.md`: the proposed MVP ticket breakdown, in order.
- `docs/decisions.md`: dated log of decisions and open questions. **Read this
  first; it wins over anything older.**

The PRD, pricing and MVP requirements files are snapshots of Claude Docs; the
Claude Doc is the live version. In the PRD and pricing files, lines marked
"(inferred)" or "recommended" are proposals, not decisions. The MVP
requirements were signed off on Nov 18, so their "(inferred)" lines are agreed
scope; they only show where an idea came from.

## Decided so far (summary; details in docs/decisions.md)

- Fresh start. The earlier mailgoose repo is not binding.
- Web app and full CLI both ship at launch; every API endpoint has a CLI command.
- v1 is five things: designer, contacts with basic tags, campaign sending,
  signup forms, basic reports. Everything else is out of scope at first, except
  maybe very simple automations later.
- Designer: drag-and-drop blocks, plus bring-your-own HTML (parsed and validated,
  edited in a code editor with a live preview). Blocks are creatable and editable
  from the CLI so LLMs can build emails.
- Signup forms: generated embed code, double opt-in on by default.
- Sending: test send, then an explicit confirm to launch, in UI and CLI. An agent
  may launch only after a quick confirmation from the person in its chat window.
- Pricing: free tier 500 contacts and 1,000 credits/month; paid accounts $5/month
  plus $20 per 10,000 credits; credits last 24 months; the $5 fee comes out of
  credits by default, yearly card billing as the alternative.
- Stack: Go for backend, workers and CLI; TypeScript and React for the web app
  and editor; AWS from day one.
- AWS Region: us-east-2 for everything Regional (the project allows no other).
- AWS layout: one account shared by staging and prod (kept apart by names, state
  keys and `Environment` tags). No production database standby until there are
  paying customers.
- Confirmed Nov 18: RDS Postgres, ECS Fargate, SQS for the send queue, SES with
  SNS events, Terraform, one REST API with an OpenAPI spec shared by web app, CLI
  and agents, designs stored as JSON blocks rendered server-side.
- MVP (Nov 18): billing ships (Stripe credit packs, $5 fee from credits; yearly
  card billing later); web sign-in by emailed one-time codes; one person per
  account; HTML emails edited as code only; agent launch confirmed with a
  one-time code; one audience per account with tags; CLI only, no MCP server;
  open and click tracking on by default with a per-campaign switch.

## Working rules

- **Two Claudes, one repo.** The cloud Claude (Carlton's Claude Duet "mailgoose"
  channel) plans, writes the PRD and docs, and writes code on branches with pull
  requests. Claude Code on Carlton's machine handles anything that needs his AWS
  sign-in: `terraform apply`, SES setup, checking real infrastructure. It also
  runs and debugs the app locally.
- **Branches and PRs.** Work on a branch and open a pull request into `main`.
  Never push to `main`, and never merge your own PR.
- **Reviewer merges (since Nov 18).** A separate reviewer agent reviews each PR
  and merges it only when all of these hold: CI is green; the PR meets the
  acceptance criteria of its ticket (or, for a PR with no ticket, does what its
  description says); it follows the rules in this file. Anything else goes back
  to the author with review comments. PRs that touch `infra/` or
  `.github/workflows/` are reviewed the same way but left for Carlton to merge
  (his call, Nov 18).
- **AWS keys stay local.** No agent in the cloud holds AWS credentials. Later,
  deploys run from GitHub Actions through the OIDC deploy role, so no agent
  holds AWS keys at all.
- **No secrets in the repo.** No keys, tokens, or `.tfvars` with secrets.
- **Keep decisions current.** When a decision is made or changed, add a dated
  entry to `docs/decisions.md` in the same PR, and update the summary above if
  it changes.
- **Don't decide product questions silently.** If work hits an open question in
  `docs/decisions.md`, pick a reversible default, say which in the PR, and leave
  the question open for Carlton.
