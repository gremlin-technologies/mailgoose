module "stack" {
  source = "../../modules/stack"

  environment    = "prod"
  zone_name      = var.domain
  hostnames      = ["api.${var.domain}", "app.${var.domain}"]
  sending_domain = "mail.${var.domain}"

  dmarc_report_email = var.dmarc_report_email
  initial_image_tag  = var.initial_image_tag

  vpc_cidr = "10.20.0.0/16"

  # One NAT gateway shared by both AZs. Move to "per_az" once an AZ outage
  # would cost more than ~$33/month extra.
  nat_gateway_mode = "single"

  db_instance_class        = "db.t4g.small"
  db_multi_az              = false # decision: true roughly doubles DB cost
  db_backup_retention_days = 14
  deletion_protection      = true

  api_desired_count    = 2 # one per AZ behind the load balancer
  worker_desired_count = 1
  log_retention_days   = 30

  # Only jobs running in the GitHub "production" environment (which can require
  # a manual approval) may deploy prod.
  github_deploy_subjects = ["environment:production"]
}
