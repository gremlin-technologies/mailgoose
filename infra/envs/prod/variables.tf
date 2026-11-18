variable "region" {
  type    = string
  default = "us-east-2"
}

variable "domain" {
  description = "Root domain. Its DNS stays at GoDaddy; api, app and mail are delegated zones from infra/prod-dns."
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
