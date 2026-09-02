locals {
  common_tags = merge(var.tags, {
    Module      = "security"
    Environment = var.environment
    ManagedBy   = "terraform"
  })
}

data "aws_caller_identity" "current" {}

# ---------------------------------------------------------------------------
# KMS
# ---------------------------------------------------------------------------

# One key for "application data at rest" — RDS, ElastiCache, the artefacts
# and CUR-ingestion S3 buckets, Secrets Manager. Sharing a key across these
# is a deliberate simplification: they are all restorable/rotatable
# application state with the same blast radius if the key were ever
# disabled. The audit bucket gets its own key (below) because that data is
# retained under object lock specifically so it survives even an incident
# that compromises this key's usual principals.
resource "aws_kms_key" "app" {
  description             = "${var.name} application data at rest (RDS, Redis, S3 artefacts/CUR, Secrets Manager)."
  deletion_window_in_days = var.kms_key_deletion_window_days
  enable_key_rotation     = true
  tags                    = merge(local.common_tags, { Name = "${var.name}-app" })
}

resource "aws_kms_alias" "app" {
  name          = "alias/${var.name}-app"
  target_key_id = aws_kms_key.app.key_id
}

# Separate key for the audit archive bucket (object-lock retained — see
# terraform/modules/storage). Kept distinct so a key-policy mistake or a
# necessary emergency key disablement on the application key during an
# incident can never simultaneously take out the evidence trail of that
# same incident.
resource "aws_kms_key" "audit" {
  description             = "${var.name} audit archive (object-lock retained S3 bucket)."
  deletion_window_in_days = var.kms_key_deletion_window_days
  enable_key_rotation     = true
  tags                    = merge(local.common_tags, { Name = "${var.name}-audit" })
}

resource "aws_kms_alias" "audit" {
  name          = "alias/${var.name}-audit"
  target_key_id = aws_kms_key.audit.key_id
}

# ---------------------------------------------------------------------------
# Secrets Manager — empty containers. See the `secrets` variable and the
# README for why values are never set here.
# ---------------------------------------------------------------------------

resource "aws_secretsmanager_secret" "this" {
  for_each   = toset(var.secrets)
  name       = "${var.name}/${each.value}"
  kms_key_id = aws_kms_key.app.arn
  tags       = local.common_tags
}

resource "aws_secretsmanager_secret_version" "placeholder" {
  for_each      = toset(var.secrets)
  secret_id     = aws_secretsmanager_secret.this[each.value].id
  secret_string = "REPLACE_ME_OUT_OF_BAND"

  # terraform apply must never be the thing that writes (or overwrites) a
  # real secret value into state or a plan diff. The placeholder above only
  # exists so the secret has *a* version (required before it can be read);
  # ignore_changes means an operator's or the bootstrap script's later
  # `aws secretsmanager put-secret-value` is never reverted by the next
  # apply, and never shows up as a Terraform-visible diff either.
  lifecycle {
    ignore_changes = [secret_string]
  }
}

# ---------------------------------------------------------------------------
# IRSA roles — one per CloudOptix component (see the service_accounts
# variable). Every role gets the same three permission groups: read+decrypt
# its own secrets, assume any customer onboarding role, and emit its own
# telemetry. None of that requires per-component variation today, but the
# roles themselves are still split so CloudTrail/Access Analyzer can tell
# components apart, and so a future component that needs something extra
# (e.g. the automation worker eventually getting write access to a
# CloudOptix-owned resource) grows its own policy without touching the rest.
# ---------------------------------------------------------------------------

data "aws_iam_policy_document" "irsa_trust" {
  for_each = var.service_accounts

  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRoleWithWebIdentity"]

    principals {
      type        = "Federated"
      identifiers = [var.eks_oidc_provider_arn]
    }

    condition {
      test     = "StringEquals"
      variable = "${var.eks_oidc_provider_url}:sub"
      values   = ["system:serviceaccount:${var.namespace}:${each.value}"]
    }
    condition {
      test     = "StringEquals"
      variable = "${var.eks_oidc_provider_url}:aud"
      values   = ["sts.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "component" {
  for_each           = var.service_accounts
  name               = "${var.name}-${each.key}"
  assume_role_policy = data.aws_iam_policy_document.irsa_trust[each.key].json
  tags               = merge(local.common_tags, { Component = each.key })
}

data "aws_iam_policy_document" "component" {
  for_each = var.service_accounts

  statement {
    sid       = "ReadOwnSecrets"
    effect    = "Allow"
    actions   = ["secretsmanager:GetSecretValue", "secretsmanager:DescribeSecret"]
    resources = [for s in var.secrets : "${aws_secretsmanager_secret.this[s].arn}"]
  }

  statement {
    sid       = "DecryptAppData"
    effect    = "Allow"
    actions   = ["kms:Decrypt", "kms:DescribeKey"]
    resources = [aws_kms_key.app.arn]
  }

  # This is the credential broker's launch pad: internal/adapters/aws/sts.Broker
  # calls sts:AssumeRole against a role ARN read from cloud.AWSAccount.RoleARNs,
  # which is only ever one of the four onboarding-role scopes a customer
  # created via terraform/modules/cloudoptix-onboarding-role. Restricting the
  # resource to that naming pattern (rather than "*") means this pod identity
  # cannot be used to assume an unrelated role even if a customer account
  # happened to also trust the CloudOptix platform account for something else.
  statement {
    sid       = "AssumeCustomerOnboardingRoles"
    effect    = "Allow"
    actions   = ["sts:AssumeRole"]
    resources = [var.customer_role_arn_pattern]
  }
}

resource "aws_iam_role_policy" "component" {
  for_each = var.service_accounts
  name     = "${var.name}-${each.key}"
  role     = aws_iam_role.component[each.key].id
  policy   = data.aws_iam_policy_document.component[each.key].json
}

# ---------------------------------------------------------------------------
# WAF — attached to the public ALB the aws-load-balancer-controller creates
# for the chart's Ingress (see helm/cloudoptix/values.yaml's
# ingress.wafAclArn, which the Ingress annotation reads). Managed rule
# groups only: this platform's attack surface is a JSON API behind OIDC/JWT
# auth, not a template-rendering surface that benefits from custom rules
# today, and managed rules stay current with AWS's own threat intel without
# CloudOptix maintaining signatures.
# ---------------------------------------------------------------------------

resource "aws_wafv2_web_acl" "this" {
  name        = "${var.name}-waf"
  description = "Public ALB protection for the CloudOptix API."
  scope       = "REGIONAL"

  default_action {
    allow {}
  }

  rule {
    name     = "AWSManagedRulesCommonRuleSet"
    priority = 0
    override_action {
      none {}
    }
    statement {
      managed_rule_group_statement {
        name        = "AWSManagedRulesCommonRuleSet"
        vendor_name = "AWS"
      }
    }
    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "${var.name}-common-rule-set"
      sampled_requests_enabled   = true
    }
  }

  rule {
    name     = "AWSManagedRulesKnownBadInputsRuleSet"
    priority = 1
    override_action {
      none {}
    }
    statement {
      managed_rule_group_statement {
        name        = "AWSManagedRulesKnownBadInputsRuleSet"
        vendor_name = "AWS"
      }
    }
    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "${var.name}-known-bad-inputs"
      sampled_requests_enabled   = true
    }
  }

  rule {
    name     = "AWSManagedRulesSQLiRuleSet"
    priority = 2
    override_action {
      none {}
    }
    statement {
      managed_rule_group_statement {
        name        = "AWSManagedRulesSQLiRuleSet"
        vendor_name = "AWS"
      }
    }
    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "${var.name}-sqli"
      sampled_requests_enabled   = true
    }
  }

  # Independent of auth.go's own per-tenant/per-token rate limiting inside
  # the app — this catches volumetric abuse from a single IP before it even
  # reaches a pod, which the app-level limiter (keyed on tenant/token) can't
  # see if the abuse is unauthenticated traffic hammering /api/v1/onboarding.
  rule {
    name     = "RateLimitPerIP"
    priority = 3
    action {
      block {}
    }
    statement {
      rate_based_statement {
        limit              = var.waf_rate_limit_per_5min
        aggregate_key_type = "IP"
      }
    }
    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "${var.name}-rate-limit"
      sampled_requests_enabled   = true
    }
  }

  visibility_config {
    cloudwatch_metrics_enabled = true
    metric_name                = "${var.name}-waf"
    sampled_requests_enabled   = true
  }

  tags = local.common_tags
}
