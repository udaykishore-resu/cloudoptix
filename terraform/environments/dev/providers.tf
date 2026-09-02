provider "aws" {
  region = var.region

  default_tags {
    tags = {
      Project     = "cloudoptix"
      Environment = "dev"
      ManagedBy   = "terraform"
    }
  }
}

# Authenticates to the cluster module.eks just created using a short-lived
# exec-plugin token (aws eks get-token) rather than a long-lived static
# kubeconfig credential — the same "no static credential" preference
# internal/adapters/aws/sts's doc comment enforces for the app's own AWS
# access.
data "aws_eks_cluster_auth" "this" {
  name = module.eks.cluster_name
}

provider "kubernetes" {
  host                   = module.eks.cluster_endpoint
  cluster_ca_certificate = base64decode(module.eks.cluster_certificate_authority_data)
  token                  = data.aws_eks_cluster_auth.this.token
}

provider "helm" {
  kubernetes {
    host                   = module.eks.cluster_endpoint
    cluster_ca_certificate = base64decode(module.eks.cluster_certificate_authority_data)
    token                  = data.aws_eks_cluster_auth.this.token
  }
}
