terraform {
  required_version = ">= 1.7.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.40.0"
    }
    helm = {
      source = "hashicorp/helm"
      # < 3.0.0: the helm provider's 3.0 release turned the `kubernetes {}`
      # provider block and every `set {}` block on helm_release into
      # attributes. This configuration is written for the 2.x block syntax,
      # and an open upper bound let `terraform init` pick 3.x, which fails
      # validate.
      version = ">= 2.13.0, < 3.3.1"
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
    key     = "cloudoptix/dev/terraform.tfstate"
    encrypt = true
  }
}
