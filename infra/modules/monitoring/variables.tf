variable "name" {
  description = "Prefix for every resource, e.g. mailgoose-staging."
  type        = string
}

variable "alert_email" {
  description = "Address that gets every alarm by email. Empty: the topic is made with no subscriber, and alarms only show in the CloudWatch console. AWS emails the address a link to confirm before anything is delivered."
  type        = string
  default     = ""
}

# --- What to watch ---

variable "queues" {
  description = "Queues whose oldest message's age is watched, by short name (send, ses-events) to SQS queue name."
  type        = map(string)
}

variable "dead_letter_queues" {
  description = "Dead-letter queues that should stay empty, by short name to SQS queue name."
  type        = map(string)
}

variable "alb_arn_suffix" {
  description = "The load balancer's ARN suffix (app/<name>/<id>), CloudWatch's dimension for it."
  type        = string
}

variable "api_target_group_arn_suffix" {
  description = "The API target group's ARN suffix (targetgroup/<name>/<id>)."
  type        = string
}

variable "ecs_cluster_name" {
  type = string
}

variable "services" {
  description = "ECS services to watch, by short name (api, worker): the service name, its log group, and whether it should be running (desired count above 0)."
  type = map(object({
    service_name   = string
    log_group_name = string
    expect_running = bool
  }))
}

variable "ses_reputation_alarms" {
  description = "Alarm on SES's account-wide bounce and complaint rates. These metrics belong to the AWS account, not an environment, so only one environment should turn them on (staging until prod is running, then prod)."
  type        = bool
  default     = false
}

# --- Thresholds (reversible defaults, chosen in MG-53) ---

variable "queue_age_threshold_seconds" {
  description = "Alarm when a queue's oldest message has waited longer than this."
  type        = number
  default     = 900 # 15 minutes
}

variable "api_5xx_percent_threshold" {
  description = "Alarm when more than this percent of the API's requests in 5 minutes end in a 5xx."
  type        = number
  default     = 5
}

variable "api_5xx_min_requests" {
  description = "Below this many requests in 5 minutes, the 5xx rate isn't judged, so one failed request at night doesn't page anyone."
  type        = number
  default     = 20
}

variable "error_log_threshold" {
  description = "Alarm when a service logs at least this many ERROR lines in 5 minutes."
  type        = number
  default     = 5
}

variable "ses_bounce_rate_threshold" {
  description = "SES bounce rate (0 to 1) to alarm at. AWS reviews an account at 5% and may pause sending at 10%."
  type        = number
  default     = 0.04
}

variable "ses_complaint_rate_threshold" {
  description = "SES complaint rate (0 to 1) to alarm at. AWS reviews an account at 0.1% and may pause sending at 0.5%."
  type        = number
  default     = 0.0008
}

variable "ses_bounce_rate_warning_threshold" {
  description = "SES bounce rate (0 to 1) for an early warning, under ses_bounce_rate_threshold."
  type        = number
  default     = 0.025

  validation {
    condition     = var.ses_bounce_rate_warning_threshold > 0 && var.ses_bounce_rate_warning_threshold < 0.05
    error_message = "The bounce warning must be over 0 and under AWS's 5% review level."
  }
}

variable "ses_complaint_rate_warning_threshold" {
  description = "SES complaint rate (0 to 1) for an early warning, under ses_complaint_rate_threshold."
  type        = number
  default     = 0.0005

  validation {
    condition     = var.ses_complaint_rate_warning_threshold > 0 && var.ses_complaint_rate_warning_threshold < 0.001
    error_message = "The complaint warning must be over 0 and under AWS's 0.1% review level."
  }
}
