variable "name" {
  description = "Prefix for every resource name, e.g. mailgoose-staging."
  type        = string
}

variable "price_class" {
  description = "CloudFront price class. PriceClass_100 serves from North America and Europe only, the cheapest."
  type        = string
  default     = "PriceClass_100"
}

variable "force_destroy" {
  description = "Let terraform destroy delete the bucket with images still in it. Keep false in prod."
  type        = bool
  default     = false
}
