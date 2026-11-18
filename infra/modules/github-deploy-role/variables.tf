variable "name" {
  type = string
}

variable "oidc_provider_arn" {
  type = string
}

variable "github_repository" {
  description = "owner/name"
  type        = string
}

variable "allowed_subjects" {
  description = <<-EOT
    OIDC subject patterns (after "repo:owner/name:") allowed to assume this role, e.g.
    ["ref:refs/heads/main"] or ["environment:production"]. Keep prod narrow.
  EOT
  type        = list(string)
}

variable "ecr_repository_arns" {
  type = list(string)
}

variable "ecs_service_arns" {
  type = list(string)
}

variable "pass_role_arns" {
  description = "Task and execution role ARNs CI must pass when registering task definitions."
  type        = list(string)
}
