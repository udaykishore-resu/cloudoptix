# terraform/bootstrap — run this ONCE per AWS account/region, before any
# environment composition in terraform/environments/* can be initialised.
#
# Remote state needs somewhere to live before Terraform can manage it
# remotely — the classic chicken-and-egg of "the S3 bucket backing my S3
# backend must itself be created by something." This tiny root module is
# that something: it uses local state (there is nothing left to bootstrap
# after this), creates the versioned, encrypted, locked-down S3 bucket every
# environment's backend.hcl points at, and the DynamoDB table used for state
# locking.
#
# One bucket, one table, shared across dev/staging/production — each
# environment gets its own state *key* (object path) within the bucket, not
# its own bucket, so this only ever needs to run once per account.

terraform {
  required_version = ">= 1.7.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.40.0"
    }
  }
  # Deliberately no backend block — this configuration's own state is local.
  # Treat terraform.tfstate in this directory as sensitive (it will contain
  # this bucket's ARN and the table's ARN) and do not commit it; back it up
  # somewhere durable outside git instead (its own S3 bucket in a
  # break-glass account, or simply re-run bootstrap and import if it's ever
  # lost — nothing here is difficult to recreate by hand if state is lost,
  # since these two resources rarely change).
}

variable "bucket_name" {
  description = "Globally-unique S3 bucket name for Terraform remote state. Include your account ID or a company-specific prefix — bucket names are global across all of AWS, not just your account."
  type        = string
}

variable "lock_table_name" {
  type    = string
  default = "cloudoptix-terraform-locks"
}

variable "region" {
  type    = string
  default = "us-east-1"
}

variable "tags" {
  type    = map(string)
  default = { Purpose = "terraform-remote-state", ManagedBy = "terraform" }
}

provider "aws" {
  region = var.region
}

resource "aws_kms_key" "state" {
  description             = "Encrypts the Terraform remote state bucket."
  enable_key_rotation     = true
  deletion_window_in_days = 30
  tags                    = var.tags
}

resource "aws_kms_alias" "state" {
  name          = "alias/cloudoptix-terraform-state"
  target_key_id = aws_kms_key.state.key_id
}

resource "aws_s3_bucket" "state" {
  bucket = var.bucket_name
  tags   = var.tags

  lifecycle {
    prevent_destroy = true # this bucket holding every environment's state is the single worst thing to lose to a fat-fingered destroy
  }
}

resource "aws_s3_bucket_versioning" "state" {
  bucket = aws_s3_bucket.state.id
  versioning_configuration {
    status = "Enabled" # every historical state version is recoverable — the backstop for a bad apply, not just accidental deletion
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "state" {
  bucket = aws_s3_bucket.state.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm     = "aws:kms"
      kms_master_key_id = aws_kms_key.state.arn
    }
    bucket_key_enabled = true
  }
}

resource "aws_s3_bucket_public_access_block" "state" {
  bucket                  = aws_s3_bucket.state.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_dynamodb_table" "locks" {
  name         = var.lock_table_name
  billing_mode = "PAY_PER_REQUEST" # lock table traffic is bursty and tiny — provisioned capacity would just be one more thing to babysit
  hash_key     = "LockID"

  attribute {
    name = "LockID"
    type = "S"
  }

  server_side_encryption {
    enabled     = true
    kms_key_arn = aws_kms_key.state.arn
  }

  point_in_time_recovery {
    enabled = true
  }

  tags = var.tags

  lifecycle {
    prevent_destroy = true
  }
}

output "bucket_name" {
  value = aws_s3_bucket.state.id
}

output "lock_table_name" {
  value = aws_dynamodb_table.locks.name
}

output "kms_key_arn" {
  value = aws_kms_key.state.arn
}
