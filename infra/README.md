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
    images/         Private S3 bucket for uploaded images, read only by CloudFront (origin access control).
    github-deploy-role/  IAM role GitHub Actions assumes via OIDC to deploy images.
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
| `images` bucket and CDN | Pictures customers upload for image blocks. The bucket is private; CloudFront reads it through origin access control and serves it at its own `*.cloudfront.net` address. The API task role may put, delete and list objects there, and the API gets `IMAGES_BUCKET` and `IMAGES_BASE_URL`. |

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
  environment's app can only create, change, delete and send from SES identities
  tagged with its own environment, and only its own configuration set may publish
  to its own SES events topic.
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
   console once DKIM shows verified. This is a manual AWS support request.

## How deploys work

- Infra changes: a person runs `terraform plan` / `apply` locally.
- App deploys, today: a person runs `make push-ecr` (images) and registers the
  task definition by hand. GitHub OIDC is off because the AWS project's managed
  service control policy denies creating the OIDC provider (`docs/decisions.md`,
  2026-11-18).
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

## Things the app code must handle

- **DB password rotation.** RDS rotates the managed master password (every 7 days by
  default). Read `DATABASE_SECRET_ARN` from Secrets Manager when opening
  connections and re-read on an auth failure, rather than once at startup.
- **Customer sending domains** are not in Terraform. The API creates SES
  identities for them at runtime (its task role allows `ses:CreateEmailIdentity`
  and friends) and shows the customer the DKIM records to add. Every identity it
  creates must carry the tag in `SES_IDENTITY_TAG` (`Environment=<env>`), or IAM
  refuses the create, and later reads, updates and sends.
- **Every send passes the configuration set** (`SES_CONFIGURATION_SET`) so events
  reach the events queue.
- **Uploaded images** go in `IMAGES_BUCKET` and are served from
  `IMAGES_BASE_URL`. Objects are never public: CloudFront is the only reader.
  Set `Content-Type` and `Cache-Control` on each upload, since CloudFront caches
  as long as the object says.
- Containers run with a read-only root filesystem; write temp files to a mounted
  volume or memory if needed.

## Not here yet

The prod deploy workflow, autoscaling, CloudWatch alarms, WAF, a custom
open/click tracking domain, a custom name for the image CDN (it needs an ACM
certificate in us-east-1), and hosting the web
editor on S3 + CloudFront (the API service can serve it for now).
