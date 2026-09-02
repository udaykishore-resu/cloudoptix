output "role_arns" {
  description = "Map of scope (read|analyze|plan|execute) -> role ARN. Paste these into the CloudOptix onboarding screen, or read them back with `terraform output -json role_arns` for a scripted onboarding flow."
  value       = { for k, v in aws_iam_role.this : k => v.arn }
}

output "role_names" {
  value = { for k, v in aws_iam_role.this : k => v.name }
}

output "external_id" {
  description = "Echoed back for confirmation — verify this matches what CloudOptix shows on your onboarding screen before considering setup complete."
  value       = var.external_id
  sensitive   = true
}
