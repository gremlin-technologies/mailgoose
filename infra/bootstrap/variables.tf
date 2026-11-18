variable "region" {
  description = "AWS region for the state bucket."
  type        = string
  default     = "us-east-1"
}

variable "project" {
  description = "Short project name, used in resource names."
  type        = string
  default     = "mailgoose"
}
