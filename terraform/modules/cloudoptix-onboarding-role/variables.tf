variable "tenant_slug" {
  description = <<-EOT
    Your CloudOptix tenant identifier, as shown in the CloudOptix onboarding
    screen. Embedded in every role name as "CloudOptix-<tenant_slug>-<Scope>"
    (see internal/application/onboarding/iam.go's roleName), so if you run
    CloudOptix for more than one tenant relationship (e.g. an MSP managing
    several customers under one AWS Organization), each gets distinguishable
    roles in this account's IAM console and CloudTrail.
  EOT
  type = string
  validation {
    condition     = can(regex("^[a-zA-Z0-9-]{1,32}$", var.tenant_slug))
    error_message = "tenant_slug must be 1-32 characters, alphanumeric and hyphens only (it becomes part of an IAM role name)."
  }
}

variable "external_id" {
  description = <<-EOT
    The external ID CloudOptix generated for your account, shown once on the
    onboarding screen (internal/application/onboarding/iam.go's
    generateExternalID — always prefixed "cloudoptix-"). Required on every
    AssumeRole call CloudOptix makes into this account; this is the
    confused-deputy defence from
    https://docs.aws.amazon.com/IAM/latest/UserGuide/id_roles_create_for-user_externalid.html
    — do not accept a role-assumption request that omits it, and never reuse
    an external ID CloudOptix did not generate for you specifically.
  EOT
  type      = string
  sensitive = true
  validation {
    condition     = length(var.external_id) >= 8
    error_message = "external_id looks too short to be a real CloudOptix-issued value — re-copy it from the onboarding screen."
  }
}

variable "cloudoptix_principal_arn" {
  description = <<-EOT
    The AWS principal CloudOptix's platform assumes these roles from. Ask
    your CloudOptix contact for the current value if you were not given one
    on the onboarding screen — do NOT guess an account ID. Defaults to a
    root-account ARN, matching the platform's own default
    (internal/application/onboarding/iam.go's platformPrincipalARN); ask
    CloudOptix whether a narrower, specific-role ARN is available for a
    tighter trust boundary, since trusting a specific role is always
    stronger than trusting the whole account root.
  EOT
  type = string
  validation {
    condition     = can(regex("^arn:aws[a-zA-Z-]*:iam::\\d{12}:(root|role/.+)$", var.cloudoptix_principal_arn))
    error_message = "cloudoptix_principal_arn must be an IAM root or role ARN, e.g. arn:aws:iam::123456789012:root."
  }
}

variable "enabled_scopes" {
  description = <<-EOT
    Which of the four role tiers to create. Every tenant needs "read" at
    minimum (discovery cannot run without it); "analyze" and "plan" are
    additive read-only tiers most tenants grant alongside it. "execute" is
    the only tier that can change anything in this account — grant it only
    once you are ready for CloudOptix's automation to actually act, and even
    then only on recommendations your own policy configuration
    (internal/domain/govern) routes to auto-execute or that a human
    approves. Omitting "execute" here does not disable those features in
    the CloudOptix product; it simply means no execute-scope credential
    exists in this account for the product to use, so every execute action
    fails at the AWS API call, not at CloudOptix's own policy check —
    defense in depth.
  EOT
  type    = set(string)
  default = ["read", "analyze", "plan", "execute"]
  validation {
    condition     = alltrue([for s in var.enabled_scopes : contains(["read", "analyze", "plan", "execute"], s)])
    error_message = "enabled_scopes entries must be one of: read, analyze, plan, execute."
  }
}

variable "max_session_duration_seconds" {
  description = "Upper bound on how long a single assumed session may last. CloudOptix's own broker (internal/adapters/aws/sts.Broker) requests 55-minute sessions and refreshes before expiry, so 3600 (1 hour, AssumeRole's own ceiling for a role not further chained) is generous headroom, not a number CloudOptix needs raised."
  type        = number
  default     = 3600
}

variable "permissions_boundary_arn" {
  description = "Optional: an IAM permissions boundary ARN to attach to every role this module creates, if your account's IAM guardrails require one. Left null (the default) attaches none."
  type        = string
  default     = null
}

variable "tags" {
  type    = map(string)
  default = {}
}
