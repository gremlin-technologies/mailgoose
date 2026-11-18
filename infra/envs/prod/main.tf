module "stack" {
  source = "../../modules/stack"

  environment = "prod"
  # mailgoose.app's DNS stays at GoDaddy (Microsoft 365 mail). Only api, app and
  # mail are delegated to Route 53, each as its own zone, created by
  # infra/prod-dns. Apply that first and add its NS records at GoDaddy before
  # this root's first apply; see infra/README.md, "DNS".
  hostnames      = ["api.${var.domain}", "app.${var.domain}"]
  sending_domain = "mail.${var.domain}"
  zone_overrides = {
    "api.${var.domain}"  = "api.${var.domain}"
    "app.${var.domain}"  = "app.${var.domain}"
    "mail.${var.domain}" = "mail.${var.domain}"
  }

  dmarc_report_email = var.dmarc_report_email
  initial_image_tag  = var.initial_image_tag

  vpc_cidr = "10.20.0.0/16"

  # One NAT gateway shared by both AZs. Move to "per_az" once an AZ outage
  # would cost more than ~$33/month extra.
  nat_gateway_mode = "single"

  db_instance_class = "db.t4g.small"
  # No standby for now (decided Nov 17, 2026). Turn Multi-AZ on once there
  # are paying customers: it roughly doubles the database cost, and failover
  # to the standby takes about a minute instead of a restore from backup.
  db_multi_az              = false
  db_backup_retention_days = 14
  deletion_protection      = true

  api_desired_count    = 2 # one per AZ behind the load balancer
  worker_desired_count = 1
  log_retention_days   = 30

  # Off until the staging deploy has run cleanly and the AWS project allows the
  # GitHub OIDC provider (docs/decisions.md). Must stay explicit: the module's
  # default is on, which would look up a provider that does not exist yet.
  enable_github_deploy = false

  # When on, only jobs running in the GitHub "production" environment (which can
  # require a manual approval) may deploy prod.
  github_deploy_subjects = ["environment:production"]
}
