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
  type = list(string)
}

variable "allowed_security_group_ids" {
  type = list(string)
}

variable "kms_key_arn" {
  type = string
}

variable "secret_arn" {
  description = "ARN of the Secrets Manager container (from the security module's `secrets` list, entry \"redis-password\") this module writes the generated AUTH token into."
  type        = string
}

variable "node_type" {
  description = "Cache node instance type. cache.t4g.small is the smallest Graviton burstable type that supports encryption in transit — a fine dev/staging default; production should size from actual cloudoptix_cache_hits_total / misses_total and memory pressure, the same discipline the platform asks of customers."
  type        = string
  default     = "cache.t4g.small"
}

variable "num_cache_clusters" {
  description = "Nodes in the replication group: 1 primary + (num_cache_clusters - 1) replicas. 1 disables replication entirely (dev); production should run at least 2 for automatic failover."
  type        = number
  default     = 1
}

variable "engine_version" {
  type    = string
  default = "7.1"
}

variable "automatic_failover_enabled" {
  description = "Requires num_cache_clusters >= 2. Promotes a replica automatically if the primary fails, instead of the cluster simply going unavailable until an operator intervenes."
  type        = bool
  default     = false
}

variable "multi_az_enabled" {
  type    = bool
  default = false
}

variable "snapshot_retention_days" {
  type    = number
  default = 5
}

variable "tags" {
  type    = map(string)
  default = {}
}
