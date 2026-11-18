variable "region" {
  type    = string
  default = "us-east-2"
}

variable "project" {
  type    = string
  default = "mailgoose"
}

variable "domain" {
  description = "Root domain mailgoose runs on, e.g. mailgoose.com."
  type        = string
}

variable "hosted_zone_name" {
  description = "Name of the Route 53 zone to create. Empty means the whole domain. Set to a subdomain (e.g. staging.mailgoose.app) when the root domain's DNS lives elsewhere and only that subdomain is delegated to Route 53."
  type        = string
  default     = ""
}

variable "create_hosted_zone" {
  description = "Create the Route 53 zone here. Set false if the zone already exists (for example, the domain was registered in Route 53, which makes one for you)."
  type        = bool
  default     = true
}

variable "create_github_oidc_provider" {
  description = "Create the GitHub Actions OIDC provider. Off while the AWS project's service control policy denies iam:CreateOpenIDConnectProvider."
  type        = bool
  default     = true
}

variable "ecr_repositories" {
  description = "Container image repositories. One Go image serves both the API and the workers (different start commands)."
  type        = list(string)
  default     = ["mailgoose"]
}
