variable "region" {
  type    = string
  default = "us-east-1"
}

variable "domain" {
  description = "Root domain (the Route 53 zone from infra/global)."
  type        = string
}

variable "dmarc_report_email" {
  type    = string
  default = ""
}

variable "initial_image_tag" {
  type    = string
  default = "bootstrap"
}
