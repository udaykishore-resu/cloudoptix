output "vpc_id" {
  value = module.network.vpc_id
}

output "stopped_instance_id" {
  description = "Run `aws ec2 stop-instances --instance-ids $(terraform output -raw stopped_instance_id)` once after apply — see aws_instance.stopped's comment."
  value       = aws_instance.stopped.id
}

output "no_lifecycle_bucket_name" {
  value = aws_s3_bucket.no_lifecycle.id
}

output "rds_primary_endpoint" {
  value = aws_db_instance.oversized_primary.endpoint
}

output "eks_cluster_name" {
  value = var.enable_eks ? module.eks[0].cluster_name : null
}

output "eks_kubeconfig_command" {
  description = "Once applied, fetch cluster credentials and apply manifests/oversized-pod-requests.yaml to complete the EKS pathology."
  value       = var.enable_eks ? "aws eks update-kubeconfig --name ${module.eks[0].cluster_name} --region ${var.region} && kubectl apply -f manifests/oversized-pod-requests.yaml" : null
}

output "estimated_monthly_cost_note" {
  value = "See README.md's cost warning — roughly $180-260/month at default sizes with enable_eks and enable_cross_az_pair both true. Run terraform destroy when done."
}
