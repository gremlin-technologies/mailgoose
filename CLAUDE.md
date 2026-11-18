# mailgoose

A deliberately small email marketing SaaS: design an email, send it to a list,
collect subscribers with signup forms. Nothing else. It ships with a CLI at full
parity with the web app, so customers can drive it from whichever AI agent they
already use.

## Where context lives

- `docs/prd.md`: product requirements (scope, CLI, deliverability, risks).
- `docs/pricing.md`: pricing research and the chosen plan.
- `docs/decisions.md`: dated log of decisions and open questions. **Read this
  first; it wins over anything older.**

The PRD and pricing files are snapshots of Claude Docs; the Claude Doc is the
live version. Lines marked "(inferred)" or "recommended" in them are proposals,
not decisions.

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
- Recommended, not yet confirmed: RDS Postgres, ECS Fargate, SQS, SES with SNS
  events, Terraform, one REST API with an OpenAPI spec shared by editor, CLI and
  agents, designs stored as JSON blocks rendered server-side.

## Working rules

- **Two Claudes, one repo.** The cloud Claude (Carlton's Claude Duet "mailgoose"
  channel) plans, writes the PRD and docs, and writes code on branches with pull
  requests. Claude Code on Carlton's machine handles anything that needs his AWS
  sign-in: `terraform apply`, SES setup, checking real infrastructure. It also
  runs and debugs the app locally.
- **Branches and PRs.** Work on a branch and open a pull request into `main`.
  Never push to `main`. Carlton reviews and merges.
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
