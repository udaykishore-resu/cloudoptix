terraform {
  required_version = ">= 1.7.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.40.0"
    }
    helm = {
      source  = "hashicorp/helm"
      version = ">= 2.13.0"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = ">= 2.31.0"
    }
    random = {
      source  = "hashicorp/random"
      version = ">= 3.6.0"
    }
  }

  # Partial configuration — see backend.hcl.example. Values are supplied at
  # `terraform init -backend-config=backend.hcl` time rather than hard-coded
  # here, because a backend block cannot reference a variable, and this repo
  # must not hard-code an account-specific bucket name (hard rule: no
  # hardcoded account IDs).
  # bucket, region and dynamodb_table are intentionally absent from this
  # block — a partial backend configuration merges them in from
  # backend.hcl at `terraform init -backend-config=backend.hcl` time.
  backend "s3" {
    key     = "cloudoptix/production/terraform.tfstate"
    encrypt = true
  }
}
