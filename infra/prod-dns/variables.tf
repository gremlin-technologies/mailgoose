variable "region" {
  type    = string
  default = "us-east-2"
}

variable "domain" {
  description = "Root domain. Its DNS stays at the registrar (GoDaddy); only the subdomains below are delegated to Route 53."
  type        = string
}

variable "delegated_subdomains" {
  description = "Labels under the root domain that get their own Route 53 zone: the API, the web app, and mailgoose's own sending domain."
  type        = set(string)
  default     = ["api", "app", "mail"]
}
