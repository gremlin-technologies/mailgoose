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

variable "dmarc_report_email" {
  type    = string
  default = ""
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
