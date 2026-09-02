# dev: cheapest shape that still exercises every module — single NAT
# gateway, serverless Aurora at its floor, one Redis node, deletion
# protection off, short retention everywhere. Never used for anything a
# customer's data passes through; see terraform/demo for the (also cheap,
# but deliberately wasteful in a different way) demo estate.

module "network" {
  source = "../../modules/network"

  name               = var.name
  environment        = "development"
  az_count           = 3
  single_nat_gateway = true # see the network module's variable doc — the dev/staging default
  enable_flow_logs   = true
  tags               = local.tags
}

module "eks" {
  source = "../../modules/eks"

  name                   = var.name
  environment            = "development"
  vpc_id                 = module.network.vpc_id
  private_subnet_ids     = module.network.private_subnet_ids
  public_subnet_ids      = module.network.public_subnet_ids
  endpoint_public_access = true

  on_demand_instance_types = ["m6i.large"]
  on_demand_min_size       = 1
  on_demand_max_size       = 3
  on_demand_desired_size   = 1

  spot_instance_types = ["m6i.large", "m6a.large", "m5.large"]
  spot_min_size       = 1
  spot_max_size       = 6
  spot_desired_size   = 1

  autoscaler = "karpenter"
  tags       = local.tags
}

module "security" {
  source = "../../modules/security"

  name                  = var.name
  environment           = "development"
  eks_oidc_provider_arn = module.eks.oidc_provider_arn
  eks_oidc_provider_url = module.eks.oidc_provider_url
  namespace             = "cloudoptix"
  tags                  = local.tags
}

module "rds" {
  source = "../../modules/rds"

  name                        = var.name
  environment                 = "development"
  vpc_id                      = module.network.vpc_id
  database_subnet_ids         = module.network.database_subnet_ids
  allowed_security_group_ids  = [module.eks.node_security_group_id]
  kms_key_arn                 = module.security.app_kms_key_arn

  serverless         = true
  serverless_min_acu = 0.5
  serverless_max_acu = 2
  instance_count     = 1 # dev tolerates a single-instance cluster with no reader; production does not — see that environment

  deletion_protection = false
  skip_final_snapshot = true
  backup_retention_days = 3

  tags = local.tags
}

module "redis" {
  source = "../../modules/redis"

  name                        = var.name
  environment                 = "development"
  vpc_id                      = module.network.vpc_id
  database_subnet_ids         = module.network.database_subnet_ids
  allowed_security_group_ids  = [module.eks.node_security_group_id]
  kms_key_arn                 = module.security.app_kms_key_arn
  secret_arn                  = module.security.secret_arns["redis-password"]

  node_type                  = "cache.t4g.small"
  num_cache_clusters         = 1
  automatic_failover_enabled = false

  tags = local.tags
}

module "storage" {
  source = "../../modules/storage"

  name              = var.name
  environment       = "development"
  app_kms_key_arn   = module.security.app_kms_key_arn
  audit_kms_key_arn = module.security.audit_kms_key_arn

  # A short, non-compliance retention for dev's audit bucket would defeat
  # the point of object lock (see that module's README: COMPLIANCE mode
  # cannot be shortened once set). dev intentionally still gets the full
  # module, including a real object-lock bucket, so schema/behaviour parity
  # with staging/production is actually exercised before either one sees a
  # change — the retention window is the one thing this environment does
  # NOT get to shrink for cost reasons.
  audit_object_lock_retention_days = 2557

  tags = local.tags
}

module "messaging" {
  source = "../../modules/messaging"

  name        = var.name
  environment = "development"
  kms_key_arn = module.security.app_kms_key_arn
  tags        = local.tags
}

module "observability" {
  source = "../../modules/observability"

  name        = var.name
  environment = "development"

  alarm_sns_topic_arns        = [module.messaging.topic_arns["operational-alerts"]]
  rds_cluster_identifier      = var.name # matches rds module's cluster_identifier = var.name
  redis_replication_group_id = var.name  # matches redis module's replication_group_id = var.name
  eks_cluster_name            = module.eks.cluster_name
  sqs_dlq_names               = { for k, v in module.messaging.dlq_arns : k => "${var.name}-${k}-dlq" }

  log_retention_days = 14 # short in dev — see the demo estate for what NOT setting this looks like
  tags                = local.tags

  depends_on = [module.rds, module.redis, module.messaging, module.eks]
}

locals {
  tags = {
    Project = "cloudoptix"
  }
}
