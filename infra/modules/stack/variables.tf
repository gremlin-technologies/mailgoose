variable "project" {
  type    = string
  default = "mailgoose"
}

variable "environment" {
  description = "staging or prod"
  type        = string
}

variable "zone_name" {
  description = "Existing Route 53 zone that holds every hostname and the sending domain, unless zone_overrides says otherwise. Empty when every name has an override."
  type        = string
  default     = ""
}

variable "zone_overrides" {
  description = <<-EOT
    Names that live in a different Route 53 zone than zone_name: a map of fully
    qualified name (an entry of hostnames, or sending_domain) to the name of its
    zone. Used when each name is its own delegated subdomain, e.g.
    { "api.example.com" = "api.example.com" }. The zones must already exist.
  EOT
  type        = map(string)
  default     = {}
}

variable "hostnames" {
  description = "Hostnames served by the API/web app, first one is the cert's main name."
  type        = list(string)
}

variable "sending_domain" {
  description = "mailgoose's own SES sending domain for this environment."
  type        = string
}

variable "shared_sending_domain" {
  description = <<-EOT
    mailgoose's shared sending domain for this environment (MG-85), set as
    SHARED_SENDING_DOMAIN on the API and worker: accounts without a verified
    domain send from <sending slug>@ this domain, with replies to an address
    they verified. Empty (the default) leaves shared sending off. Its zone is
    zone_name unless zone_overrides names another.
  EOT
  type        = string
  default     = ""
}

variable "dmarc_report_email" {
  type    = string
  default = ""
}

variable "dmarc_policy" {
  description = "DMARC policy for sending_domain, which carries only mailgoose's own mail: none, quarantine or reject. The shared sending domain stays at none whatever this is."
  type        = string
  default     = "none"
}

# --- network ---

variable "vpc_cidr" {
  type    = string
  default = "10.0.0.0/16"
}

variable "nat_gateway_mode" {
  description = "single, per_az or none. See modules/network for the cost trade-off."
  type        = string
  default     = "single"
}

# --- database ---

variable "db_instance_class" {
  type    = string
  default = "db.t4g.micro"
}

variable "db_multi_az" {
  type    = bool
  default = false
}

variable "db_backup_retention_days" {
  type    = number
  default = 7
}

variable "deletion_protection" {
  description = "Protects the database and load balancer from terraform destroy."
  type        = bool
  default     = true
}

# --- app ---

variable "ecr_repository" {
  description = "ECR repository name created in infra/global."
  type        = string
  default     = "mailgoose"
}

variable "initial_image_tag" {
  description = "Tag Terraform uses for the first task definition. Push this tag before the first apply, or set desired counts to 0."
  type        = string
  default     = "bootstrap"
}

variable "images_cdn" {
  description = "Serve uploaded images through CloudFront. With false, the API serves them itself from the private bucket (presigned redirects), e.g. while CloudFront isn't allowed on the account yet."
  type        = bool
  default     = true
}

variable "api_cpu" {
  type    = number
  default = 256
}

variable "api_memory" {
  type    = number
  default = 512
}

variable "api_desired_count" {
  type    = number
  default = 1
}

variable "worker_cpu" {
  type    = number
  default = 256
}

variable "worker_memory" {
  type    = number
  default = 512
}

variable "worker_desired_count" {
  type    = number
  default = 1
}

variable "container_port" {
  type    = number
  default = 8080
}

variable "log_retention_days" {
  type    = number
  default = 30
}

variable "app_settings" {
  description = <<-EOT
    Plain app settings set as environment variables on the API and worker
    (MG-93), such as NEW_ACCOUNTS_TEST_MODE, SIGNUP_REVIEW, ABUSE_EMAIL,
    SEND_RATE_PER_SECOND, ACCOUNT_DAILY_SEND_LIMIT and
    PLATFORM_DAILY_SEND_LIMIT. A setting left out takes the app's default.
    No secrets: they go in the <env>/app secret. Names Terraform already sets
    (DATABASE_HOST, OPS_ALERT_EMAIL ...) are refused.
  EOT
  type        = map(string)
  default     = {}

  validation {
    condition     = alltrue([for k in keys(var.app_settings) : can(regex("^[A-Z][A-Z0-9_]*$", k))])
    error_message = "app_settings names must be environment variable names: capital letters, digits and underscores."
  }

  validation {
    condition = alltrue([for k in keys(var.app_settings) : !contains([
      "APP_ENV", "AWS_REGION", "DATABASE_HOST", "DATABASE_PORT", "DATABASE_NAME", "DATABASE_SECRET_ARN",
      "DATABASE_URL", "SEND_QUEUE_URL", "SES_EVENTS_QUEUE_URL", "SES_CONFIGURATION_SET", "SYSTEM_SENDING_DOMAIN",
      "SES_IDENTITY_TAG", "PUBLIC_BASE_URL", "OPS_ALERT_EMAIL", "SHARED_SENDING_DOMAIN", "PORT", "IMAGES_BUCKET",
      "IMAGES_BASE_URL", "APP_SECRETS_JSON",
    ], k)])
    error_message = "app_settings can't set a variable Terraform sets itself (database, queues, SES, URLs, OPS_ALERT_EMAIL, images, APP_SECRETS_JSON)."
  }

  validation {
    condition     = alltrue([for k in keys(var.app_settings) : !can(regex("(SECRET|PASSWORD|TOKEN|_KEY$)", k))])
    error_message = "app_settings is for plain settings. Secrets go in the <env>/app secret in Secrets Manager, never in Terraform."
  }

  validation {
    condition     = can(tonumber(lookup(var.app_settings, "PLATFORM_DAILY_SEND_LIMIT", "0")))
    error_message = "PLATFORM_DAILY_SEND_LIMIT must be a whole number (0 means no cap)."
  }
}

# --- CI ---

variable "github_repository" {
  type    = string
  default = "gremlin-technologies/mailgoose"
}

variable "enable_github_deploy" {
  description = "Create the GitHub Actions deploy role. Needs the OIDC provider from infra/global."
  type        = bool
  default     = true
}

variable "github_deploy_subjects" {
  description = "OIDC subjects allowed to deploy this environment."
  type        = list(string)
}

variable "enable_codebuild_deploy" {
  description = "Deploy this environment from a CodeBuild project on every push to codebuild_deploy_branch. Needs the GitHub connection from infra/global, Available. Don't turn on together with enable_github_deploy and its workflow, or each merge deploys twice."
  type        = bool
  default     = false
}

variable "codebuild_deploy_branch" {
  type    = string
  default = "main"
}

variable "codebuild_deploy_slack" {
  description = "Post CodeBuild deploy results to Slack (see modules/codebuild-deploy, slack_notifications)."
  type        = bool
  default     = false
}

# --- monitoring ---

variable "alert_email" {
  description = "Address that gets this environment's alarms, and the app's ops alerts and daily abuse digest (OPS_ALERT_EMAIL on the API and worker tasks). Empty: alarms show only in the CloudWatch console, OPS_ALERT_EMAIL is not set and the app only logs its alerts."
  type        = string
  default     = ""
}

variable "ses_reputation_alarms" {
  description = "Alarm on SES's account-wide bounce and complaint rates, and on its account-wide send count nearing the platform's daily cap. Both environments share one AWS account, so only one turns this on: staging until prod is running, then prod."
  type        = bool
  default     = false
}

variable "ses_daily_quota" {
  description = "SES's daily sending quota for the AWS account (aws sesv2 get-account, SendQuota.Max24HourSend). The SES sends alarm nears it only when PLATFORM_DAILY_SEND_LIMIT is 0 (no cap); otherwise it nears the cap."
  type        = number
  default     = 50000
}
