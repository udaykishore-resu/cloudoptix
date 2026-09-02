variable "name" {
  type = string
}

variable "environment" {
  type = string
}

variable "log_retention_days" {
  description = "CloudWatch Logs retention for the application log group. Matches internal/infrastructure/config.TelemetryConfig.LogFormat=json expectations (structured logs, aggregatable by any CloudWatch Logs Insights query)."
  type        = number
  default     = 30
}

variable "alarm_sns_topic_arns" {
  description = "SNS topic ARNs every alarm below notifies on ALARM and OK — typically messaging module's \"operational-alerts\" topic output."
  type        = list(string)
}

variable "rds_cluster_identifier" {
  description = "Aurora cluster identifier (rds module's output) for CPU/storage alarms. Empty string skips RDS alarms (e.g. an environment composition run before rds exists)."
  type        = string
  default     = ""
}

variable "redis_replication_group_id" {
  type    = string
  default = ""
}

variable "alb_arn_suffix" {
  description = "The ALB's arn_suffix (not full ARN — CloudWatch dimension shape), known only once the aws-load-balancer-controller has created it. Empty string skips ALB alarms; see the README for the chicken-and-egg note."
  type        = string
  default     = ""
}

variable "sqs_dlq_names" {
  description = "Dead-letter queue names (messaging module's dlq_arns keys, mapped to names) — each gets a \"has anything landed here at all\" alarm, because any message in a DLQ is by definition something that needs a human, not a threshold judgment call."
  type        = map(string)
  default     = {}
}

variable "eks_cluster_name" {
  type    = string
  default = ""
}

variable "enable_xray" {
  description = "Whether the ADOT collector config this module publishes should export traces to X-Ray, in addition to whatever OTLP endpoint internal/infrastructure/config.TelemetryConfig.OTLPEndpoint names."
  type        = bool
  default     = true
}

variable "tags" {
  type    = map(string)
  default = {}
}
