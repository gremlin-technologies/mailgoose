variable "name" {
  description = "Name prefix, e.g. mailgoose-staging."
  type        = string
}

variable "cidr_block" {
  type    = string
  default = "10.0.0.0/16"
}

variable "az_count" {
  description = "Availability zones to spread subnets over. ALB and RDS subnet groups need at least 2."
  type        = number
  default     = 2
}

variable "nat_gateway_mode" {
  description = <<-EOT
    How private subnets reach the internet (ECR, SES, SQS, Secrets Manager APIs):
      "single" - one NAT gateway for all AZs. About $33/month plus $0.045/GB.
                 If that AZ fails, tasks in the other AZ lose outbound access. Good default for a small service.
      "per_az" - one NAT per AZ. Doubles NAT cost; survives an AZ outage.
      "none"   - no NAT. Tasks run in public subnets with public IPs (still locked down by security groups).
                 Cheapest; fine for staging.
    VPC interface endpoints (ECR x2, logs, sqs, secretsmanager, sts...) are the other route, but at
    roughly $7/month each per AZ they cost more than one NAT until traffic is heavy. The S3 gateway
    endpoint below is free and always on.
  EOT
  type        = string
  default     = "single"

  validation {
    condition     = contains(["single", "per_az", "none"], var.nat_gateway_mode)
    error_message = "nat_gateway_mode must be single, per_az or none."
  }
}
