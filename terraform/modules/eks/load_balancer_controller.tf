# aws-load-balancer-controller — the operator that turns helm/cloudoptix's
# Ingress object into a real ALB, and (as a side effect) is why
# terraform/modules/network tags public subnets kubernetes.io/role/elb and
# private subnets kubernetes.io/role/internal-elb.
#
# The IAM policy document in policies/aws-load-balancer-controller-iam-policy.json
# is AWS's own published policy for this controller (see the README for how
# to refresh it — AWS revises it occasionally as the controller gains
# features). Vendoring it as a file, rather than reconstructing it action by
# action, is deliberate: an under-scoped hand-rolled version is a worse
# failure mode (an ALB silently fails to reconcile) than a slightly wider
# vendored one that matches what upstream actually ships and tests against.

resource "aws_iam_role" "aws_load_balancer_controller" {
  count = var.install_aws_load_balancer_controller ? 1 : 0
  name  = "${var.name}-aws-lb-controller"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Federated = aws_iam_openid_connect_provider.this.arn }
      Action    = "sts:AssumeRoleWithWebIdentity"
      Condition = {
        StringEquals = {
          "${replace(aws_iam_openid_connect_provider.this.url, "https://", "")}:sub" = "system:serviceaccount:kube-system:aws-load-balancer-controller"
          "${replace(aws_iam_openid_connect_provider.this.url, "https://", "")}:aud" = "sts.amazonaws.com"
        }
      }
    }]
  })
  tags = local.common_tags
}

resource "aws_iam_policy" "aws_load_balancer_controller" {
  count  = var.install_aws_load_balancer_controller ? 1 : 0
  name   = "${var.name}-aws-lb-controller"
  policy = file("${path.module}/policies/aws-load-balancer-controller-iam-policy.json")
}

resource "aws_iam_role_policy_attachment" "aws_load_balancer_controller" {
  count      = var.install_aws_load_balancer_controller ? 1 : 0
  role       = aws_iam_role.aws_load_balancer_controller[0].name
  policy_arn = aws_iam_policy.aws_load_balancer_controller[0].arn
}

resource "helm_release" "aws_load_balancer_controller" {
  count      = var.install_aws_load_balancer_controller ? 1 : 0
  name       = "aws-load-balancer-controller"
  namespace  = "kube-system"
  repository = "https://aws.github.io/eks-charts"
  chart      = "aws-load-balancer-controller"
  version    = var.aws_load_balancer_controller_version

  set {
    name  = "clusterName"
    value = aws_eks_cluster.this.name
  }
  set {
    name  = "serviceAccount.create"
    value = "true"
  }
  set {
    name  = "serviceAccount.name"
    value = "aws-load-balancer-controller"
  }
  set {
    name  = "serviceAccount.annotations.eks\\.amazonaws\\.com/role-arn"
    value = aws_iam_role.aws_load_balancer_controller[0].arn
  }
  set {
    name  = "region"
    value = data.aws_region.current.name
  }
  set {
    name  = "vpcId"
    value = var.vpc_id
  }

  depends_on = [
    aws_eks_node_group.system,
    aws_iam_role_policy_attachment.aws_load_balancer_controller,
  ]
}
