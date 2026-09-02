# Karpenter and Cluster Autoscaler are mutually exclusive (var.autoscaler
# picks one) — see the variable's doc comment for the trade-off. Both blocks
# below are gated on that value, so an environment only ever pays for, and
# only ever has to reason about, one autoscaling model.

# ---------------------------------------------------------------------------
# Karpenter
# ---------------------------------------------------------------------------

locals {
  use_karpenter = var.autoscaler == "karpenter"
}

# Karpenter-provisioned nodes assume this role directly (not the shared
# aws_iam_role.node the managed node groups use) via an instance profile,
# because Karpenter creates and terminates EC2 instances itself rather than
# going through an ASG/node group.
resource "aws_iam_role" "karpenter_node" {
  count = local.use_karpenter ? 1 : 0
  name  = "${var.name}-karpenter-node"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ec2.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
  tags = local.common_tags
}

resource "aws_iam_role_policy_attachment" "karpenter_node" {
  for_each = local.use_karpenter ? toset([
    "AmazonEKSWorkerNodePolicy", "AmazonEKS_CNI_Policy",
    "AmazonEC2ContainerRegistryReadOnly", "AmazonSSMManagedInstanceCore",
  ]) : toset([])
  role       = aws_iam_role.karpenter_node[0].name
  policy_arn = "arn:${data.aws_partition.current.partition}:iam::aws:policy/${each.value}"
}

resource "aws_iam_instance_profile" "karpenter_node" {
  count = local.use_karpenter ? 1 : 0
  name  = "${var.name}-karpenter-node"
  role  = aws_iam_role.karpenter_node[0].name
  tags  = local.common_tags
}

# The controller's IRSA role: what actually calls ec2:RunInstances/
# TerminateInstances/CreateFleet on Karpenter's behalf when it decides new
# capacity is needed or existing capacity should consolidate/drain.
resource "aws_iam_role" "karpenter_controller" {
  count = local.use_karpenter ? 1 : 0
  name  = "${var.name}-karpenter-controller"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Federated = aws_iam_openid_connect_provider.this.arn }
      Action    = "sts:AssumeRoleWithWebIdentity"
      Condition = {
        StringEquals = {
          "${replace(aws_iam_openid_connect_provider.this.url, "https://", "")}:sub" = "system:serviceaccount:kube-system:karpenter"
          "${replace(aws_iam_openid_connect_provider.this.url, "https://", "")}:aud" = "sts.amazonaws.com"
        }
      }
    }]
  })
  tags = local.common_tags
}

data "aws_iam_policy_document" "karpenter_controller" {
  count = local.use_karpenter ? 1 : 0

  statement {
    sid    = "AllowScopedEC2InstanceActions"
    effect = "Allow"
    actions = [
      "ec2:RunInstances", "ec2:CreateFleet", "ec2:CreateLaunchTemplate",
      "ec2:CreateTags", "ec2:TerminateInstances",
    ]
    resources = ["*"]
  }
  statement {
    sid    = "AllowScopedInstanceProfileActions"
    effect = "Allow"
    actions = [
      "iam:PassRole",
    ]
    resources = [aws_iam_role.karpenter_node[0].arn]
  }
  statement {
    sid    = "AllowInstanceProfileReadActions"
    effect = "Allow"
    actions = [
      "iam:GetInstanceProfile", "iam:CreateInstanceProfile", "iam:TagInstanceProfile",
      "iam:AddRoleToInstanceProfile", "iam:RemoveRoleFromInstanceProfile", "iam:DeleteInstanceProfile",
    ]
    resources = ["*"]
  }
  statement {
    sid    = "AllowDescribeActions"
    effect = "Allow"
    actions = [
      "ec2:DescribeInstances", "ec2:DescribeImages", "ec2:DescribeInstanceTypes",
      "ec2:DescribeInstanceTypeOfferings", "ec2:DescribeAvailabilityZones",
      "ec2:DescribeLaunchTemplates", "ec2:DescribeSpotPriceHistory", "ec2:DescribeSubnets",
      "ec2:DescribeSecurityGroups", "pricing:GetProducts", "ssm:GetParameter",
    ]
    resources = ["*"]
  }
  statement {
    sid       = "AllowInterruptionQueueActions"
    effect    = "Allow"
    actions   = ["sqs:DeleteMessage", "sqs:GetQueueUrl", "sqs:ReceiveMessage"]
    resources = [aws_sqs_queue.karpenter_interruption[0].arn]
  }
  statement {
    sid       = "AllowEKSClusterRead"
    effect    = "Allow"
    actions   = ["eks:DescribeCluster"]
    resources = [aws_eks_cluster.this.arn]
  }
}

resource "aws_iam_role_policy" "karpenter_controller" {
  count  = local.use_karpenter ? 1 : 0
  name   = "${var.name}-karpenter-controller"
  role   = aws_iam_role.karpenter_controller[0].id
  policy = data.aws_iam_policy_document.karpenter_controller[0].json
}

# Spot Instance interruption notices (and Scheduled Change / Rebalance
# Recommendation events) land here; Karpenter drains the node gracefully
# ahead of actual termination instead of the workload simply losing its
# node with 2 minutes' notice and no warning to the scheduler.
resource "aws_sqs_queue" "karpenter_interruption" {
  count                     = local.use_karpenter ? 1 : 0
  name                      = "${var.name}-karpenter-interruption"
  message_retention_seconds = 300
  tags                      = local.common_tags
}

resource "aws_sqs_queue_policy" "karpenter_interruption" {
  count     = local.use_karpenter ? 1 : 0
  queue_url = aws_sqs_queue.karpenter_interruption[0].id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = ["events.amazonaws.com", "sqs.amazonaws.com"] }
      Action    = "sqs:SendMessage"
      Resource  = aws_sqs_queue.karpenter_interruption[0].arn
    }]
  })
}

resource "aws_cloudwatch_event_rule" "karpenter_spot_interruption" {
  count       = local.use_karpenter ? 1 : 0
  name        = "${var.name}-karpenter-spot-interruption"
  description = "Routes EC2 Spot interruption warnings to Karpenter's interruption queue."
  event_pattern = jsonencode({
    source      = ["aws.ec2"]
    detail-type = ["EC2 Spot Instance Interruption Warning", "EC2 Instance Rebalance Recommendation"]
  })
  tags = local.common_tags
}

resource "aws_cloudwatch_event_target" "karpenter_spot_interruption" {
  count = local.use_karpenter ? 1 : 0
  rule  = aws_cloudwatch_event_rule.karpenter_spot_interruption[0].name
  arn   = aws_sqs_queue.karpenter_interruption[0].arn
}

resource "helm_release" "karpenter" {
  count      = local.use_karpenter ? 1 : 0
  name       = "karpenter"
  namespace  = "kube-system"
  repository = "oci://public.ecr.aws/karpenter"
  chart      = "karpenter"
  version    = var.karpenter_version

  set {
    name  = "settings.clusterName"
    value = aws_eks_cluster.this.name
  }
  set {
    name  = "settings.interruptionQueue"
    value = aws_sqs_queue.karpenter_interruption[0].name
  }
  set {
    name  = "serviceAccount.annotations.eks\\.amazonaws\\.com/role-arn"
    value = aws_iam_role.karpenter_controller[0].arn
  }

  depends_on = [aws_eks_node_group.system]
}

# EC2NodeClass: the AMI/subnet/security-group/instance-profile shape
# Karpenter-provisioned nodes use. Subnets and security groups are
# discovered by the karpenter.sh/discovery tag the managed node groups
# above already carry, rather than hard-coded IDs, so this stays correct if
# the network module's subnets ever change.
resource "kubernetes_manifest" "karpenter_node_class" {
  count = local.use_karpenter ? 1 : 0
  manifest = {
    apiVersion = "karpenter.k8s.aws/v1"
    kind       = "EC2NodeClass"
    metadata   = { name = "default" }
    spec = {
      amiFamily = "AL2023"
      role      = aws_iam_role.karpenter_node[0].name
      subnetSelectorTerms = [
        { tags = { "karpenter.sh/discovery" = var.name } },
      ]
      securityGroupSelectorTerms = [
        { tags = { "karpenter.sh/discovery" = var.name } },
      ]
      tags = local.common_tags
    }
  }
  depends_on = [helm_release.karpenter]
}

# NodePool: the workload-shape Karpenter provisions against — deliberately
# permissive on instance family/size (so Karpenter can pick the cheapest
# fit) but capped on total resources so a runaway scale-out cannot run
# unbounded. Spot-first with on-demand as the fallback capacity type.
resource "kubernetes_manifest" "karpenter_node_pool" {
  count = local.use_karpenter ? 1 : 0
  manifest = {
    apiVersion = "karpenter.sh/v1"
    kind       = "NodePool"
    metadata   = { name = "default" }
    spec = {
      template = {
        metadata = { labels = { "cloudoptix.io/workload-class" = "karpenter" } }
        spec = {
          nodeClassRef = { group = "karpenter.k8s.aws", kind = "EC2NodeClass", name = "default" }
          requirements = [
            { key = "karpenter.sh/capacity-type", operator = "In", values = ["spot", "on-demand"] },
            { key = "kubernetes.io/arch", operator = "In", values = ["amd64"] },
            { key = "karpenter.k8s.aws/instance-category", operator = "In", values = ["m", "c", "r"] },
            { key = "karpenter.k8s.aws/instance-generation", operator = "Gt", values = ["5"] },
          ]
        }
      }
      limits = { cpu = "100", memory = "400Gi" }
      disruption = {
        consolidationPolicy = "WhenEmptyOrUnderutilized"
        consolidateAfter    = "5m"
      }
    }
  }
  depends_on = [kubernetes_manifest.karpenter_node_class]
}

# ---------------------------------------------------------------------------
# Cluster Autoscaler (the alternative to Karpenter — see var.autoscaler)
# ---------------------------------------------------------------------------

locals {
  use_cluster_autoscaler = var.autoscaler == "cluster-autoscaler"
}

resource "aws_iam_role" "cluster_autoscaler" {
  count = local.use_cluster_autoscaler ? 1 : 0
  name  = "${var.name}-cluster-autoscaler"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Federated = aws_iam_openid_connect_provider.this.arn }
      Action    = "sts:AssumeRoleWithWebIdentity"
      Condition = {
        StringEquals = {
          "${replace(aws_iam_openid_connect_provider.this.url, "https://", "")}:sub" = "system:serviceaccount:kube-system:cluster-autoscaler"
          "${replace(aws_iam_openid_connect_provider.this.url, "https://", "")}:aud" = "sts.amazonaws.com"
        }
      }
    }]
  })
  tags = local.common_tags
}

data "aws_iam_policy_document" "cluster_autoscaler" {
  count = local.use_cluster_autoscaler ? 1 : 0

  statement {
    sid    = "Describe"
    effect = "Allow"
    actions = [
      "autoscaling:DescribeAutoScalingGroups", "autoscaling:DescribeAutoScalingInstances",
      "autoscaling:DescribeLaunchConfigurations", "autoscaling:DescribeTags",
      "ec2:DescribeInstanceTypes", "ec2:DescribeLaunchTemplateVersions",
    ]
    resources = ["*"]
  }
  statement {
    sid    = "ScopedToThisClusterOnly"
    effect = "Allow"
    actions = [
      "autoscaling:SetDesiredCapacity", "autoscaling:TerminateInstanceInAutoScalingGroup",
      "autoscaling:UpdateAutoScalingGroup",
    ]
    resources = ["*"]
    condition {
      test     = "StringEquals"
      variable = "aws:ResourceTag/k8s.io/cluster-autoscaler/${var.name}"
      values   = ["owned"]
    }
  }
}

resource "aws_iam_role_policy" "cluster_autoscaler" {
  count  = local.use_cluster_autoscaler ? 1 : 0
  name   = "${var.name}-cluster-autoscaler"
  role   = aws_iam_role.cluster_autoscaler[0].id
  policy = data.aws_iam_policy_document.cluster_autoscaler[0].json
}

resource "helm_release" "cluster_autoscaler" {
  count      = local.use_cluster_autoscaler ? 1 : 0
  name       = "cluster-autoscaler"
  namespace  = "kube-system"
  repository = "https://kubernetes.github.io/autoscaler"
  chart      = "cluster-autoscaler"
  version    = var.cluster_autoscaler_version

  set {
    name  = "autoDiscovery.clusterName"
    value = aws_eks_cluster.this.name
  }
  set {
    name  = "awsRegion"
    value = data.aws_region.current.name
  }
  set {
    name  = "rbac.serviceAccount.annotations.eks\\.amazonaws\\.com/role-arn"
    value = aws_iam_role.cluster_autoscaler[0].arn
  }
  set {
    name  = "extraArgs.balance-similar-node-groups"
    value = "true"
  }
  set {
    name  = "extraArgs.skip-nodes-with-system-pods"
    value = "false"
  }

  depends_on = [aws_eks_node_group.system]
}
