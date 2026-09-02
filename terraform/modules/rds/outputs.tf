output "cluster_endpoint" {
  description = "Writer endpoint — set as CLOUDOPTIX_DATABASE_HOST."
  value       = aws_rds_cluster.this.endpoint
}

output "reader_endpoint" {
  description = "Load-balanced reader endpoint, for a future read-replica-aware repository path. Not currently read by any Go adapter — internal/adapters/postgres connects to a single DSN — documented here so that changes."
  value       = aws_rds_cluster.this.reader_endpoint
}

output "port" {
  value = aws_rds_cluster.this.port
}

output "database_name" {
  value = aws_rds_cluster.this.database_name
}

output "master_username" {
  value = aws_rds_cluster.this.master_username
}

output "master_user_secret_arn" {
  description = "ARN of the RDS-managed Secrets Manager secret holding the master password. Sync its `password` field into the chart's database secret via ExternalSecrets — see this module's README."
  value       = aws_rds_cluster.this.master_user_secret[0].secret_arn
}

output "security_group_id" {
  value = aws_security_group.rds.id
}

output "cluster_resource_id" {
  description = "Cluster resource ID, for CloudWatch alarm dimensions in the observability module."
  value       = aws_rds_cluster.this.cluster_resource_id
}
