locals {
  common_tags = merge(var.tags, {
    Module      = "redis"
    Environment = var.environment
    ManagedBy   = "terraform"
  })
}

resource "aws_elasticache_subnet_group" "this" {
  name       = "${var.name}-redis"
  subnet_ids = var.database_subnet_ids
  tags       = local.common_tags
}

resource "aws_security_group" "redis" {
  name        = "${var.name}-redis"
  description = "Allows Redis (TLS) from CloudOptix's own workloads only."
  vpc_id      = var.vpc_id
  tags        = merge(local.common_tags, { Name = "${var.name}-redis-sg" })
}

resource "aws_vpc_security_group_ingress_rule" "redis_from_app" {
  for_each                      = toset(var.allowed_security_group_ids)
  security_group_id             = aws_security_group.redis.id
  referenced_security_group_id  = each.value
  from_port                     = 6379
  to_port                       = 6379
  ip_protocol                   = "tcp"
  description                   = "Redis (TLS) from CloudOptix workloads"
}

resource "aws_elasticache_parameter_group" "this" {
  name   = "${var.name}-redis7"
  family = "redis7"
  tags   = local.common_tags
}

# ElastiCache has no RDS-style "manage this credential for me" option, so
# this module generates the AUTH token itself. The value lives only in
# Terraform's own (encrypted, access-controlled — see
# terraform/environments/*/backend.tf) remote state and in the Secrets
# Manager version below; it never appears in any file this module writes.
# random_password, not a static default, so re-running apply against a
# fresh state never reintroduces a predictable token.
resource "random_password" "auth_token" {
  length  = 32
  special = false # ElastiCache AUTH tokens may not contain the characters "@\"/" — disabling special characters entirely is the simplest way to guarantee that
}

# Unlike security's placeholder secrets (which are deliberately never
# updated by Terraform again after creation), this version has no
# lifecycle.ignore_changes — this module IS the source of truth for the
# Redis AUTH token, so it should keep the secret's value in sync with
# random_password.auth_token across every apply.
resource "aws_secretsmanager_secret_version" "redis_auth" {
  secret_id     = var.secret_arn
  secret_string = random_password.auth_token.result
}

resource "aws_elasticache_replication_group" "this" {
  replication_group_id = var.name
  description           = "CloudOptix shared cache / distributed lock backend."

  engine         = "redis"
  engine_version = var.engine_version
  node_type      = var.node_type

  # Single shard (no cluster-mode sharding — CloudOptix's Redis usage is
  # cache + distributed lock, not a dataset that needs partitioning across
  # shards). num_cache_clusters is 1 primary plus (num_cache_clusters - 1)
  # read replicas; automatic_failover_enabled requires at least 2.
  num_cache_clusters         = var.num_cache_clusters
  automatic_failover_enabled = var.automatic_failover_enabled
  multi_az_enabled           = var.multi_az_enabled

  subnet_group_name    = aws_elasticache_subnet_group.this.name
  security_group_ids   = [aws_security_group.redis.id]
  parameter_group_name = aws_elasticache_parameter_group.this.name

  at_rest_encryption_enabled = true
  kms_key_id                 = var.kms_key_arn
  transit_encryption_enabled = true
  auth_token                 = random_password.auth_token.result
  auth_token_update_strategy = "ROTATE"

  snapshot_retention_limit = var.snapshot_retention_days
  snapshot_window          = "05:00-06:00"
  maintenance_window       = "sun:06:30-sun:07:30"

  tags = local.common_tags
}
