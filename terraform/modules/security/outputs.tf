output "app_kms_key_arn" {
  description = "KMS key ARN for RDS, Redis, Secrets Manager and the artefacts/CUR S3 buckets."
  value       = aws_kms_key.app.arn
}

output "audit_kms_key_arn" {
  description = "KMS key ARN for the object-lock retained audit archive bucket."
  value       = aws_kms_key.audit.arn
}

output "secret_arns" {
  description = "Map of secret name -> Secrets Manager ARN, for the chart's ExternalSecrets templates to reference."
  value       = { for k, v in aws_secretsmanager_secret.this : k => v.arn }
}

output "component_role_arns" {
  description = "Map of component key (matching var.service_accounts) -> IAM role ARN, for the chart's ServiceAccount eks.amazonaws.com/role-arn annotation."
  value       = { for k, v in aws_iam_role.component : k => v.arn }
}

output "waf_web_acl_arn" {
  description = "WAFv2 Web ACL ARN — set as helm/cloudoptix's ingress.wafAclArn value."
  value       = aws_wafv2_web_acl.this.arn
}
