# Only exercised when enable_eks = true (module.eks then installs Cluster
# Autoscaler via helm_release internally). When enable_eks = false these
# providers are configured but never actually used by any resource, which
# Terraform tolerates fine — provider configuration is only evaluated
# against resources that reference it.

data "aws_eks_cluster_auth" "this" {
  count = var.enable_eks ? 1 : 0
  name  = module.eks[0].cluster_name
}

provider "kubernetes" {
  host                   = var.enable_eks ? module.eks[0].cluster_endpoint : ""
  cluster_ca_certificate = var.enable_eks ? base64decode(module.eks[0].cluster_certificate_authority_data) : ""
  token                  = var.enable_eks ? data.aws_eks_cluster_auth.this[0].token : ""
}

provider "helm" {
  kubernetes {
    host                   = var.enable_eks ? module.eks[0].cluster_endpoint : ""
    cluster_ca_certificate = var.enable_eks ? base64decode(module.eks[0].cluster_certificate_authority_data) : ""
    token                  = var.enable_eks ? data.aws_eks_cluster_auth.this[0].token : ""
  }
}
