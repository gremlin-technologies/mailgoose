variable "name" {
  description = "Service name, e.g. mailgoose-staging-api."
  type        = string
}

variable "cluster_arn" {
  type = string
}

variable "image" {
  description = "Initial image (repo URL:tag). CI deploys later images; see lifecycle note in main.tf."
  type        = string
}

variable "command" {
  description = "Container command, e.g. [\"serve\"] or [\"worker\"]."
  type        = list(string)
  default     = null
}

variable "cpu" {
  description = "Fargate CPU units (256 = 0.25 vCPU)."
  type        = number
  default     = 256
}

variable "memory" {
  description = "MiB."
  type        = number
  default     = 512
}

variable "desired_count" {
  type    = number
  default = 1
}

variable "subnet_ids" {
  type = list(string)
}

variable "security_group_ids" {
  type = list(string)
}

variable "assign_public_ip" {
  type    = bool
  default = false
}

variable "container_port" {
  description = "Port the container listens on. Null for workers with no listener."
  type        = number
  default     = null
}

variable "target_group_arn" {
  description = "ALB target group to register with, or null."
  type        = string
  default     = null
}

variable "environment" {
  description = "Plain environment variables."
  type        = map(string)
  default     = {}
}

variable "secrets" {
  description = "Env var name => Secrets Manager ARN (optionally ARN:json-key::). Read by the execution role at task start."
  type        = map(string)
  default     = {}
}

variable "task_role_policy_json" {
  description = "IAM policy for what the app itself may call (SQS, SES, ...)."
  type        = string
}

variable "log_retention_days" {
  type    = number
  default = 30
}

variable "cpu_architecture" {
  description = "ARM64 (Graviton) is ~20% cheaper on Fargate and Go cross-compiles to it trivially."
  type        = string
  default     = "ARM64"
}
