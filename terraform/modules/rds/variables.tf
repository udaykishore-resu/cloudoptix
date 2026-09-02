variable "name" {
  type = string
}

variable "environment" {
  type = string
}

variable "vpc_id" {
  type = string
}

variable "database_subnet_ids" {
  description = "Subnet IDs from the network module's database tier (no NAT/IGW route)."
  type        = list(string)
}

variable "allowed_security_group_ids" {
  description = "Security groups (typically the EKS node/pod security group) allowed to reach Postgres on 5432."
  type        = list(string)
}

variable "kms_key_arn" {
  description = "KMS key (from the security module's app key) encrypting storage and the RDS-managed master password secret."
  type        = string
}

variable "engine_version" {
  description = "Aurora PostgreSQL engine version. Pinned explicitly (never \"latest\") so a provider refresh never silently changes the running engine version underneath a deployed environment."
  type        = string
  default     = "16.4"
}

variable "database_name" {
  description = "Matches internal/infrastructure/config.DatabaseConfig.Name (CLOUDOPTIX_DATABASE_NAME)."
  type        = string
  default     = "cloudoptix"
}

variable "master_username" {
  description = "Matches internal/infrastructure/config.DatabaseConfig.User (CLOUDOPTIX_DATABASE_USER)."
  type        = string
  default     = "cloudoptix"
}

variable "serverless" {
  description = <<-EOT
    true: Aurora Serverless v2, scaling capacity within
    [serverless_min_acu, serverless_max_acu] on demand — the right default
    for dev/staging and for a production environment still finding its
    steady-state load, since idle capacity costs nothing beyond the
    configured floor.

    false: fixed-size provisioned instances at rds_instance_class, sized by
    an operator who has actually looked at sustained load. Provisioned is
    cheaper than serverless once utilisation is high and steady enough that
    serverless's autoscaling headroom stops buying anything — which is
    itself exactly the kind of rightsizing judgment call CloudOptix's own
    product exists to help a customer make about their own Aurora clusters.
  EOT
  type = bool
  default = true
}

variable "serverless_min_acu" {
  type    = number
  default = 0.5
}

variable "serverless_max_acu" {
  type    = number
  default = 4
}

variable "provisioned_instance_class" {
  description = "Instance class for each cluster member when serverless = false."
  type        = string
  default     = "db.r6g.large"
}

variable "instance_count" {
  description = "Cluster member count: a writer plus (instance_count - 1) readers. Aurora, unlike single-instance RDS, makes an in-region reader close to free to add for read-scaling and fast failover, so 2 is a reasonable production floor; the demo estate's oversized-idle-replica pathology is deliberately NOT modelled here — see terraform/demo."
  type        = number
  default     = 2
}

variable "backup_retention_days" {
  type    = number
  default = 14
}

variable "backup_window" {
  description = "UTC window for automated backups, kept outside the production traffic peak."
  type        = string
  default     = "05:00-06:00"
}

variable "maintenance_window" {
  type    = string
  default = "sun:06:30-sun:07:30"
}

variable "deletion_protection" {
  type    = bool
  default = true
}

variable "skip_final_snapshot" {
  description = "Only ever true for disposable environments (dev, terraform/demo) — production and staging must always leave a final snapshot on destroy."
  type        = bool
  default     = false
}

variable "monitoring_interval_seconds" {
  description = "Enhanced Monitoring granularity, in seconds. 0 disables Enhanced Monitoring (dev default, to skip the monitoring role's small per-instance cost)."
  type        = number
  default     = 0
}

variable "performance_insights_enabled" {
  type    = bool
  default = true
}

variable "performance_insights_retention_days" {
  description = "7 (free tier) or 731 (2 years, paid) are the only two values Performance Insights actually accepts for the standard/long-term tiers."
  type        = number
  default     = 7
}

variable "tags" {
  type    = map(string)
  default = {}
}
