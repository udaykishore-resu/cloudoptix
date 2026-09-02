output "application_log_group_name" {
  value = aws_cloudwatch_log_group.application.name
}

output "application_log_group_arn" {
  value = aws_cloudwatch_log_group.application.arn
}

output "dashboard_name" {
  value = aws_cloudwatch_dashboard.platform.dashboard_name
}

output "otel_collector_config_ssm_parameter_arn" {
  value = aws_ssm_parameter.otel_collector_config.arn
}

output "otel_collector_config_ssm_parameter_name" {
  value = aws_ssm_parameter.otel_collector_config.name
}

output "otel_collector_iam_policy_json" {
  description = "Attach to the OTel collector's own IRSA role (a role this module does not itself create — see the README)."
  value       = data.aws_iam_policy_document.otel_collector.json
}
