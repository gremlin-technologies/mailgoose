variable "name" {
  type = string
}

variable "sending_domain" {
  description = <<-EOT
    mailgoose's own sending domain (system mail such as account and billing notices).
    Customer domains are verified at runtime by the app through the SES API, not here.
  EOT
  type        = string
}

variable "zone_id" {
  description = "Route 53 zone that holds sending_domain."
  type        = string
}

variable "shared_sending_domain" {
  description = <<-EOT
    mailgoose's shared sending domain (MG-85): customers without a verified
    domain of their own send from <their sending slug>@ this domain, with
    replies to an address they verified. Must differ from sending_domain.
    Empty (the default) creates nothing and leaves shared sending off.
  EOT
  type        = string
  default     = ""
}

variable "shared_zone_id" {
  description = "Route 53 zone that holds shared_sending_domain. Needed only when it's set."
  type        = string
  default     = ""
}

variable "mail_from_subdomain" {
  description = "Custom MAIL FROM subdomain so SPF aligns with the sending domain."
  type        = string
  default     = "bounce"
}

variable "dmarc_policy" {
  description = "DMARC policy for sending_domain, the system domain: none, quarantine or reject. Start at none, move to quarantine once reports look clean."
  type        = string
  default     = "none"

  validation {
    condition     = contains(["none", "quarantine", "reject"], var.dmarc_policy)
    error_message = "dmarc_policy must be none, quarantine or reject."
  }
}

variable "shared_dmarc_policy" {
  description = "DMARC policy for shared_sending_domain, which carries customers' mail: none, quarantine or reject. Stays none until its reports are clean."
  type        = string
  default     = "none"

  validation {
    condition     = contains(["none", "quarantine", "reject"], var.shared_dmarc_policy)
    error_message = "shared_dmarc_policy must be none, quarantine or reject."
  }
}

variable "dmarc_report_email" {
  description = "Where aggregate DMARC reports go. Empty to skip rua."
  type        = string
  default     = ""
}

variable "event_types" {
  description = "SES events published to SNS for the events worker. No OPEN or CLICK: with either here SES adds its own pixel and link rewriting, and mailgoose tracks opens and clicks itself (MG-37), so a campaign's tracking switch has to mean no tracking at all."
  type        = list(string)
  default     = ["BOUNCE", "COMPLAINT", "DELIVERY", "REJECT", "RENDERING_FAILURE", "DELIVERY_DELAY"]
}
