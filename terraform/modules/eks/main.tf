locals {
  common_tags = merge(var.tags, {
    Module      = "eks"
    Environment = var.environment
    ManagedBy   = "terraform"
  })
  cluster_subnet_ids = concat(var.private_subnet_ids, var.public_subnet_ids)

  # cluster-autoscaler's autoDiscovery mode finds node groups purely by
  # these two tags on the underlying ASG (which EKS managed node groups
  # propagate tags to automatically) — Karpenter needs neither, since it
  # provisions instances directly rather than scaling an ASG.
  cluster_autoscaler_discovery_tags = var.autoscaler == "cluster-autoscaler" ? {
    "k8s.io/cluster-autoscaler/${var.name}" = "owned"
    "k8s.io/cluster-autoscaler/enabled"     = "true"
  } : {}
}

data "aws_partition" "current" {}
data "aws_region" "current" {}

# ---------------------------------------------------------------------------
# Cluster IAM role
# ---------------------------------------------------------------------------

resource "aws_iam_role" "cluster" {
  name = "${var.name}-eks-cluster"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "eks.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
  tags = local.common_tags
}

resource "aws_iam_role_policy_attachment" "cluster_policy" {
  role       = aws_iam_role.cluster.name
  policy_arn = "arn:${data.aws_partition.current.partition}:iam::aws:policy/AmazonEKSClusterPolicy"
}

# ---------------------------------------------------------------------------
# Cluster
# ---------------------------------------------------------------------------

resource "aws_eks_cluster" "this" {
  name     = var.name
  role_arn = aws_iam_role.cluster.arn
  version  = var.kubernetes_version

  vpc_config {
    subnet_ids              = local.cluster_subnet_ids
    endpoint_private_access = true
    endpoint_public_access  = var.endpoint_public_access
    public_access_cidrs     = var.endpoint_public_access ? var.endpoint_public_access_cidrs : null
  }

  enabled_cluster_log_types = var.cluster_log_types

  access_config {
    authentication_mode = "API_AND_CONFIG_MAP" # EKS access entries for humans/CI, aws-auth ConfigMap kept working for anything not yet migrated
  }

  tags = local.common_tags

  depends_on = [aws_iam_role_policy_attachment.cluster_policy]
}

# ---------------------------------------------------------------------------
# IRSA trust anchor — every per-component role in the security module, and
# every controller role below, trusts this OIDC provider.
# ---------------------------------------------------------------------------

data "tls_certificate" "cluster_oidc" {
  url = aws_eks_cluster.this.identity[0].oidc[0].issuer
}

resource "aws_iam_openid_connect_provider" "this" {
  url             = aws_eks_cluster.this.identity[0].oidc[0].issuer
  client_id_list  = ["sts.amazonaws.com"]
  thumbprint_list = [data.tls_certificate.cluster_oidc.certificates[0].sha1_fingerprint]
  tags            = local.common_tags
}

# ---------------------------------------------------------------------------
# Node IAM role — shared by both managed node groups. The three attached
# policies are the documented minimum for a worker node to join the cluster,
# run the VPC CNI, and pull images; SSM is added on top so an operator can
# reach a node via Session Manager instead of needing an SSH bastion in the
# public subnet.
# ---------------------------------------------------------------------------

resource "aws_iam_role" "node" {
  name = "${var.name}-eks-node"
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

resource "aws_iam_role_policy_attachment" "node" {
  for_each = toset([
    "AmazonEKSWorkerNodePolicy",
    "AmazonEKS_CNI_Policy",
    "AmazonEC2ContainerRegistryReadOnly",
    "AmazonSSMManagedInstanceCore",
  ])
  role       = aws_iam_role.node.name
  policy_arn = "arn:${data.aws_partition.current.partition}:iam::aws:policy/${each.value}"
}

# ---------------------------------------------------------------------------
# Managed node groups: one On-Demand "system" group for anything that must
# not be interrupted mid-work, one Spot "workers" group sized for the
# stateless, retryable discovery/optimization/validation/notification
# workers this platform runs. See spot_instance_types' doc comment for why
# Spot is the deliberate default for the worker fleet, not just the cheap
# option.
# ---------------------------------------------------------------------------

resource "aws_eks_node_group" "system" {
  cluster_name    = aws_eks_cluster.this.name
  node_group_name = "${var.name}-system"
  node_role_arn   = aws_iam_role.node.arn
  subnet_ids      = var.private_subnet_ids

  capacity_type  = "ON_DEMAND"
  instance_types = var.on_demand_instance_types
  disk_size      = var.node_disk_size_gb

  scaling_config {
    min_size     = var.on_demand_min_size
    max_size     = var.on_demand_max_size
    desired_size = var.on_demand_desired_size
  }

  update_config {
    max_unavailable_percentage = 33
  }

  labels = {
    "cloudoptix.io/workload-class" = "system"
  }

  tags = merge(local.common_tags, local.cluster_autoscaler_discovery_tags, {
    # Karpenter's own auto-discovery convention: tagging the node group's
    # subnets/security groups this way lets a Karpenter EC2NodeClass select
    # them without hard-coding IDs — see autoscaling.tf.
    "karpenter.sh/discovery" = var.name
  })

  depends_on = [aws_iam_role_policy_attachment.node]

  lifecycle {
    ignore_changes = [scaling_config[0].desired_size] # let the autoscaler own desired_size after initial creation
  }
}

resource "aws_eks_node_group" "spot" {
  cluster_name    = aws_eks_cluster.this.name
  node_group_name = "${var.name}-spot"
  node_role_arn   = aws_iam_role.node.arn
  subnet_ids      = var.private_subnet_ids

  capacity_type  = "SPOT"
  instance_types = var.spot_instance_types
  disk_size      = var.node_disk_size_gb

  scaling_config {
    min_size     = var.spot_min_size
    max_size     = var.spot_max_size
    desired_size = var.spot_desired_size
  }

  update_config {
    max_unavailable_percentage = 50 # Spot workloads are already interruption-tolerant; a faster rollout is fine
  }

  labels = {
    "cloudoptix.io/workload-class" = "spot"
  }

  taint {
    key    = "cloudoptix.io/spot"
    value  = "true"
    effect = "NO_SCHEDULE"
  }

  tags = merge(local.common_tags, local.cluster_autoscaler_discovery_tags, {
    "karpenter.sh/discovery" = var.name
  })

  depends_on = [aws_iam_role_policy_attachment.node]

  lifecycle {
    ignore_changes = [scaling_config[0].desired_size]
  }
}

# ---------------------------------------------------------------------------
# EKS-managed addons
# ---------------------------------------------------------------------------

resource "aws_eks_addon" "vpc_cni" {
  cluster_name                = aws_eks_cluster.this.name
  addon_name                  = "vpc-cni"
  addon_version               = var.addons.vpc_cni != "" ? var.addons.vpc_cni : null
  resolve_conflicts_on_update = "OVERWRITE"
  tags                        = local.common_tags
}

resource "aws_eks_addon" "coredns" {
  cluster_name                = aws_eks_cluster.this.name
  addon_name                  = "coredns"
  addon_version               = var.addons.coredns != "" ? var.addons.coredns : null
  resolve_conflicts_on_update = "OVERWRITE"
  tags                        = local.common_tags
  depends_on                  = [aws_eks_node_group.system]
}

resource "aws_eks_addon" "kube_proxy" {
  cluster_name                = aws_eks_cluster.this.name
  addon_name                  = "kube-proxy"
  addon_version               = var.addons.kube_proxy != "" ? var.addons.kube_proxy : null
  resolve_conflicts_on_update = "OVERWRITE"
  tags                        = local.common_tags
}

resource "aws_iam_role" "ebs_csi" {
  name = "${var.name}-ebs-csi"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Federated = aws_iam_openid_connect_provider.this.arn }
      Action    = "sts:AssumeRoleWithWebIdentity"
      Condition = {
        StringEquals = {
          "${replace(aws_iam_openid_connect_provider.this.url, "https://", "")}:sub" = "system:serviceaccount:kube-system:ebs-csi-controller-sa"
          "${replace(aws_iam_openid_connect_provider.this.url, "https://", "")}:aud" = "sts.amazonaws.com"
        }
      }
    }]
  })
  tags = local.common_tags
}

resource "aws_iam_role_policy_attachment" "ebs_csi" {
  role       = aws_iam_role.ebs_csi.name
  policy_arn = "arn:${data.aws_partition.current.partition}:iam::aws:policy/service-role/AmazonEBSCSIDriverPolicy"
}

resource "aws_eks_addon" "ebs_csi" {
  cluster_name                = aws_eks_cluster.this.name
  addon_name                  = "aws-ebs-csi-driver"
  addon_version               = var.addons.ebs_csi != "" ? var.addons.ebs_csi : null
  service_account_role_arn    = aws_iam_role.ebs_csi.arn
  resolve_conflicts_on_update = "OVERWRITE"
  tags                        = local.common_tags
  depends_on                  = [aws_eks_node_group.system]
}
