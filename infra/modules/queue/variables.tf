variable "name" {
  type = string
}

variable "visibility_timeout_seconds" {
  description = "Longer than the slowest message handler, or messages get processed twice."
  type        = number
  default     = 60
}

variable "message_retention_seconds" {
  type    = number
  default = 345600 # 4 days
}

variable "max_receive_count" {
  description = "Failed receives before a message moves to the dead-letter queue."
  type        = number
  default     = 5
}

variable "allowed_sns_topic_arns" {
  description = "SNS topics allowed to publish into this queue."
  type        = list(string)
  default     = []
}
