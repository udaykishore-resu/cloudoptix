terraform {
  required_version = ">= 1.7.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.40.0"
    }
    tls = {
      source  = "hashicorp/tls"
      version = ">= 4.0.0"
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
  }
}

