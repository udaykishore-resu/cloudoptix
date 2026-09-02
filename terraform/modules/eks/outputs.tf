output "cluster_name" {
  value = aws_eks_cluster.this.name
}

output "cluster_arn" {
  value = aws_eks_cluster.this.arn
}

output "cluster_endpoint" {
  value = aws_eks_cluster.this.endpoint
}

output "cluster_certificate_authority_data" {
  value = aws_eks_cluster.this.certificate_authority[0].data
}

output "cluster_version" {
  value = aws_eks_cluster.this.version
}

output "oidc_provider_arn" {
  description = "Pass to the security module's eks_oidc_provider_arn."
  value       = aws_iam_openid_connect_provider.this.arn
}

output "oidc_provider_url" {
  description = "Pass to the security module's eks_oidc_provider_url (without the https:// prefix, already stripped here)."
  value       = replace(aws_iam_openid_connect_provider.this.url, "https://", "")
}

output "node_security_group_id" {
  description = "EKS's own cluster-managed security group — the one to pass as rds/redis's allowed_security_group_ids so the database and cache tiers accept traffic from cluster workloads."
  value       = aws_eks_cluster.this.vpc_config[0].cluster_security_group_id
}

output "node_role_arn" {
  value = aws_iam_role.node.arn
}

output "system_node_group_name" {
  value = aws_eks_node_group.system.node_group_name
}

output "spot_node_group_name" {
  value = aws_eks_node_group.spot.node_group_name
}
