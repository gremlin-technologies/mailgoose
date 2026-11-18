# mailgoose infrastructure

Terraform for running mailgoose on AWS: a Go API and Go workers on ECS Fargate,
Postgres on RDS, SQS for send jobs, SES for delivery, behind an ALB with TLS.

Nothing here has been applied to any AWS account yet. Placeholders to replace
before the first apply: `domain = "example.com"` in each `terraform.tfvars`,
and the region (`us-east-1`) if you want another one.

## Layout

```
infra/
  bootstrap/        S3 bucket for Terraform state. Run once, local state.
  global/           Account-wide: Route 53 zone, GitHub OIDC provider, ECR repo.
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
CloudWatch Logs. Staging runs with **no NAT** (tasks in public subnets with
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
   `api_desired_count`/`worker_desired_count` to 0 for the first apply).

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
- App deploys: GitHub Actions assumes `<env>-github-deploy` through OIDC (no AWS
  keys stored in GitHub), pushes the image to ECR, registers a new task
  definition revision and updates the service. Staging accepts pushes to `main`;
  prod accepts only jobs in the GitHub `production` environment, which can
  require an approval.
- The ECS services ignore `task_definition` drift so `terraform apply` never
  rolls back an image CI deployed. When Terraform changes env vars or sizes, it
  creates a new revision and CI's next deploy picks it up.

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
- Containers run with a read-only root filesystem; write temp files to a mounted
  volume or memory if needed.

## Not here yet

GitHub Actions workflows, autoscaling, CloudWatch alarms, WAF, a custom
open/click tracking domain, backups to a second region, and hosting the web
editor on S3 + CloudFront (the API service can serve it for now).
