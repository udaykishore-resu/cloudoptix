# staging: production's exact topology (multi-AZ NAT, Aurora with a real
# reader, Redis failover, deletion protection on) at production's smallest
# viable sizes — the point of staging is catching a topology or IAM bug
# before production sees it, which a cheaper-shaped staging cannot do.

module "network" {
  source = "../../modules/network"

  name               = var.name
  environment        = "staging"
  az_count           = 3
  single_nat_gateway = false # staging matches production's NAT topology — see this module's README
  enable_flow_logs   = true
  tags               = local.tags
}

module "eks" {
  source = "../../modules/eks"

  name                   = var.name
  environment            = "staging"
  vpc_id                 = module.network.vpc_id
  private_subnet_ids     = module.network.private_subnet_ids
  public_subnet_ids      = module.network.public_subnet_ids
  endpoint_public_access = true

  on_demand_instance_types = ["m6i.large"]
  on_demand_min_size       = 2
  on_demand_max_size       = 4
  on_demand_desired_size   = 2

  spot_instance_types = ["m6i.large", "m6a.large", "m5.large", "m5a.large"]
  spot_min_size       = 1
  spot_max_size       = 10
  spot_desired_size   = 2

  autoscaler = "karpenter"
  tags       = local.tags
}

module "security" {
  source = "../../modules/security"

  name                  = var.name
  environment           = "staging"
  eks_oidc_provider_arn = module.eks.oidc_provider_arn
  eks_oidc_provider_url = module.eks.oidc_provider_url
  namespace             = "cloudoptix"
  tags                  = local.tags
}

module "rds" {
  source = "../../modules/rds"

  name                        = var.name
  environment                 = "staging"
  vpc_id                      = module.network.vpc_id
  database_subnet_ids         = module.network.database_subnet_ids
  allowed_security_group_ids  = [module.eks.node_security_group_id]
  kms_key_arn                 = module.security.app_kms_key_arn

  serverless         = true
  serverless_min_acu = 0.5
  serverless_max_acu = 4
  instance_count     = 2 # writer + one reader — matches production's topology, not production's scale

  deletion_protection   = true
  skip_final_snapshot   = false
  backup_retention_days = 7

  tags = local.tags
}

module "redis" {
  source = "../../modules/redis"

  name                        = var.name
  environment                 = "staging"
  vpc_id                      = module.network.vpc_id
  database_subnet_ids         = module.network.database_subnet_ids
  allowed_security_group_ids  = [module.eks.node_security_group_id]
  kms_key_arn                 = module.security.app_kms_key_arn
  secret_arn                  = module.security.secret_arns["redis-password"]

  node_type                  = "cache.t4g.small"
  num_cache_clusters         = 2
  automatic_failover_enabled = true

  tags = local.tags
}

module "storage" {
  source = "../../modules/storage"

  name              = var.name
  environment       = "staging"
  app_kms_key_arn   = module.security.app_kms_key_arn
  audit_kms_key_arn = module.security.audit_kms_key_arn

  audit_object_lock_retention_days = 2557

  tags = local.tags
}

module "messaging" {
  source = "../../modules/messaging"

  name        = var.name
  environment = "staging"
  kms_key_arn = module.security.app_kms_key_arn
  tags        = local.tags
}

module "observability" {
  source = "../../modules/observability"

  name        = var.name
  environment = "staging"

  alarm_sns_topic_arns        = [module.messaging.topic_arns["operational-alerts"]]
  rds_cluster_identifier      = var.name
  redis_replication_group_id = var.name
  eks_cluster_name            = module.eks.cluster_name
  sqs_dlq_names               = { for k, v in module.messaging.dlq_arns : k => "${var.name}-${k}-dlq" }

  log_retention_days = 30
  tags                = local.tags

  depends_on = [module.rds, module.redis, module.messaging, module.eks]
}

locals {
  tags = {
    Project = "cloudoptix"
  }
}
