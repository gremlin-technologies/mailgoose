# mailgoose infrastructure

Terraform for running mailgoose on AWS: a Go API and Go workers on ECS Fargate,
Postgres on RDS, SQS for send jobs, SES for delivery, behind an ALB with TLS.

Applied so far: `bootstrap` (state bucket `mailgoose-tfstate-680484232605`),
`global`, and `envs/staging`. Prod has not been applied.

Everything runs in `us-east-2`, the Region assigned to the Gremlin Technologies AWS project.
Regional resources can't be created in any other Region.

## DNS

`mailgoose.app` is registered at GoDaddy, and its DNS stays there: it carries
live Microsoft 365 email (MX, SPF, autodiscover, verification TXT) and the
website. Route 53 hosts only delegated subdomains. A delegation is a set of NS
records at GoDaddy for one name; it takes over that name and everything beneath
it, and nothing else. Nothing in this Terraform touches the apex, `www`,
`autodiscover`, `email`, `_dmarc` or any Microsoft 365 record.

- **Staging:** one zone, `staging.mailgoose.app`, created by `global`
  (`hosted_zone_name`). GoDaddy has an NS record for `staging` pointing at it
  (`terraform output hosted_zone_name_servers` in `global`). Everything staging
  creates lives in it: `api.staging`, `app.staging`, `mail.staging`, DKIM,
  MAIL FROM, DMARC.
- **Prod:** three zones, created by `prod-dns`, because the names are not under
  one subdomain:

  | Zone | Holds |
  | --- | --- |
  | `api.mailgoose.app` | alias to the load balancer, ACM validation record |
  | `app.mailgoose.app` | alias to the load balancer, ACM validation record |
  | `mail.mailgoose.app` | mailgoose's SES sending domain: the three Easy DKIM CNAMEs, `bounce.mail` (custom MAIL FROM MX and SPF) and `_dmarc.mail` |

  `envs/prod` looks the zones up by name (`zone_overrides`) and adds the records.
  Each zone costs about $0.50/month (an estimate; check Route 53 pricing).

SES and ACM work from these subdomain zones: SES verifies the sending domain
with DKIM CNAMEs under `mail.mailgoose.app`, the custom MAIL FROM domain is a
subdomain of it, and ACM validates `api` and `app` with CNAMEs under those
names. Neither needs a record at the apex. If a CAA record is ever added at the
apex, it must allow `amazon.com`, or ACM will refuse to issue.

### Setting up prod DNS

In this order, with AWS credentials for the project:

1. `cd infra/prod-dns && terraform init -backend-config="bucket=<state_bucket>"`,
   then `terraform plan` (expect three zones, nothing else) and `apply`.
2. `terraform output godaddy_ns_records`. At GoDaddy, add each as an NS record
   (name is the label: `api`, `app` or `mail`; value is the name server). Twelve
   records, four per label. Add nothing else and change nothing that is there.
3. Wait until the delegation resolves, for each of the three labels:
   `dig NS api.mailgoose.app +short` returns the four `awsdns` names.
4. Only then apply `envs/prod`. Its certificate validation waits for the
   delegation, so applying first just stalls.

## Layout

```
infra/
  bootstrap/        S3 bucket for Terraform state. Run once, local state.
  global/           Account-wide: staging Route 53 zone, GitHub OIDC provider, ECR repo.
  prod-dns/         Prod's three delegated Route 53 zones (api, app, mail). Apply before envs/prod.
  envs/
    staging/        Thin root: calls modules/stack with staging sizes.
    prod/           Thin root: calls modules/stack with prod sizes.
  modules/
    stack/          One whole environment, wired together. Both envs use it.
    network/        VPC, 2 AZs, public + private subnets, NAT choice, free S3 endpoint.
    database/       RDS Postgres 17, encrypted, managed master password, forced TLS.
    queue/          SQS queue + dead-letter queue (+ optional SNS publish permission).
    email/          SES domain identity, DKIM/MAIL FROM/DMARC records, config set, SNS events topic.
    edge/           ACM cert, ALB (HTTP→HTTPS redirect), Route 53 aliases.
    service/        One Fargate service: task def, log group, execution + task roles.
    images/         Private S3 bucket for uploaded images, read only by CloudFront (origin access control), or with cdn = false by the API alone.
    github-deploy-role/  IAM role GitHub Actions assumes via OIDC to deploy images.
    monitoring/     CloudWatch alarms, error-log metric filters, the alerts SNS topic.
```

What runs per environment:

| Piece | What it does |
| --- | --- |
| `api` service | `mailgoose serve` behind the ALB on port 8080, `/healthz` health check. Serves the REST API (and can serve the built React editor). |
| `worker` service | `mailgoose worker`. Drains the send queue and the SES events queue. |
| `send` queue | API enqueues send jobs; worker sends through SES. Failed jobs land in `send-dlq` after 5 tries. |
| `ses-events` queue | SES → SNS → SQS. Bounces, complaints, deliveries, opens, clicks for basic reports and suppression. |
| RDS Postgres | Master password lives in Secrets Manager, managed and rotated by RDS. |
| `<env>/app` secret | Empty JSON secret for app keys (Stripe etc.). Set values by hand, never in Terraform. |
| `images` bucket and CDN | Pictures customers upload for image blocks. The bucket is private; CloudFront reads it through origin access control and serves it at its own `*.cloudfront.net` address. The API task role may put, read, delete and list objects there, and the API gets `IMAGES_BUCKET` and `IMAGES_BASE_URL`. With `images_cdn = false` (staging for now) there is no CloudFront: the API gets only `IMAGES_BUCKET` and serves images itself. |

All plain AWS provider resources, no community modules, so every line is
readable and `terraform validate` works offline. The VPC could be swapped for
`terraform-aws-modules/vpc/aws` later if it grows.

## One AWS account

Staging and prod live in the same AWS account (decided Nov 17, 2026). They stay
apart by:

- **Names:** every resource starts with `mailgoose-staging-` or `mailgoose-prod-`.
- **State:** separate keys in the state bucket (`envs/staging/...`, `envs/prod/...`).
- **Network:** separate VPCs (`10.10.0.0/16` and `10.20.0.0/16`).
- **Tags:** the providers tag everything `Environment = staging | prod`. Each
  environment's app can only create, change and delete SES identities tagged
  with its own environment, and only its own configuration set may publish to
  its own SES events topic. Tags don't keep sends apart: SES never matches a tag
  condition on `ses:SendEmail` (see `docs/decisions.md`, 2026-11-20), so each
  role sends from its own system identity by exact ARN, and customer-domain
  sends wait on a fix.
- **IAM:** task roles, deploy roles and secrets are per environment and scoped by ARN.

What they still share, by the nature of one account: the SES sending quota, sandbox
status and account-level suppression list, the ECR image repository, and the GitHub
OIDC provider. A staging test that bounces a lot can dent prod's SES reputation, so
send staging tests to SES's mailbox simulator addresses where possible. Moving to
separate accounts later means a second state bucket and re-running global per account.

## Cost notes (rough, before email volume)

Per environment, at the defaults: ALB ~$17/mo, RDS db.t4g.micro ~$12/mo
(db.t4g.small ~$24 in prod), each 0.25 vCPU / 0.5 GB ARM Fargate task ~$7/mo,
NAT gateway ~$33/mo + $0.045/GB, a few dollars of Secrets Manager, Route 53,
CloudWatch Logs, and pennies of S3 for images (CloudFront's first 1 TB a month
out is free). Staging runs with **no NAT** (tasks in public subnets with
public IPs, still locked down by security groups) to save the NAT fee.
VPC interface endpoints are the other way to avoid NAT, but at ~$7/mo each per
AZ they cost more than one NAT until traffic is large. These are estimates, not
quotes; check the AWS pricing pages before relying on them.

## First-time setup

Needs Terraform 1.10 or newer (for S3-native state locking) and AWS credentials
for an admin user or SSO role in the target account.

1. **State bucket**
   ```sh
   cd infra/bootstrap
   terraform init && terraform apply
   terraform output -raw state_bucket   # e.g. mailgoose-tfstate-123456789012
   ```
   Optional: move bootstrap's own state into the bucket by adding a `backend "s3"`
   block with key `bootstrap/terraform.tfstate` and running `terraform init -migrate-state`.

2. **Global** (zone, OIDC provider, ECR). Set `domain` in `global/terraform.tfvars`.
   ```sh
   cd infra/global
   terraform init -backend-config="bucket=<state_bucket>"
   terraform apply
   ```
   If the zone was created here, point the registrar's name servers at
   `hosted_zone_name_servers`. If the domain is already in Route 53, set
   `create_hosted_zone = false`.

3. **Push a first image** tagged `bootstrap` to the ECR repo (or set
   `api_desired_count`/`worker_desired_count` to 0 for the first apply, as
   staging currently does).

4. **Staging, then prod.** Set `domain` in each `terraform.tfvars`.
   ```sh
   cd infra/envs/staging
   terraform init -backend-config="bucket=<state_bucket>"
   terraform plan -out tfplan && terraform apply tfplan
   ```

5. **Lock provider hashes for every platform** you and CI use, then commit the lock files:
   ```sh
   terraform providers lock -platform=linux_amd64 -platform=linux_arm64 -platform=darwin_arm64
   ```

6. **SES production access.** New accounts start in the SES sandbox (only
   verified recipients, 200 emails/day). Request production access from the SES
   console once DKIM shows verified. This is a manual AWS support request; see
   "Bringing up staging for testing" for the details used for staging.

### Applying the image bucket and CDN (MG-23)

Staging, from the repo root:

```sh
cd infra/envs/staging
terraform init -backend-config="bucket=mailgoose-tfstate-680484232605"
terraform plan -out tfplan
terraform apply tfplan
terraform output -json stack | jq -r '.images_bucket, .images_base_url'
```

The plan creates the bucket and its four settings, the bucket policy, the
origin access control and the distribution, updates the API task role's
policy, and registers a new API task definition revision. The services ignore
task definition drift, so the new `IMAGES_*` variables reach the API at its
next deploy. The distribution takes a few minutes to finish deploying. Prod is
the same in `infra/envs/prod`, once prod is being brought up.

### Staging without the CDN (until AWS allows CloudFront)

AWS refused staging's distribution with "Your account must be verified before
you can add new CloudFront resources", which only AWS Support can lift. Until
it does, staging runs with `images_cdn = false` in `infra/envs/staging/main.tf`:

- No origin access control or distribution. The bucket stays private (public
  access block on) and its policy only denies non-TLS requests.
- The API's role may also read the bucket, and the API gets `IMAGES_BUCKET`
  but no `IMAGES_BASE_URL`. It then serves images itself at
  `https://api.staging.mailgoose.app/images/<key>`, by redirecting each request
  to a presigned URL that lasts a few minutes. Image URLs saved in emails stay
  valid, since they point at the API and not at the presigned URL.

Apply it with the same commands as above. After the earlier failed apply, the
plan should:

- **destroy** the origin access control that apply created (staging no longer
  uses it),
- **update** the bucket policy (the CloudFront statement goes) and the API task
  role's policy (adds `s3:GetObject` on the bucket),
- **create** (or replace) the API task definition: the env changes, and the
  failed apply may have removed the old one before it could register the new
  one. Like any task definition change, it reaches the running API at its
  next deploy,
- leave the bucket and its settings alone.

`images_base_url` is then `null`. If the plan wants to replace or destroy the
bucket, stop: that isn't expected.

**Turning the CDN on later:** once AWS Support confirms, set `images_cdn =
true` (or delete the line) and plan and apply again. That creates the origin
access control and distribution, puts the CloudFront statement back in the
bucket policy and sets `IMAGES_BASE_URL` to the CloudFront address. Images
uploaded meanwhile keep working at their API URLs, which redirect for as long
as the API serves `/images/`; new uploads get CloudFront URLs.

## How deploys work

- Infra changes: a person runs `terraform plan` / `apply` locally.
- App deploys, today: a person runs `make push-ecr` (images), then, from a
  machine signed in to AWS, the same script CI will run:
  ```sh
  CLUSTER=mailgoose-staging \
  IMAGE=<account>.dkr.ecr.us-east-2.amazonaws.com/mailgoose:<commit> \
  API_SERVICE=mailgoose-staging-api WORKER_SERVICE=mailgoose-staging-worker \
  scripts/ci/deploy-ecs.sh
  ```
  It needs the AWS CLI and `jq`. GitHub OIDC is off because the AWS project's
  managed service control policy denies creating the OIDC provider
  (`docs/decisions.md`, 2026-11-18).
- App deploys, once OIDC is allowed: `.github/workflows/deploy-staging.yml`
  assumes `mailgoose-staging-github-deploy` through OIDC (no AWS keys stored in
  GitHub), pushes the image to ECR tagged with the commit, runs
  `mailgoosed migrate up` as a one-off task (a failed migration stops the
  deploy), then registers a new task definition revision and updates both
  services (`scripts/ci/deploy-ecs.sh`). The role trusts only
  `repo:gremlin-technologies/mailgoose:ref:refs/heads/main`. Prod is not wired
  up yet: its role is off (`enable_github_deploy = false`) and would accept only
  jobs in the GitHub `production` environment, which can require an approval.
- The ECS services ignore `task_definition` drift so `terraform apply` never
  rolls back an image CI deployed. When Terraform changes env vars or sizes, it
  creates a new revision and CI's next deploy picks it up.

### Turning on CI deploys (staging)

Do these in order, only after the AWS project allows the OIDC provider:

1. `infra/global/terraform.tfvars`: set `create_github_oidc_provider = true`,
   then `terraform apply` in `infra/global`.
2. `infra/envs/staging/main.tf`: set `enable_github_deploy = true`, then
   `terraform plan` (it should add only the deploy role and its policy) and
   `apply`.
3. In GitHub, add the repository **variable** (not a secret)
   `STAGING_DEPLOY_ROLE_ARN` with `terraform output github_deploy_role_arn`.
   Until it is set, the deploy job is skipped.
4. Raise `api_desired_count` and `worker_desired_count` in staging once the
   first deployed image is healthy.

## Bringing up staging for testing

What it took to get staging ready for internal testing on Nov 20, 2026, with
the snags hit along the way. Run it from the repo root on a machine signed in
to the AWS project, with the AWS CLI v2, `jq`, Docker (images are ARM64) and
Terraform 1.10 or newer.

```sh
export AWS_REGION=us-east-2
```

1. **Check what's running.**
   ```sh
   aws ecs describe-services --cluster mailgoose-staging \
     --services mailgoose-staging-api mailgoose-staging-worker \
     --query 'services[].[serviceName,runningCount,desiredCount,taskDefinition]' --output table
   curl -s https://api.staging.mailgoose.app/readyz
   ```

2. **SES.** `aws sesv2 get-account --query ProductionAccessEnabled` says
   whether the account is out of the sandbox. In the sandbox SES only sends
   to verified addresses, 200 a day, one a second. Verify each tester's
   address, tagged like everything else in the environment:
   ```sh
   aws sesv2 create-email-identity --email-identity tester@example.com \
     --tags Key=Environment,Value=staging
   ```
   SES emails that address a link to click. To leave the sandbox, request
   production access in the SES console (Ohio, Account dashboard), mail type
   Marketing, website `https://mailgoose.app`. AWS can take a few days to
   answer. The sandbox and production access cover the whole AWS account, prod
   included.

3. **Alerts.** Put the address in the git-ignored `alerts.auto.tfvars` (see
   "Where alarms go" below), then plan and apply:
   ```sh
   cd infra/envs/staging
   terraform init -backend-config="bucket=mailgoose-tfstate-680484232605"
   terraform plan -out tfplan
   terraform apply tfplan
   cd -
   ```
   Expect the plan to create the email subscription on the alerts topic, or
   replace the existing one if the address changed (a subscription nobody
   confirmed shows up as one to replace), and to register new API and worker
   task definition revisions for `OPS_ALERT_EMAIL`. Anything else is worth a
   second look. Click the link AWS emails to confirm the subscription.

4. **Deploy current main** (see "How deploys work"):
   ```sh
   git checkout main && git pull
   make push-ecr
   CLUSTER=mailgoose-staging \
   IMAGE=680484232605.dkr.ecr.us-east-2.amazonaws.com/mailgoose:$(git rev-parse --short HEAD) \
   API_SERVICE=mailgoose-staging-api WORKER_SERVICE=mailgoose-staging-worker \
   scripts/ci/deploy-ecs.sh
   ```
   It runs the migrations first and stops if one fails. Nothing tells you
   when staging falls behind main, so redeploy after merges you want to test.

5. **Your own account.** Sign up at `https://app.staging.mailgoose.app/signup`.
   Sign up, not sign in: signing in with an address that has no account
   answers the same as for one that does (so the form can't be used to find
   accounts) but sends nothing. Then make yourself an admin with a one-off
   task, which needs the account to exist:
   ```sh
   svc=$(aws ecs describe-services --cluster mailgoose-staging --services mailgoose-staging-api --query 'services[0]')
   task=$(aws ecs run-task --cluster mailgoose-staging --launch-type FARGATE \
     --task-definition "$(echo "$svc" | jq -r .taskDefinition)" \
     --network-configuration "$(echo "$svc" | jq -c .networkConfiguration)" \
     --overrides '{"containerOverrides":[{"name":"app","command":["admin","grant","you@example.com","--reason","staging admin"]}]}' \
     --query 'tasks[0].taskArn' --output text)
   aws ecs wait tasks-stopped --cluster mailgoose-staging --tasks "$task"
   aws logs tail /ecs/mailgoose-staging-api --since 15m | grep -i admin
   ```

6. **The CLI.** `make build` needs the Go version in `go.mod` (1.27). Go 1.21
   or newer fetches it by itself (`GOTOOLCHAIN=auto`, the default), so with
   mise any installed Go works once one is selected, for example
   `mise use go@1.25`. Then:
   ```sh
   make build
   ./bin/mailgoose config set api-url https://api.staging.mailgoose.app
   ./bin/mailgoose login
   ```
   `mailgoose login` prints a code and waits while you approve it in the
   browser, so run it in a terminal of your own. It fails from a coding
   agent's shell or any shell without a TTY.

7. **Admin commands.** `./bin/mailgoose admin signups` lists accounts waiting
   for sign-up review (staging leaves `SIGNUP_REVIEW` unset, so every new
   account waits), and `./bin/mailgoose admin approve-signup <account> --reason "..."`
   lets one in. Both need a build from Nov 19 or later (MG-66), on the server
   and in the CLI. New accounts start in test mode; accounts made before test
   mode keep full sending, so lock them with
   `./bin/mailgoose admin set-test-mode <account> --reason "..."`.

## Things the app code must handle

- **DB password rotation.** RDS rotates the managed master password (every 7 days by
  default). Read `DATABASE_SECRET_ARN` from Secrets Manager when opening
  connections and re-read on an auth failure, rather than once at startup.
- **Customer sending domains** are not in Terraform. The API creates SES
  identities for them at runtime (its task role allows `ses:CreateEmailIdentity`
  and friends) and shows the customer the DKIM records to add. Every identity it
  creates must carry the tag in `SES_IDENTITY_TAG` (`Environment=<env>`), or IAM
  refuses the create, and later reads and updates. Sends from these identities
  are denied today, because SES ignores the tag condition on sends (see
  `docs/decisions.md`, 2026-11-20).
- **Every send passes the configuration set** (`SES_CONFIGURATION_SET`) so events
  reach the events queue.
- **Uploaded images** go in `IMAGES_BUCKET` and are served from
  `IMAGES_BASE_URL`. Objects are never public: CloudFront is the only reader.
  Without `IMAGES_BASE_URL` (no CDN), serve them at `PUBLIC_BASE_URL/images/`
  by redirecting to short-lived presigned GET URLs.
  Set `Content-Type` and `Cache-Control` on each upload, since CloudFront caches
  as long as the object says.
- Containers run with a read-only root filesystem; write temp files to a mounted
  volume or memory if needed.

## Alarms

`modules/monitoring` puts CloudWatch alarms on each environment. Each one
notifies the `<env>-alerts` SNS topic when it fires and again when it clears:

| Alarm | Fires when |
| --- | --- |
| `<queue>-queue-age` | The send or SES events queue's oldest message has waited over 15 minutes. |
| `<queue>-dlq` | Anything is on a dead-letter queue (a message failed five times). |
| `api-5xx-rate` | Over 5% of API requests in 5 minutes end in a 5xx (only judged from 20 requests). |
| `api-no-healthy-tasks` | The load balancer has had no healthy API task for 5 minutes. |
| `<service>-not-running` | No api or worker task has reported in for 10 minutes. |
| `<service>-errors` | A service logged 5 or more `ERROR` lines in 5 minutes. |
| `ses-bounce-rate`, `ses-complaint-rate` | Prod only, since they cover the whole AWS account: bounces over 4%, complaints over 0.08%. |

Errors are tracked in CloudWatch itself: the app writes JSON lines, a metric
filter counts the `ERROR` ones. To read them, use CloudWatch Logs Insights on
`/ecs/<env>-api` or `/ecs/<env>-worker`:

```
filter level = "ERROR" | sort @timestamp desc
```

API lines carry `request_id` (also sent back as the `X-Request-Id` header) and
worker lines carry `queue` and `message_id`, so `filter request_id = "req_..."`
shows everything one request or job did.

**Where alarms go.** Put the address in a git-ignored file next to each
environment's `main.tf`, so no personal address lands in the repo, then apply:

```sh
echo 'alert_email = "you@example.com"' > infra/envs/staging/alerts.auto.tfvars
```

AWS emails that address a link to confirm the subscription; nothing is
delivered until it is clicked. With no address, the alarms still show in the
CloudWatch console. Thresholds are variables of `modules/monitoring`.

The same `alert_email` is also set as `OPS_ALERT_EMAIL` on the API and worker
task definitions (`common_env` in `modules/stack`), so the app's own alerts
go there: automatic pauses, throttles and suspensions, sign-ups waiting for
review, and the worker's daily abuse digest (at `ABUSE_DIGEST_HOUR`, 13:00
UTC by default; see the root README). Empty, `OPS_ALERT_EMAIL` isn't set and
the app only logs those alerts. Applying a change registers new task
definition revisions; the services ignore task definition drift, so the
tasks pick the address up at their next deploy. Those revisions still name
the `initial_image_tag` image (`:bootstrap`), so don't point a service at
the latest revision by hand: that rolls it back to that image. Deploy with
`scripts/ci/deploy-ecs.sh` instead, which copies the latest revision with
only the image swapped (see "How deploys work"); once OIDC is allowed,
`deploy-staging` runs it.

## Not here yet

The prod deploy workflow, autoscaling, WAF, a custom
open/click tracking domain, a custom name for the image CDN (it needs an ACM
certificate in us-east-1), and hosting the web
editor on S3 + CloudFront (the API service can serve it for now).
