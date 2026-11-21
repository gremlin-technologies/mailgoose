variable "name" {
  description = "CodeBuild project name, e.g. mailgoose-staging-deploy. The role is <name>-codebuild."
  type        = string
}

variable "github_repository" {
  description = "owner/name"
  type        = string
}

variable "branch" {
  description = "Pushes to this branch deploy."
  type        = string
  default     = "main"
}

variable "github_connection_arn" {
  description = "CodeConnections connection to GitHub (infra/global output github_connection_arn). It must be AVAILABLE, and set as CodeBuild's GitHub source credential (infra/global does both)."
  type        = string
}

variable "build_image" {
  description = "CodeBuild ARM image."
  type        = string
  default     = "aws/codebuild/amazonlinux-aarch64-standard:3.0"
}

variable "ecr_repository_name" {
  type = string
}

variable "cluster_name" {
  type = string
}

variable "api_service_name" {
  type = string
}

variable "worker_service_name" {
  type = string
}

variable "readyz_url" {
  description = "Checked after the deploy; it must answer 200 with the new commit."
  type        = string
}

variable "environment_label" {
  description = "Shown in Slack posts, e.g. staging."
  type        = string
}

variable "slack_notifications" {
  description = "Post each deploy's result to Slack. Makes an empty secret <name>-slack-webhook for the incoming-webhook URL; until it has a value, deploys skip the post."
  type        = bool
  default     = false
}

variable "log_retention_days" {
  type    = number
  default = 30
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

variable "run_task_cluster_arn" {
  description = "ECS cluster CI may run the one-off migration task in. Null skips the run-task permissions."
  type        = string
  default     = null
}

variable "run_task_definition_arns" {
  description = "Task definition ARNs (use a :* revision wildcard) CI may run as the migration task."
  type        = list(string)
  default     = []
}
