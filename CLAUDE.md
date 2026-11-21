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
- `docs/api/`: the API reference, `llms.txt` and the agent guide, generated
  by `make docs` from `api/openapi.yaml` and the CLI. Don't edit them by hand.
- `docs/brand.md`: the goose mascot, the UI's direction and its starting color
  palette. UI tickets build their design tokens from it.
- `docs/design-system.md`: how the web app looks (tokens, components,
  interaction rules) and the research behind it. Read it before changing UI.
- `docs/public-site.md`: the public site at mailgoose.app, which lives in its
  own public repository, gremlin-technologies/mailgoose-public (moved Nov 21).
- `docs/releasing.md`: how the CLI is released (GoReleaser, to GitHub Releases
  on mailgoose-public, Homebrew, the AUR and .deb/.rpm).

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
- Import from Mailchimp (Nov 20): customers can bring their audiences, tags and
  email HTML over from Mailchimp, by uploading Mailchimp's export or (later)
  connecting with OAuth, in the web app, API and CLI. Tickets MG-69 to MG-78
  (issues #153 to #162), after the current queue. Its product choices are
  open; see `docs/decisions.md`.
- Designer: drag-and-drop blocks, plus bring-your-own HTML (parsed and validated,
  edited in a code editor with a live preview). Blocks are creatable and editable
  from the CLI so LLMs can build emails.
- Signup forms: generated embed code, double opt-in on by default.
- Sending: test send, then an explicit confirm to launch, in UI and CLI. An agent
  may launch only after a quick confirmation from the person in its chat window.
- Pricing: free tier 500 contacts and 1,000 credits/month; paid plan a card
  subscription, $5/month or $50/year (first 2,500 contacts, then the same again
  per further 10,000), plus $20 per 10,000 credits; credits last 24 months
  (changed Nov 18: the fee no longer comes out of credits).
- Stack: Go for backend, workers and CLI; TypeScript and React for the web app
  and editor; AWS from day one.
- AWS Region: us-east-2 for everything Regional (the project allows no other).
- AWS layout: one account shared by staging and prod (kept apart by names, state
  keys and `Environment` tags). No production database standby until there are
  paying customers. SES ignores tags on sends, so the app checks that each
  message's sending domain belongs to its environment; prod gets its own AWS
  account before the first beta customer (Nov 20).
- Confirmed Nov 18: RDS Postgres, ECS Fargate, SQS for the send queue, SES with
  SNS events, Terraform, one REST API with an OpenAPI spec shared by web app, CLI
  and agents, designs stored as JSON blocks rendered server-side.
- MVP (Nov 18): billing ships (Stripe credit packs, and the paid plan as a card
  subscription); web sign-in by emailed one-time codes (CLI sign-up uses
  the same code, owner decision Nov 18); one person per account; HTML emails
  edited as code only; agent launch confirmed with a one-time code; one
  audience per account with tags; CLI only, no MCP server; open and click
  tracking on by default with a per-campaign switch (our own pixel and
  redirects only; SES tracking off).
- Test mode (Nov 20, MG-80): accounts send in `test` or `full` mode; test
  mode sends only to up to 5 confirmed test addresses plus the owner's own.
  New accounts start in test mode while `NEW_ACCOUNTS_TEST_MODE` is on (the
  default); prod turns it off before launch. Admins switch accounts.
- Brand (Nov 18): the goose is the mascot and the UI palette comes from it;
  modern and sleek, but simple to use. Logos (Nov 21, the owner's):
  `docs/brand/wordmark.png` (head and word; light-text version for dark mode)
  in the app's side menu and the site header, `docs/brand/goose.png` wherever
  the goose stands alone, the head as favicon. See `docs/brand.md`.
- Deploys (Nov 21): every merge to main deploys staging from a CodeBuild
  project in the AWS account (GitHub OIDC is blocked by AWS's plan policy) and
  posts the result to Slack `#deployments`. The repo stays on the personal
  GitHub account gremlin-technologies; its GitHub connection must be made with
  "Install a new app". See `infra/README.md`.
- Tester accounts (Nov 21, MG-86): admins make invited testers tester
  accounts (`mailgoose admin tester grant|revoke`): full sending within 500
  credits for life, 500 contacts, 100 copies a day and a stricter pause,
  for 30 days by default. SES mailbox simulator copies are free for testers
  and left out of every account's rates.
- UI look (Nov 20): white pages, near-black text, the periwinkle blue only for
  actions and state, hairline-and-shadow depth, 120ms transitions, Lucide icons
  in the side menu. See `docs/design-system.md`. Light by default (Nov 21):
  dark mode is a switch in Settings, kept per browser; the public site is
  light only.
- Optional custom domains (Nov 21, MG-85): with `SHARED_SENDING_DOMAIN` set,
  an account without a verified domain sends from `<sending slug>@` that
  shared domain, with replies to an address it verified (the owner's sign-in
  address or a confirmed sender address, up to 10). The app nudges toward
  adding a domain. Off in prod for now; details are reversible defaults.
  MG-98 (Nov 21): there, mail always goes under the account's name (sign-up
  and rename refuse names with `@`, domain-shaped names, and well-known
  brands and payment terms), and an account with no verified domain stays at
  the new-account limit (1,000) until it adds one.
- SES backpressure (Nov 21, MG-87): SES throttling, quota and
  paused-sending errors leave copies queued for a minute instead of failing
  them, and `PLATFORM_DAILY_SEND_LIMIT` (default 45,000) caps campaign
  copies across all accounts under the SES quota.
- Form signup cap (Nov 21, MG-92): each form adds at most
  `FORM_DAILY_SIGNUP_LIMIT` (default 1,000) new contacts in a rolling 24
  hours, counted in Postgres across API tasks; past it the form answers 429
  `form_signup_limit`, and the owner and ops are told once a day per form.
- Block designer (Nov 20): rebuilt full-window, with headings, text and button
  labels typed on the email itself, a floating formatting toolbar, and drags
  that snap to the nearest gap. Text blocks keep the Markdown subset; no API
  changes.
- Public site and CLI releases (Nov 21): mailgoose.app is served from its own
  public repository, gremlin-technologies/mailgoose-public, so this one can go
  private. The CLI ships from here with GoReleaser to GitHub Releases on
  mailgoose-public, a Homebrew tap (gremlin-technologies/homebrew-tap), the
  AUR (`mailgoose-bin`) and .deb/.rpm files. See `docs/releasing.md`.

## Working rules

- **Two Claudes, one repo.** The cloud Claude (the owner's Claude Duet "mailgoose"
  channel) plans, writes the PRD and docs, and writes code on branches with pull
  requests. Claude Code on the owner's machine handles anything that needs the owner's AWS
  sign-in: `terraform apply`, SES setup, checking real infrastructure. It also
  runs and debugs the app locally.
- **Branches and PRs.** Work on a branch and open a pull request into `main`.
  Never push to `main`, and never merge your own PR.
- **Reviewer merges (since Nov 18).** A separate reviewer agent reviews each PR
  and merges it only when all of these hold: CI is green; the PR meets the
  acceptance criteria of its ticket (or, for a PR with no ticket, does what its
  description says); it follows the rules in this file. Anything else goes back
  to the author with review comments. PRs that touch `infra/` or
  `.github/workflows/` are reviewed the same way but left for the owner to merge
  (the owner's call, Nov 18).
- **AWS keys stay local.** No agent in the cloud holds AWS credentials.
  Staging deploys run from CodeBuild inside the AWS account on every merge to
  `main` (GitHub OIDC is blocked by AWS's plan policy), so no agent holds AWS
  keys at all.
- **No secrets in the repo.** No keys, tokens, or `.tfvars` with secrets.
- **No personal names (since Nov 18).** Don't name the owner anywhere in the
  repo: docs, code, commit messages, PRs or issues. Write "Gremlin
  Technologies", or "the owner" where a role reads better.
- **Keep decisions current.** When a decision is made or changed, add a dated
  entry to `docs/decisions.md` in the same PR, and update the summary above if
  it changes.
- **Don't decide product questions silently.** If work hits an open question in
  `docs/decisions.md`, pick a reversible default, say which in the PR, and leave
  the question open for the owner.

## AWS rules (from the AWS Agent Toolkit)

Added by the AWS Agent Toolkit setup on the owner's machine. They apply to any
agent working on AWS here; the AWS MCP tools they mention (`aws___...`) exist
only in the local Claude Code. Where they conflict with the rules above, the
rules above win. Re-running the toolkit setup replaces only the text between
the markers.

<!-- BEGIN AWS Agent Toolkit rules -->
# AWS Guidance for the new AWS experience

This user has signed up for the new AWS experience. This experience lets you sign into AWS using a social provider and requires the following additional context.

Where this guidance conflicts with the project's own instructions, the project's instructions take precedence.

## Context

### Terminology:

- Say "project" instead of "account" — a project contains an AWS account and settings for sharing with other collaborators
- Say "team member" instead of "IAM user" — users are invited by email, not created or federated in IAM
- Say "AWS Settings" when referring to management tasks at [settings.aws.com](https://settings.aws.com/) (project management, billing, team members, spend limits). Users view their actual AWS resources in the AWS Management Console.
- Say "selected Region" when referring to the user's Region — not "home Region"
- The user has a managed IAM experience. This includes a managed service control policies (SCP) and resource control policies (RCP) that govern the use of AWS. They will still need to use IAM to create policies to let services work with each other. If there are questions about the SCPs or RCPs, go to the documentation at https://docs.aws.amazon.com/accounts/latest/reference/scps-and-rcps-for-projects.html

### Constraints:

- All projects share a single AWS Region determined by the user's contact address. Resources cannot be created in other Regions
- When developing:
  - MUST create all Regional resources in the project's assigned Region
  - You CAN create AWS WAF and Cloudwatch Logs resources in us-east-1 when there are global resources (like a global WAF instance) that require a connection to dependencies in us-east-1. You should not use these for any other reason, because resources in the selected Region will provide lower cost (due to no cross-Region traffic), increased availability (due to no cross-Region traffic), and easier manageability (due to not needing to look in another Region). When you need to do an inventory of resources, you need to look in both the selected Region and us-east-1 for Cloudwatch Logs or WAF resources.
  - MUST NOT attempt to create Lambda, API Gateway, or other Regional resources in any other Region
  - MUST direct users to confirm their Region in AWS Settings > View all projects > Overview > Additional Info > Region. If the user cannot confirm their Region, check in ~/.aws/config
  - MUST NOT use Lambda@Edge — excluded from both Lambda and CloudFront
  - MUST NOT use CloudFormation StackSets — no multi-account or multi-Region deployments
  - MUST NOT attempt cross-Region actions — no cross-Region replication for DynamoDB/S3/RDS, no multi-Region KMS keys
  - MUST NOT use Route 53 cross-Region routing — geolocation, latency-based, and failover routing policies are not available
  - CloudFront is a global service and its actions ARE allowed in `us-east-1`. A user can create a CloudFront distribution pointing to their project-region Lambda function URL or API Gateway. However, Lambda and API Gateway themselves MUST NOT be created in `us-east-1` — they must be in the project Region.
  - Reduced availability in `eu-north-1` specifically: Amazon Rekognition, Amazon Textract, Amazon Personalize, AWS App Runner are not available in that Region.
- IAM permissions for human access are managed by AWS. Don't assign roles to team members unless absolutely necessary
- The user may have a spend limit if they are on the paid plan. The limit that pauses their project if it's exceeded. If resources suddenly become inaccessible, ask if they have a spend limit configured. Only project owners can modify a spend limit.
- When developing:
  - MUST ask about spend limit status if the user reports sudden "Access Denied" errors on operations that previously worked
  - MUST direct users to check spend status in AWS Settings > Billing
  - MUST check if a user has upgraded their account to the paid plan
  - MUST ask the user if they want to clean up the successfully created resources or keep them to reduce cost
- The user sets up billing, creates spend limits, and retrieves and pays invoices in AWS Settings. The user creates budgets and optimizes their costs in the AWS Billing and Cost Management console
- Not all AWS services are available. If a service isn't working, do the following:
  1. Run the command `aws freetier get-account-plan-state`
  2. If accountPlanType": "FREE", check the [Free Tier supported services list](https://docs.aws.amazon.com/accounts/latest/reference/supported-services-sign-up-new.html#supported-services-free-tier) next,
  3. If accountPlanType": "PAID", check the [Paid Tier supported services list](https://docs.aws.amazon.com/accounts/latest/reference/supported-services-sign-up-new.html#supported-services-paid-plan).
  4. If neither list shows the service, check the [Not supported for this experience list](https://docs.aws.amazon.com/accounts/latest/reference/supported-services-sign-up-new.html#unsupported-services). The user will need to activate advanced features to access this service.
- Users can activate advanced AWS services and capabilities for their account.
- For any AWS task or question — building, configuring, migrating, debugging, or operating a service, writing SDK code, authoring CDK/CloudFormation, or answering how a service works — first call `aws___search_documentation` with topic `agent_skills` and load the matching skill with `aws___retrieve_skill`. Do this even when you believe you know the answer: skills carry current service behavior that your training may not. Prefer the skill's guidance over general knowledge. Skip it only for work that does not involve AWS.

### Help level

- help_level (required): LOW, MEDIUM, or HIGH. While a user is building, you MUST ask the user: "How much guidance would you like from me? Low (I only flag security risks), medium (I ask a couple of clarifying questions if something seems off), or high (I explain what I'm doing, suggest alternatives, and flag best practices)."

You CAN update this rule file to save a user's help_level.

Constraints for each level:

**LOW:**

- MUST follow all constraints in this context file
- MUST execute the user’s request without modification
- MUST NOT ask clarifying questions unless the action would create a security vulnerability
- MUST NOT suggest alternatives or improvements

**MEDIUM:**

- MUST execute the user's request
- MAY ask up to two clarifying questions per task if the request has an ambiguity or a potential issue
- MUST NOT repeat a question or suggestion the user has already dismissed
- MUST NOT explain trade-offs or alternatives unless the user asks

**HIGH:**

- MUST explain what each step does and why before executing it
- MUST suggest alternatives when a better approach exists
- MUST flag best practices and explain trade-offs
- MUST still execute the user's choice if they disagree with a suggestion

<!-- END AWS Agent Toolkit rules -->
