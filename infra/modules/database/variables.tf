variable "name" {
  type = string
}

variable "vpc_id" {
  type = string
}

variable "subnet_ids" {
  description = "Private subnets for the DB subnet group (at least 2 AZs)."
  type        = list(string)
}

variable "allowed_security_group_ids" {
  description = "Security groups allowed to connect on 5432 (the app tasks)."
  type        = list(string)
}

variable "engine_version" {
  description = "Postgres major version."
  type        = string
  default     = "17"
}

variable "instance_class" {
  description = "Graviton burstable classes are the cheap start: db.t4g.micro (~$12/mo), db.t4g.small (~$24/mo)."
  type        = string
  default     = "db.t4g.micro"
}

variable "allocated_storage" {
  type    = number
  default = 20
}

variable "max_allocated_storage" {
  description = "Storage autoscaling ceiling in GB."
  type        = number
  default     = 100
}

variable "database_name" {
  type    = string
  default = "mailgoose"
}

variable "multi_az" {
  description = "Standby in a second AZ. Roughly doubles the instance cost."
  type        = bool
  default     = false
}

variable "backup_retention_days" {
  type    = number
  default = 7
}

variable "deletion_protection" {
  description = "Also controls whether a final snapshot is taken on destroy."
  type        = bool
  default     = true
}
