variable "name" {
  type = string
}

variable "vpc_id" {
  type = string
}

variable "public_subnet_ids" {
  type = list(string)
}

variable "zone_id" {
  type = string
}

variable "hostnames" {
  description = "Hostnames served by the load balancer, e.g. [api.example.com, app.example.com]. The first is the certificate's main name."
  type        = list(string)
}

variable "container_port" {
  type    = number
  default = 8080
}

variable "health_check_path" {
  type    = string
  default = "/healthz"
}

variable "deletion_protection" {
  type    = bool
  default = false
}
