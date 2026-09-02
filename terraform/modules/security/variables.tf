variable "name" {
  description = "Name prefix for every resource this module creates."
  type        = string
}

variable "environment" {
  type = string
}

variable "kms_key_deletion_window_days" {
  description = "Days a KMS key stays pending-deletion before AWS actually deletes it. 30 (the max) in every environment — this only protects against fat-fingering a destroy, and a shorter window buys nothing."
  type        = number
  default     = 30
}

variable "eks_oidc_provider_arn" {
  description = "ARN of the EKS cluster's IAM OIDC provider (from the eks module's output), used as the trust anchor for every IRSA role this module creates."
  type        = string
}

variable "eks_oidc_provider_url" {
  description = "The OIDC provider's issuer URL without the https:// prefix (also from the eks module), used to build the federated trust condition."
  type        = string
}

variable "namespace" {
  description = "Kubernetes namespace the CloudOptix chart is installed into — must match helm/cloudoptix's release namespace for the IRSA trust condition's service-account subject to match."
  type        = string
  default     = "cloudoptix"
}

variable "service_accounts" {
  description = <<-EOT
    Component name -> Kubernetes ServiceAccount name, one IRSA role per entry.
    Every role gets the same least-privilege shape (see main.tf): read its own
    secrets, decrypt with the app KMS key, and assume any customer onboarding
    role. Split per-component (rather than one shared role) so CloudTrail in a
    customer's account, and IAM Access Analyzer in ours, can distinguish "the
    discovery worker did this" from "the automation worker did this".
  EOT
  type        = map(string)
  default = {
    api                  = "cloudoptix-api"
    worker_discovery     = "cloudoptix-worker-discovery"
    worker_optimization  = "cloudoptix-worker-optimization"
    worker_automation    = "cloudoptix-worker-automation"
    worker_validation    = "cloudoptix-worker-validation"
    worker_notification  = "cloudoptix-worker-notification"
  }
}

variable "customer_role_arn_pattern" {
  description = <<-EOT
    Wildcard ARN pattern matching every onboarding role a customer can create
    (see terraform/modules/cloudoptix-onboarding-role — role names are always
    "CloudOptix-<tenant-slug>-<Read|Analyze|Plan|Execute>"). Scoping
    sts:AssumeRole to this pattern rather than "*" means a compromised
    CloudOptix pod identity still cannot assume an unrelated role a customer
    happens to also trust the account for.
  EOT
  type    = string
  default = "arn:aws:iam::*:role/CloudOptix-*"
}

variable "secrets" {
  description = <<-EOT
    Names of the Secrets Manager entries this module creates as empty
    containers. Values are injected out-of-band (by an operator, a bootstrap
    script, or an external secrets sync — see helm/cloudoptix's ExternalSecrets
    support) specifically so `terraform apply` never has a secret value in its
    plan, state diff, or this repository. See the README for why the resource
    uses lifecycle.ignore_changes on secret_string.
  EOT
  type    = list(string)
  default = [
    "database-password",
    "redis-password",
    "aws-assume-role-external-id",
    "llm-api-key",
    "auth-service-token-secret",
  ]
}

variable "waf_rate_limit_per_5min" {
  description = "Requests from a single IP per 5-minute window before the WAF rate-based rule blocks it."
  type        = number
  default     = 3000
}

variable "tags" {
  type    = map(string)
  default = {}
}
