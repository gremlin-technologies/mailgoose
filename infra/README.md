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
  creates lives in it: `api.staging`, `app.staging`, `mail.staging`, the
  shared sending domain `shared.staging` (MG-85), and their DKIM, MAIL FROM
  and DMARC records.
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
  condition on `ses:SendEmail` (see `docs/decisions.md`, 2026-11-20). Each
  role may send as any identity in the account, but only through its own
  configuration set, by exact ARN, and the app checks every message before
  it goes out: it must be from the system sending domain or from a domain its
  account verified in this environment (`mail.Guard`). So on sends, isolation
  rests on our code, not on IAM.
- **IAM:** task roles, deploy roles and secrets are per environment and scoped by ARN.

What they still share, by the nature of one account: the SES sending quota, sandbox
status and account-level suppression list, the ECR image repository, and the GitHub
OIDC provider. A staging test that bounces a lot can dent prod's SES reputation, so
send staging tests to SES's mailbox simulator addresses where possible. Moving to
separate accounts later means a second state bucket and re-running global per account.

**Before the first beta customer, prod gets an AWS account of its own**
(decided Nov 20, 2026). Shared SES reputation and quotas mean staging's
bounces or complaints could get prod's sending paused, and send isolation
rests on app code. The likely shape: this account, whose SES production
access is in review, stays prod, and staging moves to a new account that can
stay in the SES sandbox.

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
- App deploys to staging, automatic: every push to `main` (a merged PR)
  starts the CodeBuild project `mailgoose-staging-deploy`
  (`infra/modules/codebuild-deploy`). It builds the ARM64 image on an ARM
  build machine, pushes it to ECR tagged with the commit's first 12
  characters, and runs `scripts/ci/deploy-ecs.sh`: migrations as a one-off
  task (a failed migration stops the deploy), then both services, then a
  check that `https://api.staging.mailgoose.app/readyz` answers with the new
  commit. Its result shows as a check on the commit in GitHub, and its log is
  in CloudWatch under `/codebuild/mailgoose-staging-deploy`. A build whose
  commit is no longer `main`'s head stops before touching the services, so two
  quick merges can't leave staging on the older one. No AWS keys exist for
  this: CodeBuild runs as the role `mailgoose-staging-deploy-codebuild`, with
  the same permissions the GitHub deploy role would have, and reaches GitHub
  through the CodeConnections connection `mailgoose-github` (infra/global).
  Pull requests never start it, including ones from forks.
- App deploys by hand (prod later, or staging if CodeBuild is down):
  `make push-ecr`, then, from a machine signed in to AWS:
  ```sh
  CLUSTER=mailgoose-staging \
  IMAGE=<account>.dkr.ecr.us-east-2.amazonaws.com/mailgoose:<commit> \
  API_SERVICE=mailgoose-staging-api WORKER_SERVICE=mailgoose-staging-worker \
  READYZ_URL=https://api.staging.mailgoose.app/readyz EXPECT_COMMIT=<commit> \
  scripts/ci/deploy-ecs.sh
  ```
  It needs the AWS CLI and `jq`. `READYZ_URL` and `EXPECT_COMMIT` are
  optional.
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
  creates a new revision and the next deploy picks it up, so deploy again
  right after such an apply (see below).

### After an apply that changes a task definition

A merge to `main` deploys at once, often before its Terraform change is
applied. The deploy copies the latest task definition revision, so if it ran
first, the running tasks lack the change (MG-85's `SHARED_SENDING_DOMAIN` went
missing this way). So after any apply that registers new task definition
revisions (environment, secrets, CPU or memory), deploy again and check:

1. **Deploy again** (it builds `main`'s head, about 6 minutes):
   ```sh
   aws codebuild start-build --project-name mailgoose-staging-deploy --query build.id --output text
   aws codebuild batch-get-builds --ids <build id> --query 'builds[0].buildStatus' --output text
   ```
   Repeat the second command until it says `SUCCEEDED`, or wait for the post
   in `#deployments`. (Without CodeBuild, the manual deploy above does the
   same.)
2. **Check the running tasks have the change**: one deployment, on a
   revision newer than the apply's, holding the new value. Put the variable
   the apply changed in for `NAME` (or look at `cpu` and `memory`). For a
   changed secret, the new ARN shows under
   `containerDefinitions[0].secrets[?name=='NAME'].valueFrom`, not in `value`:
   ```sh
   for svc in api worker; do
     aws ecs describe-services --cluster mailgoose-staging --services mailgoose-staging-$svc \
       --query 'services[0].deployments[].[status,rolloutState,taskDefinition]' --output text
     td=$(aws ecs describe-services --cluster mailgoose-staging --services mailgoose-staging-$svc \
       --query 'services[0].taskDefinition' --output text)
     aws ecs describe-task-definition --task-definition "$td" --query "taskDefinition.{cpu:cpu,memory:memory,image:containerDefinitions[0].image,value:containerDefinitions[0].environment[?name=='NAME'].value|[0]}"
   done
   ```
   Each service should show one `PRIMARY COMPLETED` deployment, an image
   tagged with `main`'s commit (not `:bootstrap`), and the new value. A
   CodeBuild deploy tags the image with the commit's first 12 characters; a
   manual `make push-ecr` uses `git rev-parse --short HEAD` (usually 7), so
   compare the tag with the start of `git rev-parse HEAD` on `main`.

### Template for an infra apply prompt

Infra pull requests come with a prompt for the owner's local Claude Code,
which has the AWS sign-in. Start each one from this and fill in the angle
brackets; drop step 5 only when the plan registers no task definition
revision.

````markdown
Run this from the mailgoose repo after the <ticket> pull request (<what it
does>) is merged into main. Work only in us-east-2 and only in
infra/envs/staging. Don't touch prod or infra/global, don't create any IAM
user or access key, and don't change the AWS plan or spend limit. Ask me
before anything not written here, and stop and tell me if any command fails.

1. Update the repo and check the account:
   git checkout main && git pull
   export AWS_REGION=us-east-2
   aws sts get-caller-identity --query Account --output text

2. Plan staging and show me the summary:
   cd infra/envs/staging
   terraform init -backend-config="bucket=mailgoose-tfstate-<account>"
   terraform plan -out=tfplan
   Expected: <each resource added, changed or replaced, and the summary
   line>. If the plan shows anything else, stop and show me. Wait for my yes.

3. After my yes: terraform apply tfplan

4. Verify: <commands and what each should show>.

5. The apply registered new task definition revisions, so deploy again and
   check the running tasks have the change, as infra/README.md says in
   "After an apply that changes a task definition" (<the variable or size
   to check>).

6. Report back: the plan summary, what the checks showed, and anything that
   didn't match.
````

### Turning on CI deploys (staging)

GitHub OIDC can't be used while the AWS project's managed service control
policy denies `iam:CreateOpenIDConnectProvider` (`docs/decisions.md`,
2026-11-21), so staging deploys from CodeBuild. Once, from the repo root on
`main`, on a machine signed in to the AWS project:

```sh
export AWS_REGION=us-east-2
```

1. **Create the GitHub connection.** It starts `PENDING`:
   ```sh
   cd infra/global
   terraform init -backend-config="bucket=mailgoose-tfstate-<account>"
   terraform plan -var github_connection_ready=false   # adds only aws_codestarconnections_connection.github
   terraform apply -var github_connection_ready=false
   terraform output github_connection_status            # "PENDING"
   ```
2. **Finish it in the console**, signed in to GitHub as `gremlin-technologies`
   (a personal GitHub account, not an organization, so its owner does this):
   AWS Console, Region US East (Ohio) > Developer Tools > Settings >
   Connections > `mailgoose-github` > **Update pending connection**. At the
   **App installation** step choose **Install a new app**; never leave the box
   blank, which makes a "GitHub user" connection with no app installed and
   CodeBuild can't create its webhook. On GitHub, install "AWS Connector for
   GitHub" on `gremlin-technologies` with **Only select repositories** =
   `mailgoose` > **Install** > back in AWS, pick that installation >
   **Connect**. Then:
   ```sh
   aws codeconnections get-connection --connection-arn "$(terraform output -raw github_connection_arn)" \
     --query Connection.ConnectionStatus --output text   # "AVAILABLE"
   ```
   Check on GitHub too: the repo's **Settings > GitHub Apps** must list AWS
   Connector for GitHub. If it doesn't, the connection can't be fixed in
   place: recreate it (`terraform apply -replace='aws_codestarconnections_connection.github[0]' -var github_connection_ready=false`)
   and do this step again.
3. **Make it CodeBuild's GitHub credential:**
   ```sh
   terraform apply        # adds only aws_codebuild_source_credential.github
   cd ../..
   ```
4. **Create the deploy project:**
   ```sh
   cd infra/envs/staging
   terraform plan   # adds only module.stack.module.codebuild_deploy[0].* (8 resources)
   terraform apply
   cd ../../..
   ```
   If the apply fails with `OAuthProviderException: User is not authorized to
   access connection` right after it changed the role's `build` policy, wait a
   minute and apply again: the new IAM permissions hadn't reached CodeBuild
   yet (seen Nov 21; the retry worked with no changes).
5. **Try it** before relying on merges:
   ```sh
   id=$(aws codebuild start-build --project-name mailgoose-staging-deploy --query build.id --output text)
   aws logs tail /codebuild/mailgoose-staging-deploy --follow   # Ctrl-C once it says "Deployed"
   aws codebuild batch-get-builds --ids "$id" --query 'builds[0].buildStatus' --output text   # SUCCEEDED
   curl -s https://api.staging.mailgoose.app/readyz
   ```
   After that, every merge to `main` deploys by itself.

Turning it off: `enable_codebuild_deploy = false` in `infra/envs/staging` and
apply. Deploys by hand keep working either way.

### Deploy posts in Slack

Each CodeBuild deploy posts its result to `#deployments` in the Gremlin
Technologies Slack: deployed or failed, the commit, its title and author, and
a link to the build log. A build skipped because `main` moved on posts
nothing. The Slack incoming-webhook URL lives in the Secrets Manager secret
`mailgoose-staging-deploy-slack-webhook` (about $0.40 a month), never in the
repo or in Terraform state; Terraform makes the secret empty, and until it has
a value deploys skip the post.

1. **Slack** (a workspace admin): https://api.slack.com/apps > **Create New
   App** > **From scratch** > name `mailgoose deploys`, workspace Gremlin
   Technologies > **Create App**. In the app: **Incoming Webhooks** > turn
   **Activate Incoming Webhooks** on > **Add New Webhook** (at the bottom) >
   pick `#deployments` > **Allow**. Copy the webhook URL
   (`https://hooks.slack.com/services/...`). Treat it like a password: anyone
   with it can post to the channel.
2. **AWS**, in a terminal yourself (not through an AI agent, so the URL stays
   out of chats), after the staging apply in step 4 above:
   ```sh
   read -rsp "Slack webhook URL: " url; echo
   aws secretsmanager put-secret-value --region us-east-2 \
     --secret-id mailgoose-staging-deploy-slack-webhook --secret-string "$url"
   unset url
   ```
   The next deploy posts. To stop the posts, delete the webhook in the Slack
   app; to change the channel, add a new webhook and put its URL in the same way.

### Rolling back to an earlier revision

A Terraform apply that changes a task definition (environment, secrets, CPU
or memory) replaces it, and the old revision stays registered
(`skip_destroy`, MG-102), so every revision a deploy or an apply made is still
there to go back to. Revisions deregistered before that was applied (such
as the staging API's `:5`) are gone. From a machine signed in to the AWS
project, for staging (for prod once it runs: `ENV=prod` and
`API_HOST=api.mailgoose.app`):

```sh
export AWS_REGION=us-east-2 ENV=staging API_HOST=api.staging.mailgoose.app
```

1. **List the recent revisions and their images**, newest first, and see
   which one each service runs now:
   ```sh
   for svc in api worker; do
     echo "== $svc, running: $(aws ecs describe-services --cluster mailgoose-$ENV \
       --services mailgoose-$ENV-$svc --query 'services[0].taskDefinition' --output text)"
     for td in $(aws ecs list-task-definitions --family-prefix mailgoose-$ENV-$svc \
         --status ACTIVE --sort DESC --max-items 10 --query 'taskDefinitionArns[]' --output text); do
       echo "${td##*/}  $(aws ecs describe-task-definition --task-definition "$td" \
         --query "taskDefinition.containerDefinitions[?name=='app'].image | [0]" --output text)"
     done
   done
   ```
   Each image's tag is the first 12 characters of the commit it was built
   from. Pick the newest revision of each service with the last good commit.
   Skip revisions whose image ends in `:bootstrap`: Terraform registered
   those, and that image is the placeholder from the first apply.
2. **Point both services at it** (put the revision numbers in for `N` and
   `M`), then wait and check:
   ```sh
   aws ecs update-service --cluster mailgoose-$ENV --service mailgoose-$ENV-api \
     --task-definition mailgoose-$ENV-api:N >/dev/null
   aws ecs update-service --cluster mailgoose-$ENV --service mailgoose-$ENV-worker \
     --task-definition mailgoose-$ENV-worker:M >/dev/null
   aws ecs wait services-stable --cluster mailgoose-$ENV \
     --services mailgoose-$ENV-api mailgoose-$ENV-worker
   curl -s https://$API_HOST/readyz
   ```
   `/readyz` should report the commit you rolled back to.

What to know before you do it:

- **Migrations stay.** They're additive (`docs/mvp-tickets.md`, "Done for
  every ticket"), so the older code runs on the newer schema.
- **An old revision has the settings it had then.** A variable an apply added
  since is missing from it. To roll back the code but keep today's settings,
  run the manual deploy in "How deploys work" with the older image tag in
  `IMAGE` instead: it copies the latest revision and swaps only the image.
- **The next deploy ends the rollback.** Terraform ignores which revision a
  service runs, so an apply leaves it alone, but the next merge to `main`
  deploys as usual. Revert the bad change on `main` rather than holding
  merges back.
- Kept revisions cost nothing. `aws ecs deregister-task-definition` removes
  one by hand if the list ever gets in the way.

### Turning on GitHub OIDC deploys instead (later)

Only if the AWS project ever allows the OIDC provider (today that means
activating advanced features, which can't be undone; see `docs/decisions.md`):

1. `infra/global/terraform.tfvars`: set `create_github_oidc_provider = true`,
   then `terraform apply` in `infra/global`.
2. `infra/envs/staging/main.tf`: set `enable_github_deploy = true` and
   `enable_codebuild_deploy = false`, then `terraform plan` and `apply`.
3. In GitHub, add the repository **variable** (not a secret)
   `STAGING_DEPLOY_ROLE_ARN` with `terraform output github_deploy_role_arn`.
   Until it is set, `.github/workflows/deploy-staging.yml` skips itself.

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
   `mise use go@1.25`, or for one command `MISE_GO_VERSION=1.25.10 make build`
   (what worked on Nov 20). Then:
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

8. **Check customer domains work.** Sends no longer rely on tags, but adding,
   checking and deleting a customer's domain still do (`aws:RequestTag` and
   `aws:ResourceTag` on the SES identity calls). This passed on Nov 20; run it
   again after changing those statements. Add a made-up domain, check it, and
   delete it (the CLI never prompts, so `delete` needs `--yes`). The identity
   stays pending and nothing is sent:
   ```sh
   ./bin/mailgoose domain add tagcheck-mailgoose.com
   ./bin/mailgoose domain verify tagcheck-mailgoose.com
   ./bin/mailgoose domain delete tagcheck-mailgoose.com --yes
   ```
   `add` should print DKIM records, `verify` should say it isn't verified yet
   (and exit 1), and `delete` should succeed. An error on any of them that the API log
   (`aws logs tail /ecs/mailgoose-staging-api --since 15m | grep -i AccessDenied`)
   shows as an SES `AccessDenied` means SES ignores tags there too, and those
   statements need the same change as sends.

## Things the app code must handle

- **DB password rotation.** RDS rotates the managed master password (every 7 days by
  default). Read `DATABASE_SECRET_ARN` from Secrets Manager when opening
  connections and re-read on an auth failure, rather than once at startup.
- **Customer sending domains** are not in Terraform. The API creates SES
  identities for them at runtime (its task role allows `ses:CreateEmailIdentity`
  and friends) and shows the customer the DKIM records to add. Every identity it
  creates must carry the tag in `SES_IDENTITY_TAG` (`Environment=<env>`), or IAM
  refuses the create, and later reads and updates. Sends aren't tag-conditioned
  (SES ignores tags on sends, `docs/decisions.md` 2026-11-20): before every
  send the app checks the From domain is the system sending domain or one the
  account verified in this environment, and refuses the message otherwise.
- **The shared sending domain** (MG-85, `SHARED_SENDING_DOMAIN`) is in
  Terraform, like the system sending domain: `shared_sending_domain` on the
  stack makes its SES identity, DKIM, MAIL FROM and DMARC records and sets
  the variable on the API and worker. The app lets mail from it through only
  as the account's own sending slug with a verified Reply-To. Staging sets it
  to `shared.staging.mailgoose.app`; prod leaves it unset, so shared sending
  is off there. See "Shared sending domain" below.
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

## Shared sending domain (MG-85)

Accounts without a verified domain of their own send from
`<sending slug>@<shared sending domain>`, with replies to an address they
verified. Shared sending is on in an environment only when its root sets
`shared_sending_domain`; it is the same SES setup as the system sending
domain, with the same configuration set, so its bounces and complaints reach
the events worker. Its reputation is shared by every account that uses it.

To go live in staging (the variable is already set in `envs/staging`):

1. From a machine signed in to AWS, in `infra/envs/staging`:
   `terraform plan`, check it adds one SES identity
   (`shared.staging.mailgoose.app`), its MAIL FROM, six Route 53 records
   (three DKIM CNAMEs, `bounce.shared.staging` MX and TXT, and
   `_dmarc.shared.staging`) and new API and worker task definitions (the
   new variable), and changes nothing else; then `terraform apply`.
2. Wait for SES to show the identity verified, DKIM and MAIL FROM both
   successful (usually minutes):
   `aws sesv2 get-email-identity --email-identity shared.staging.mailgoose.app --region us-east-2`
   should show `"VerifiedForSendingStatus": true`, DKIM `"Status": "SUCCESS"`
   and `"MailFromDomainStatus": "SUCCESS"`.
3. Deploy staging (a merge to main once CodeBuild deploys, or the manual
   deploy in "How deploys work"). The deploy copies the latest task
   definitions, so the running tasks get `SHARED_SENDING_DOMAIN` then. The
   image must include the app change (MG-85, migration 00041); an older
   image ignores the variable.
4. Check it: in a staging account with no domain, send a test from the
   owner's own address. It should arrive from
   `<sending slug>@shared.staging.mailgoose.app`, with Reply-To the owner's
   address, and pass SPF, DKIM and DMARC in the message's headers.

For prod: pick the shared domain first (an open question in
`docs/decisions.md`; the recommendation is a separately registered domain, so
a reputation problem on it can't reach `mailgoose.app` or its sign-in mail).
It needs a Route 53 zone (add it to `zone_overrides`), then the same steps
with `shared_sending_domain` set in `envs/prod`.

To turn shared sending off again, remove `shared_sending_domain` and apply.
The identity and its records are destroyed, so SES refuses mail from the
domain at once; the variable leaves the running tasks at the next deploy.

## Alarms

`modules/monitoring` puts CloudWatch alarms on each environment. Each one
notifies the `<env>-alerts` SNS topic when it fires and again when it clears:

| Alarm | Fires when |
| --- | --- |
| `<queue>-queue-age` | The send or SES events queue's oldest message has waited over 15 minutes. |
| `<queue>-dlq` | Anything is on a dead-letter queue (a message failed five times). What to do: [A dead-letter queue alarm fired](#a-dead-letter-queue-alarm-fired). |
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

## A dead-letter queue alarm fired

`mailgoose-<env>-send-dlq` or `mailgoose-<env>-ses-events-dlq` fired (each
alarm has its queue's name): a message failed five times and SQS set it
aside on the dead-letter queue (DLQ). The queues per environment:

| Queue | Its dead-letter queue | What's on it |
| --- | --- | --- |
| `mailgoose-staging-send` | `mailgoose-staging-send-dlq` | Send batches: `{"account_id", "campaign_id", "ids"}`, up to 100 `campaign_recipients` rows. |
| `mailgoose-staging-ses-events` | `mailgoose-staging-ses-events-dlq` | SES bounce, complaint, delivery and other events. |

Prod's are the same with `prod` for `staging`. A DLQ keeps messages 14 days.
The source queues keep them 4 days, and SQS may count a message's age from
when it was first queued, so move messages back within a few days of the
alarm. From a machine signed in to the AWS project, for staging (for prod:
`ENV=prod` and `API_HOST=api.mailgoose.app`):

```sh
export AWS_REGION=us-east-2 ENV=staging API_HOST=api.staging.mailgoose.app
```

**First, find the cause** in the worker's log. Each failed try logs
`message left on queue` with `queue`, `message_id`, `receive_count` and
`err`:

```sh
aws logs filter-log-events --log-group-name /ecs/mailgoose-$ENV-worker \
  --start-time $(( ($(date +%s) - 86400) * 1000 )) \
  --filter-pattern '{ $.msg = "message left on queue" }' \
  --query 'events[].message' --output text | tail -n 20
```

Fix that first. Moving messages back while the cause is still there only
sends them through five more failures and back to the DLQ.

### `ses-events`

Usually the database was down or locked for 10 minutes or more (an outage, a
long migration). Until these events are handled, their bounces and
complaints don't suppress contacts or count toward an account's pause.
Handling is idempotent by event key, so an event handled before is skipped
when it comes again.

1. **Check the database is healthy:** the instance says `available` and
   `/readyz` answers 200. Then check that the `err` lines above stopped.
   ```sh
   aws rds describe-db-instances --db-instance-identifier mailgoose-$ENV \
     --query 'DBInstances[0].DBInstanceStatus' --output text
   curl -s -o /dev/null -w '%{http_code}\n' https://$API_HOST/readyz
   ```
2. **Move the messages back** to `ses-events`:
   ```sh
   DLQ_ARN=$(aws sqs get-queue-attributes --attribute-names QueueArn \
     --queue-url "$(aws sqs get-queue-url --queue-name mailgoose-$ENV-ses-events-dlq --query QueueUrl --output text)" \
     --query Attributes.QueueArn --output text)
   aws sqs start-message-move-task --source-arn "$DLQ_ARN" \
     --destination-arn "${DLQ_ARN%-dlq}" --max-number-of-messages-per-second 10
   ```
3. **Watch it finish and the alarm clear** (below).

### `send`

A batch dead-letters when its fifth try ends in an error. Moving it back
never sends a copy twice and never charges for a refunded one: a worker only
claims rows that are still `queued`, so rows already sent, skipped or failed
are passed over. A copy marked failed was refunded and stays failed; moving
the batch doesn't send it, and a customer who wants those recipients reached
sends a new campaign. So the question per message is only whether its cause
is fixed, and what the move will do.

1. **Read the messages** without removing them. Each comes back to the DLQ
   after 60 seconds:
   ```sh
   DLQ_URL=$(aws sqs get-queue-url --queue-name mailgoose-$ENV-send-dlq --query QueueUrl --output text)
   aws sqs receive-message --queue-url "$DLQ_URL" --max-number-of-messages 10 \
     --visibility-timeout 60 --attribute-names All \
     --query 'Messages[].[MessageId,Attributes.ApproximateReceiveCount,Body]' --output text
   ```
2. **Read the worker's lines for each one**, by its campaign and message id:
   ```sh
   aws logs filter-log-events --log-group-name /ecs/mailgoose-$ENV-worker \
     --start-time $(( ($(date +%s) - 4 * 86400) * 1000 )) \
     --filter-pattern '{ ($.campaign_id = "CAMPAIGN_ID") || ($.message_id = "MESSAGE_ID") }' \
     --query 'events[].message' --output text
   ```
   Look at the last try: the `message left on queue` line's `err`, and the
   `send batch finished` line with `"attempt":5`, if there is one.
   - **`err` starts with `some copies failed after`:** the batch
     ran to its end. SES refused the `failed` copies on every try; they're
     marked failed with SES's reason in `campaign_recipients.error`, and
     refunded. Every row is finished, so there is nothing left to send.
     Safe to move: the worker finds nothing queued and deletes the message,
     which clears the alarm. If many copies of one campaign were refused,
     read the reason before moving on: it may be the account's From or
     content.
   - **Any other `err`** (the database, a timeout, a deploy): the batch
     stopped partway. Rows it didn't reach are still `queued`. While the
     campaign is sending, the dispatcher puts them in a new batch an hour
     after they were queued, so they may have gone out already. A row cut
     off mid-send is marked failed after 10 minutes and refunded, never
     resent. Safe to move once the cause is fixed: the move sends whatever
     is still `queued`, once.
   - **Not safe yet:** the same `err` still shows in lines from the last few
     minutes. Fix it first; the message keeps on the DLQ.

   SQS may carry the receive count over on the move. If it does, the worker
   treats a moved batch as its last try, so a copy SES refuses again is
   marked failed and refunded at once instead of after five tries.
3. **Move the messages back** to `send`:
   ```sh
   DLQ_ARN=$(aws sqs get-queue-attributes --queue-url "$DLQ_URL" --attribute-names QueueArn \
     --query Attributes.QueueArn --output text)
   aws sqs start-message-move-task --source-arn "$DLQ_ARN" \
     --destination-arn "${DLQ_ARN%-dlq}" --max-number-of-messages-per-second 10
   ```
   Then check the campaigns' new `send batch finished` lines say
   `"ended":"done"`.

The rows themselves (`campaign_recipients.status` and `error`) can't be read
from outside the VPC today; the worker's lines above carry the same counts.

### Watching the move and the alarm

With `DLQ_ARN` and `DLQ_URL` set for the queue you moved (for `ses-events`,
`DLQ_URL=$(aws sqs get-queue-url --queue-name mailgoose-$ENV-ses-events-dlq --query QueueUrl --output text)`):

```sh
aws sqs list-message-move-tasks --source-arn "$DLQ_ARN" --max-results 1 \
  --query 'Results[0].[Status,ApproximateNumberOfMessagesMoved,ApproximateNumberOfMessagesToMove,FailureReason]' --output text
aws sqs get-queue-attributes --queue-url "$DLQ_URL" \
  --attribute-names ApproximateNumberOfMessages ApproximateNumberOfMessagesNotVisible --output text
aws cloudwatch describe-alarms --alarm-names "${DLQ_ARN##*:}" \
  --query 'MetricAlarms[0].StateValue' --output text
```

The task says `RUNNING`, then `COMPLETED` with every message moved. The DLQ
then shows `0 0`, and the alarm goes back to `OK` within about 10 minutes
(it looks at 5-minute periods) and emails that it cleared. If the alarm
fires again later, the moved messages failed five more times: the cause
isn't fixed, so go back to the log lines. To stop a move partway, use
`aws sqs cancel-message-move-task --task-handle <TaskHandle from the start command>`.

## Not here yet

The prod deploy workflow, autoscaling, WAF, a custom
open/click tracking domain, a custom name for the image CDN (it needs an ACM
certificate in us-east-1), and hosting the web
editor on S3 + CloudFront (the API service can serve it for now).
