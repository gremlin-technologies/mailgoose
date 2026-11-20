# One environment of mailgoose. envs/staging and envs/prod both call this module
# with different sizes, so the two can't drift apart structurally.

data "aws_region" "current" {}
data "aws_caller_identity" "current" {}

# Every name that needs DNS records, and the zone each one lives in. In staging
# they all share one delegated zone; prod delegates api, app and mail separately.
locals {
  dns_names     = concat(var.hostnames, [var.sending_domain])
  zone_name_for = { for n in local.dns_names : n => lookup(var.zone_overrides, n, var.zone_name) }
}

data "aws_route53_zone" "this" {
  for_each = toset(values(local.zone_name_for))

  name         = each.key
  private_zone = false

  lifecycle {
    precondition {
      condition     = each.key != ""
      error_message = "A hostname or the sending domain has no Route 53 zone: set zone_name, or add it to zone_overrides."
    }

    precondition {
      condition = alltrue([
        for n, z in local.zone_name_for : z != each.key || n == each.key || endswith(n, ".${each.key}")
      ])
      error_message = "Zone ${each.key} is assigned a name that is not inside it, so its records would never resolve."
    }
  }
}

data "aws_iam_openid_connect_provider" "github" {
  count = var.enable_github_deploy ? 1 : 0

  url = "https://token.actions.githubusercontent.com"
}

data "aws_ecr_repository" "app" {
  name = var.ecr_repository
}

locals {
  name  = "${var.project}-${var.environment}"
  image = "${data.aws_ecr_repository.app.repository_url}:${var.initial_image_tag}"

  # Staging and prod share one AWS account (decided Nov 17, 2026). Every
  # resource name starts with local.name, state lives under separate keys, and
  # the providers tag everything with Environment, which the IAM policies below
  # use to keep each environment's app away from the other's SES identities.
  ses_identity_arns = "arn:aws:ses:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:identity/*"
}

# --- Network ---

module "network" {
  source = "../network"

  name             = local.name
  cidr_block       = var.vpc_cidr
  nat_gateway_mode = var.nat_gateway_mode
}

# Shared by the API and worker tasks. Outbound open (SES, SQS, ECR, Secrets
# Manager are public AWS endpoints); inbound only from the load balancer.
resource "aws_security_group" "app" {
  name        = "${local.name}-app"
  description = "mailgoose API and worker tasks"
  vpc_id      = module.network.vpc_id
}

resource "aws_vpc_security_group_egress_rule" "app_all" {
  security_group_id = aws_security_group.app.id
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "-1"
}

resource "aws_vpc_security_group_ingress_rule" "app_from_alb" {
  security_group_id            = aws_security_group.app.id
  referenced_security_group_id = module.edge.alb_security_group_id
  ip_protocol                  = "tcp"
  from_port                    = var.container_port
  to_port                      = var.container_port
}

# --- Data ---

module "database" {
  source = "../database"

  name                       = local.name
  vpc_id                     = module.network.vpc_id
  subnet_ids                 = module.network.private_subnet_ids
  allowed_security_group_ids = [aws_security_group.app.id]
  instance_class             = var.db_instance_class
  multi_az                   = var.db_multi_az
  backup_retention_days      = var.db_backup_retention_days
  deletion_protection        = var.deletion_protection
}

# App secrets (Stripe key, session signing key, ...). Terraform creates the
# empty container only; set values with the console or
#   aws secretsmanager put-secret-value --secret-id <arn> --secret-string '{"STRIPE_SECRET_KEY":"..."}'
resource "aws_secretsmanager_secret" "app" {
  name                    = "${local.name}/app"
  description             = "mailgoose ${var.environment} application secrets (JSON)"
  recovery_window_in_days = 7
}

# Uploaded images (DES-5), served through CloudFront, or by the API itself
# when images_cdn is false. Staging's bucket may be destroyed with images in
# it; prod's may not.
module "images" {
  source = "../images"

  name          = local.name
  force_destroy = !var.deletion_protection
  cdn           = var.images_cdn
}

# --- Queues ---

# API enqueues send jobs (one message per batch of recipients); workers drain it.
module "send_queue" {
  source = "../queue"

  name                       = "${local.name}-send"
  visibility_timeout_seconds = 120
}

# SES events (bounce, complaint, delivery, open, click) via SNS.
module "events_queue" {
  source = "../queue"

  name                   = "${local.name}-ses-events"
  allowed_sns_topic_arns = [module.email.events_topic_arn]
}

resource "aws_sns_topic_subscription" "ses_events" {
  topic_arn            = module.email.events_topic_arn
  protocol             = "sqs"
  endpoint             = module.events_queue.arn
  raw_message_delivery = true
}

# --- Email ---

module "email" {
  source = "../email"

  name               = local.name
  sending_domain     = var.sending_domain
  zone_id            = data.aws_route53_zone.this[local.zone_name_for[var.sending_domain]].zone_id
  dmarc_report_email = var.dmarc_report_email
}

# --- Edge ---

module "edge" {
  source = "../edge"

  name                = local.name
  vpc_id              = module.network.vpc_id
  public_subnet_ids   = module.network.public_subnet_ids
  zone_ids            = { for h in var.hostnames : h => data.aws_route53_zone.this[local.zone_name_for[h]].zone_id }
  hostnames           = var.hostnames
  container_port      = var.container_port
  deletion_protection = var.deletion_protection
}

# --- Compute ---

resource "aws_ecs_cluster" "this" {
  name = local.name

  setting {
    name  = "containerInsights"
    value = "disabled" # "enhanced" adds per-task metrics at extra CloudWatch cost
  }
}

resource "aws_ecs_cluster_capacity_providers" "this" {
  cluster_name       = aws_ecs_cluster.this.name
  capacity_providers = ["FARGATE", "FARGATE_SPOT"]
}

locals {
  # OPS_ALERT_EMAIL is the same address as the alarms (var.alert_email): it
  # gets automatic pauses, throttles, suspensions, sign-ups waiting for
  # review and the daily abuse digest. Left unset when alert_email is empty,
  # so the app only logs them.
  common_env = merge({
    APP_ENV               = var.environment
    AWS_REGION            = data.aws_region.current.region
    DATABASE_HOST         = module.database.address
    DATABASE_PORT         = tostring(module.database.port)
    DATABASE_NAME         = module.database.database_name
    DATABASE_SECRET_ARN   = module.database.master_user_secret_arn # read at connect time; RDS rotates it
    SEND_QUEUE_URL        = module.send_queue.url
    SES_EVENTS_QUEUE_URL  = module.events_queue.url
    SES_CONFIGURATION_SET = module.email.configuration_set_name
    SYSTEM_SENDING_DOMAIN = var.sending_domain
    SES_IDENTITY_TAG      = "Environment=${var.environment}" # tag every customer identity the app creates
    PUBLIC_BASE_URL       = "https://${var.hostnames[0]}"
    }, {
    for k, v in { OPS_ALERT_EMAIL = var.alert_email } : k => v if v != ""
  })

  # The API also serves image uploads, so only it gets the image bucket.
  # Without the CDN, IMAGES_BASE_URL is left unset and the API serves the
  # images at PUBLIC_BASE_URL/images, redirecting to presigned URLs.
  api_env = merge(local.common_env, {
    PORT          = tostring(var.container_port)
    IMAGES_BUCKET = module.images.bucket_name
    }, {
    for k, v in { IMAGES_BASE_URL = module.images.base_url } : k => v if var.images_cdn
  })

  # The whole app secret (JSON) is injected at task start; the app parses it.
  app_secrets = {
    APP_SECRETS_JSON = aws_secretsmanager_secret.app.arn
  }
}

data "aws_iam_policy_document" "api" {
  statement {
    sid       = "EnqueueSends"
    actions   = ["sqs:SendMessage", "sqs:GetQueueAttributes"]
    resources = [module.send_queue.arn]
  }

  statement {
    sid       = "ReadDatabaseSecret"
    actions   = ["secretsmanager:GetSecretValue"]
    resources = [module.database.master_user_secret_arn]
  }

  # Customers verify their own sending domains through the app, so the API
  # manages SES identities on their behalf. Identity ARNs aren't known ahead of
  # time, so the resource is a wildcard. Staging and prod share one AWS account,
  # so every identity carries an Environment tag and each environment can only
  # create, read, change or delete identities tagged with its own name.
  statement {
    sid = "CreateCustomerIdentities"
    actions = [
      "ses:CreateEmailIdentity",
      "ses:TagResource",
    ]
    resources = [local.ses_identity_arns]

    condition {
      test     = "StringEquals"
      variable = "aws:RequestTag/Environment"
      values   = [var.environment]
    }
  }

  statement {
    sid = "ManageOwnCustomerIdentities"
    actions = [
      "ses:DeleteEmailIdentity",
      "ses:GetEmailIdentity",
      "ses:PutEmailIdentityConfigurationSetAttributes",
      "ses:PutEmailIdentityDkimSigningAttributes",
      "ses:PutEmailIdentityMailFromAttributes",
    ]
    resources = [local.ses_identity_arns]

    condition {
      test     = "StringEquals"
      variable = "aws:ResourceTag/Environment"
      values   = [var.environment]
    }
  }

  # Image uploads, and deleting an account's images when the account is
  # deleted (internal/images).
  statement {
    sid       = "WriteImages"
    actions   = ["s3:PutObject", "s3:DeleteObject"]
    resources = ["${module.images.bucket_arn}/*"]
  }

  # Without the CDN, the API presigns the URLs images are read from, which
  # needs read on the bucket.
  statement {
    sid       = "ReadImages"
    actions   = ["s3:GetObject"]
    resources = ["${module.images.bucket_arn}/*"]
  }

  statement {
    sid       = "ListImages"
    actions   = ["s3:ListBucket"]
    resources = [module.images.bucket_arn]
  }

  # Test sends ("mailgoose send --test") go straight from the API.
  statement {
    sid       = "SendTestEmail"
    actions   = ["ses:SendEmail", "ses:SendRawEmail"]
    resources = [local.ses_identity_arns, module.email.configuration_set_arn]

    # Only identities and the configuration set tagged for this environment.
    condition {
      test     = "StringEquals"
      variable = "aws:ResourceTag/Environment"
      values   = [var.environment]
    }
  }
}

data "aws_iam_policy_document" "worker" {
  statement {
    sid = "ConsumeQueues"
    actions = [
      "sqs:ReceiveMessage",
      "sqs:DeleteMessage",
      "sqs:ChangeMessageVisibility",
      "sqs:GetQueueAttributes",
    ]
    resources = [module.send_queue.arn, module.events_queue.arn]
  }

  statement {
    sid       = "ReadDatabaseSecret"
    actions   = ["secretsmanager:GetSecretValue"]
    resources = [module.database.master_user_secret_arn]
  }

  statement {
    sid       = "SendEmail"
    actions   = ["ses:SendEmail", "ses:SendRawEmail", "ses:SendBulkEmail"]
    resources = [local.ses_identity_arns, module.email.configuration_set_arn]

    condition {
      test     = "StringEquals"
      variable = "aws:ResourceTag/Environment"
      values   = [var.environment]
    }
  }
}

module "api" {
  source = "../service"

  name                  = "${local.name}-api"
  cluster_arn           = aws_ecs_cluster.this.arn
  image                 = local.image
  command               = ["serve"]
  cpu                   = var.api_cpu
  memory                = var.api_memory
  desired_count         = var.api_desired_count
  subnet_ids            = module.network.app_subnet_ids
  assign_public_ip      = module.network.app_assign_public_ip
  security_group_ids    = [aws_security_group.app.id]
  container_port        = var.container_port
  target_group_arn      = module.edge.api_target_group_arn
  environment           = local.api_env
  secrets               = local.app_secrets
  task_role_policy_json = data.aws_iam_policy_document.api.json
  log_retention_days    = var.log_retention_days
}

module "worker" {
  source = "../service"

  name                  = "${local.name}-worker"
  cluster_arn           = aws_ecs_cluster.this.arn
  image                 = local.image
  command               = ["worker"]
  cpu                   = var.worker_cpu
  memory                = var.worker_memory
  desired_count         = var.worker_desired_count
  subnet_ids            = module.network.app_subnet_ids
  assign_public_ip      = module.network.app_assign_public_ip
  security_group_ids    = [aws_security_group.app.id]
  environment           = local.common_env
  secrets               = local.app_secrets
  task_role_policy_json = data.aws_iam_policy_document.worker.json
  log_retention_days    = var.log_retention_days
}

# --- CI deploy role ---

module "deploy_role" {
  source = "../github-deploy-role"
  count  = var.enable_github_deploy ? 1 : 0

  name                = "${local.name}-github-deploy"
  oidc_provider_arn   = data.aws_iam_openid_connect_provider.github[0].arn
  github_repository   = var.github_repository
  allowed_subjects    = var.github_deploy_subjects
  ecr_repository_arns = [data.aws_ecr_repository.app.arn]
  ecs_service_arns    = [module.api.service_arn, module.worker.service_arn]

  # Migrations run as a one-off task of the API task definition.
  run_task_cluster_arn = aws_ecs_cluster.this.arn
  run_task_definition_arns = [
    "arn:aws:ecs:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:task-definition/${module.api.task_definition_family}:*",
  ]
  pass_role_arns = [
    module.api.task_role_arn,
    module.api.execution_role_arn,
    module.worker.task_role_arn,
    module.worker.execution_role_arn,
  ]
}

# --- Monitoring (OPS-4) ---

module "monitoring" {
  source = "../monitoring"

  name        = local.name
  alert_email = var.alert_email

  queues = {
    send       = module.send_queue.name
    ses-events = module.events_queue.name
  }
  dead_letter_queues = {
    send       = module.send_queue.dlq_name
    ses-events = module.events_queue.dlq_name
  }

  alb_arn_suffix              = module.edge.alb_arn_suffix
  api_target_group_arn_suffix = module.edge.api_target_group_arn_suffix

  ecs_cluster_name = aws_ecs_cluster.this.name
  services = {
    api = {
      service_name   = module.api.service_name
      log_group_name = module.api.log_group_name
      expect_running = var.api_desired_count > 0
    }
    worker = {
      service_name   = module.worker.service_name
      log_group_name = module.worker.log_group_name
      expect_running = var.worker_desired_count > 0
    }
  }

  ses_reputation_alarms = var.ses_reputation_alarms
}
