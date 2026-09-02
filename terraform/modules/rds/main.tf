locals {
  common_tags = merge(var.tags, {
    Module      = "rds"
    Environment = var.environment
    ManagedBy   = "terraform"
  })
}

resource "aws_db_subnet_group" "this" {
  name       = "${var.name}-db"
  subnet_ids = var.database_subnet_ids
  tags       = local.common_tags
}

# Aurora's own parameter group (not the RDS instance-level one) — the one
# setting worth calling out is log_min_duration_statement: CloudOptix's own
# telemetry (internal/infrastructure/telemetry) exports request latency, but
# the database's own slow-query log is what an SRE actually reaches for when
# a p99 regression traces back to a specific query rather than "the database
# is slow".
resource "aws_rds_cluster_parameter_group" "this" {
  name        = "${var.name}-aurora-pg16"
  family      = "aurora-postgresql16"
  description = "CloudOptix Aurora PostgreSQL cluster parameters."

  parameter {
    name  = "log_min_duration_statement"
    value = "1000" # log anything slower than 1s
  }
  parameter {
    name  = "log_connections"
    value = "1"
  }
  parameter {
    name  = "log_disconnections"
    value = "1"
  }

  tags = local.common_tags
}

resource "aws_security_group" "rds" {
  name        = "${var.name}-rds"
  description = "Allows Postgres from CloudOptix's own workloads only."
  vpc_id      = var.vpc_id

  tags = merge(local.common_tags, { Name = "${var.name}-rds-sg" })
}

resource "aws_vpc_security_group_ingress_rule" "rds_from_app" {
  for_each                    = toset(var.allowed_security_group_ids)
  security_group_id           = aws_security_group.rds.id
  referenced_security_group_id = each.value
  from_port                   = 5432
  to_port                     = 5432
  ip_protocol                 = "tcp"
  description                 = "Postgres from CloudOptix workloads"
}

# Enhanced Monitoring's IAM role, only created when monitoring is on — an
# unused IAM role is harmless but not free of audit noise, so it is gated
# the same as the feature it serves.
resource "aws_iam_role" "monitoring" {
  count = var.monitoring_interval_seconds > 0 ? 1 : 0
  name  = "${var.name}-rds-monitoring"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "monitoring.rds.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
  tags = local.common_tags
}

resource "aws_iam_role_policy_attachment" "monitoring" {
  count      = var.monitoring_interval_seconds > 0 ? 1 : 0
  role       = aws_iam_role.monitoring[0].name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonRDSEnhancedMonitoringRole"
}

# The cluster. manage_master_user_password delegates password generation and
# storage entirely to RDS/Secrets Manager — Terraform never sees, holds, or
# has a plan/state diff containing the actual credential. The app reads it
# via the ExternalSecrets sync documented in this module's README, not via
# anything this module outputs directly.
resource "aws_rds_cluster" "this" {
  cluster_identifier     = var.name
  engine                 = "aurora-postgresql"
  engine_version         = var.engine_version
  engine_mode            = "provisioned" # required value even for Serverless v2; capacity comes from serverlessv2_scaling_configuration below
  database_name          = var.database_name
  master_username        = var.master_username
  manage_master_user_password = true
  master_user_secret_kms_key_id = var.kms_key_arn

  db_subnet_group_name            = aws_db_subnet_group.this.name
  vpc_security_group_ids          = [aws_security_group.rds.id]
  db_cluster_parameter_group_name = aws_rds_cluster_parameter_group.this.name

  storage_encrypted = true
  kms_key_id        = var.kms_key_arn

  backup_retention_period = var.backup_retention_days
  preferred_backup_window = var.backup_window
  preferred_maintenance_window = var.maintenance_window

  deletion_protection      = var.deletion_protection
  skip_final_snapshot      = var.skip_final_snapshot
  final_snapshot_identifier = var.skip_final_snapshot ? null : "${var.name}-final-${formatdate("YYYYMMDDhhmmss", timestamp())}"

  copy_tags_to_snapshot = true

  dynamic "serverlessv2_scaling_configuration" {
    for_each = var.serverless ? [1] : []
    content {
      min_capacity = var.serverless_min_acu
      max_capacity = var.serverless_max_acu
    }
  }

  tags = local.common_tags

  lifecycle {
    # final_snapshot_identifier embeds timestamp() so an accidental
    # replace-on-destroy always produces a uniquely-named snapshot instead
    # of colliding with a prior one; it must not otherwise force a diff on
    # every plan.
    ignore_changes = [final_snapshot_identifier]
  }
}

# Cluster members: instance_count total, Aurora auto-elects one writer.
# instance_class is meaningless for Serverless v2 members (capacity comes
# from the cluster's serverlessv2_scaling_configuration) but the argument is
# still required by the provider; "db.serverless" is the documented
# placeholder value for that case.
resource "aws_rds_cluster_instance" "this" {
  count              = var.instance_count
  identifier         = "${var.name}-${count.index}"
  cluster_identifier = aws_rds_cluster.this.id
  engine             = aws_rds_cluster.this.engine
  engine_version     = aws_rds_cluster.this.engine_version
  instance_class     = var.serverless ? "db.serverless" : var.provisioned_instance_class

  db_subnet_group_name = aws_db_subnet_group.this.name

  performance_insights_enabled          = var.performance_insights_enabled
  performance_insights_kms_key_id       = var.performance_insights_enabled ? var.kms_key_arn : null
  performance_insights_retention_period = var.performance_insights_enabled ? var.performance_insights_retention_days : null

  monitoring_interval = var.monitoring_interval_seconds
  monitoring_role_arn = var.monitoring_interval_seconds > 0 ? aws_iam_role.monitoring[0].arn : null

  # Every member (writer and readers alike) auto-applies minor version
  # upgrades during the cluster's maintenance window; the major version is
  # controlled explicitly via var.engine_version and never auto-upgrades.
  auto_minor_version_upgrade = true

  tags = local.common_tags
}
