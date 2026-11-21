module "stack" {
  source = "../../modules/stack"

  environment = "staging"
  # Only this subdomain is delegated to Route 53; the root stays at GoDaddy.
  zone_name      = "staging.${var.domain}"
  hostnames      = ["api.staging.${var.domain}", "app.staging.${var.domain}"]
  sending_domain = "mail.staging.${var.domain}"
  # Customers without a domain of their own send from here (MG-85), with
  # replies to an address they verified. Prod leaves it unset for now (see
  # docs/decisions.md, 2026-11-21), so shared sending is off there.
  shared_sending_domain = "shared.staging.${var.domain}"

  dmarc_report_email = var.dmarc_report_email
  initial_image_tag  = var.initial_image_tag

  vpc_cidr = "10.10.0.0/16"

  # No NAT gateway: tasks run in public subnets with public IPs, still behind
  # security groups. Saves about $33/month. Switch to "single" to mirror prod.
  nat_gateway_mode = "none"

  db_instance_class        = "db.t4g.micro"
  db_multi_az              = false
  db_backup_retention_days = 1
  deletion_protection      = false

  # No CloudFront until AWS Support allows it on the account (it refused
  # with "Your account must be verified"); the API serves images itself
  # meanwhile. Set to true, or remove, to turn the CDN on.
  images_cdn = false

  # 0 until the first app image is in ECR; raise to 1 after the first deploy.
  api_desired_count    = 1
  worker_desired_count = 1
  log_retention_days   = 14

  # Off until the AWS project allows the GitHub OIDC provider (see
  # docs/decisions.md). When on, any push to main deploys staging.
  enable_github_deploy   = false
  github_deploy_subjects = ["ref:refs/heads/main"]

  # Meanwhile CodeBuild deploys every push to main (docs/decisions.md,
  # 2026-11-21). Apply infra/global first: it makes the GitHub connection.
  enable_codebuild_deploy = true
  # Each deploy's result goes to #deployments in Slack once the webhook URL is
  # in the secret this makes (infra/README.md, "Deploy posts in Slack").
  codebuild_deploy_slack = true

  # Alarms email this address once it confirms AWS's subscription email.
  alert_email = var.alert_email
  # SES reputation is the AWS account's, shared with prod. Staging watches it
  # while it's the only environment running; once prod is, turn this off here
  # and on in prod (see docs/decisions.md, 2026-11-20).
  ses_reputation_alarms = true
}
