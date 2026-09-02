output "cluster_name" {
  value = module.eks.cluster_name
}

output "cluster_endpoint" {
  value = module.eks.cluster_endpoint
}

output "database_endpoint" {
  value = module.rds.cluster_endpoint
}

output "database_master_secret_arn" {
  value = module.rds.master_user_secret_arn
}

output "redis_endpoint" {
  value = module.redis.primary_endpoint
}

output "redis_auth_secret_arn" {
  value = module.redis.auth_token_secret_arn
}

output "component_role_arns" {
  description = "IRSA role ARNs — set as helm/cloudoptix's serviceAccount.<component>.roleArn values."
  value       = module.security.component_role_arns
}

output "waf_web_acl_arn" {
  value = module.security.waf_web_acl_arn
}

output "artefacts_bucket_name" {
  value = module.storage.artefacts_bucket_name
}

output "cur_bucket_name" {
  value = module.storage.cur_bucket_name
}

output "audit_bucket_name" {
  value = module.storage.audit_bucket_name
}

output "event_bus_name" {
  value = module.messaging.event_bus_name
}

output "dashboard_name" {
  value = module.observability.dashboard_name
}
