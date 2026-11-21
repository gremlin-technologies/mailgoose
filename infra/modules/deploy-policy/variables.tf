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
