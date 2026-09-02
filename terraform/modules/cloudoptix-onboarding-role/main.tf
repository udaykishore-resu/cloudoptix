# ---------------------------------------------------------------------------
# This module's action lists are a deliberate, exact mirror of
# internal/application/onboarding/iam.go — the Go source CloudOptix's own
# onboarding flow uses to render the identical policy documents in-app. Keep
# them in sync by diffing against that file's scopeReadActions,
# scopeAnalyzeActions, scopePlanActions and executeActionsByType whenever
# either changes; a drift between what this module grants and what the
# application actually calls is either a customer support ticket ("why does
# CloudOptix say AccessDenied") or a security review question ("why does
# this role have an action CloudOptix's onboarding page didn't mention") —
# both are the same underlying bug.
# ---------------------------------------------------------------------------

locals {
  common_tags = merge(var.tags, {
    ManagedBy      = "terraform"
    CloudOptixRole = "onboarding"
    Tenant         = var.tenant_slug
  })

  # Mirrors iam.go's scopeReadActions.
  read_actions = [
    "ec2:Describe*", "rds:Describe*", "rds:ListTagsForResource",
    "s3:ListAllMyBuckets", "s3:GetBucketLocation", "s3:GetBucketTagging", "s3:GetBucketLifecycleConfiguration",
    "lambda:List*", "lambda:GetFunction*",
    "ecs:Describe*", "ecs:List*",
    "eks:Describe*", "eks:List*",
    "elasticloadbalancing:Describe*",
    "autoscaling:Describe*",
    "dynamodb:Describe*", "dynamodb:List*",
    "elasticache:Describe*",
    "tag:GetResources", "tag:GetTagKeys", "tag:GetTagValues",
    "organizations:ListAccounts", "organizations:DescribeOrganization",
  ]

  # Mirrors iam.go's scopeAnalyzeActions. The "analyze" role grants
  # read_actions + analyze_actions (buildPolicyDocument's ScopeAnalyze case).
  analyze_actions = [
    "ce:GetCostAndUsage", "ce:GetCostForecast", "ce:GetReservationUtilization",
    "ce:GetSavingsPlansUtilization", "ce:GetRightsizingRecommendation",
    "cur:DescribeReportDefinitions",
    "cloudwatch:GetMetricData", "cloudwatch:GetMetricStatistics", "cloudwatch:ListMetrics",
    "pricing:GetProducts", "pricing:DescribeServices",
    "compute-optimizer:Get*",
  ]

  # Mirrors iam.go's scopePlanActions. The "plan" role grants
  # read_actions + plan_actions (buildPolicyDocument's ScopePlan case) — every
  # entry here is read-only or DryRun-only; CloudOptix's plan/simulate stage
  # never calls a mutating API without DryRun set.
  plan_actions = [
    "ec2:DescribeInstanceAttribute",
    "pricing:GetProducts",
    "compute-optimizer:GetEC2InstanceRecommendations",
    "compute-optimizer:GetRDSDatabaseRecommendations",
  ]

  # Mirrors the deduplicated union of iam.go's executeActionsByType — every
  # narrow mutating action any executor in internal/adapters/aws/executor
  # (plus the two actions logs_unsupported.go documents but does not yet
  # implement, and the two commitment-purchase actions
  # internal/application/economics drives) can call. See this module's
  # README for the full per-action-type breakdown a security reviewer would
  # want, rather than just the flat union IAM actually enforces.
  execute_actions = [
    "ec2:ModifyInstanceAttribute", "ec2:StopInstances", "ec2:StartInstances", "ec2:TerminateInstances",
    "ec2:DeleteVolume", "ec2:ModifyVolume", "ec2:DeleteSnapshot", "ec2:DeregisterImage",
    "ec2:ReleaseAddress", "ec2:CreateFleet", "ec2:CreateVpcEndpoint", "ec2:DeleteNatGateway",
    "rds:ModifyDBInstance", "rds:DeleteDBInstance", "rds:StopDBInstance", "rds:StartDBInstance",
    "s3:PutBucketLifecycleConfiguration", "s3:AbortMultipartUpload",
    "s3:ListMultipartUploadParts", "s3:ListBucketMultipartUploads",
    "logs:PutRetentionPolicy",
    "lambda:UpdateFunctionConfiguration", "lambda:DeleteProvisionedConcurrencyConfig",
    "eks:UpdateNodegroupConfig", "eks:DescribeCluster", "autoscaling:UpdateAutoScalingGroup",
    "ce:GetSavingsPlansPurchaseRecommendation", "savingsplans:CreateSavingsPlan",
    "dynamodb:UpdateTable",
  ]

  scope_actions = {
    read    = local.read_actions
    analyze = concat(local.read_actions, local.analyze_actions)
    plan    = concat(local.read_actions, local.plan_actions)
    execute = local.execute_actions
  }

  scope_title = {
    read    = "Read"
    analyze = "Analyze"
    plan    = "Plan"
    execute = "Execute"
  }
}

data "aws_iam_policy_document" "trust" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRole"]

    principals {
      type        = "AWS"
      identifiers = [var.cloudoptix_principal_arn]
    }

    condition {
      test     = "StringEquals"
      variable = "sts:ExternalId"
      values   = [var.external_id]
    }
  }
}

data "aws_iam_policy_document" "scope" {
  for_each = var.enabled_scopes

  statement {
    sid       = "CloudOptix${local.scope_title[each.value]}"
    effect    = "Allow"
    actions   = local.scope_actions[each.value]
    resources = ["*"] # CloudOptix does not know your resource ARNs until discovery — which runs under this very role — has found them; every action above is itself already narrow.
  }
}

resource "aws_iam_role" "this" {
  for_each = var.enabled_scopes

  name                 = "CloudOptix-${var.tenant_slug}-${local.scope_title[each.value]}"
  description          = "CloudOptix ${each.value}-tier onboarding role. Provisioned by terraform/modules/cloudoptix-onboarding-role — see that module's README for exactly what each action is for."
  assume_role_policy   = data.aws_iam_policy_document.trust.json
  max_session_duration = var.max_session_duration_seconds
  permissions_boundary = var.permissions_boundary_arn

  tags = merge(local.common_tags, { Scope = each.value })
}

resource "aws_iam_role_policy" "this" {
  for_each = var.enabled_scopes
  name     = "CloudOptix-${var.tenant_slug}-${local.scope_title[each.value]}"
  role     = aws_iam_role.this[each.value].id
  policy   = data.aws_iam_policy_document.scope[each.value].json
}
