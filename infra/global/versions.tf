terraform {
  required_version = ">= 1.10"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }

  # Partial config: pass the bucket at init time.
  #   terraform init -backend-config="bucket=<state_bucket from bootstrap>"
  # use_lockfile gives native S3 state locking (Terraform 1.10+), no DynamoDB table.
  backend "s3" {
    key          = "global/terraform.tfstate"
    region       = "us-east-1"
    encrypt      = true
    use_lockfile = true
  }
}

provider "aws" {
  region = var.region

  default_tags {
    tags = {
      Project   = var.project
      ManagedBy = "terraform"
      Stack     = "global"
    }
  }
}
