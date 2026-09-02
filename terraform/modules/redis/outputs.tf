output "primary_endpoint" {
  description = "Primary (writer) endpoint — the first entry of CLOUDOPTIX_REDIS_ADDRS."
  value       = aws_elasticache_replication_group.this.primary_endpoint_address
}

output "reader_endpoint" {
  value = try(aws_elasticache_replication_group.this.reader_endpoint_address, null)
}

output "port" {
  value = aws_elasticache_replication_group.this.port
}

output "security_group_id" {
  value = aws_security_group.redis.id
}

output "auth_token_secret_arn" {
  description = "Secrets Manager ARN holding the AUTH token (same ARN as var.secret_arn, echoed here for convenience)."
  value       = var.secret_arn
}
