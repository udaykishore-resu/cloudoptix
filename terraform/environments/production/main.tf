# production: full HA — one NAT gateway per AZ, provisioned Aurora sized
# from observed load (serverless is still the toggle-in default until
# real traffic justifies switching — see the rds module's README), Redis
# with automatic failover, deletion protection everywhere, long retention.

module "network" {
  source = "../../modules/network"

  name               = var.name
  environment        = "production"
  az_count           = 3
  single_nat_gateway = false # production default — one NAT per AZ, see the network module's README
  enable_flow_logs   = true
  tags               = local.tags
}

module "eks" {
  source = "../../modules/eks"

  name                          = var.name
  environment                   = "production"
  vpc_id                        = module.network.vpc_id
  private_subnet_ids            = module.network.private_subnet_ids
  public_subnet_ids             = module.network.public_subnet_ids
  endpoint_public_access        = false # production requires the private endpoint + VPN/Session Manager path — see the eks module's variable doc
  endpoint_public_access_cidrs  = []

  on_demand_instance_types = ["m6i.large", "m6i.xlarge"]
  on_demand_min_size       = 3
  on_demand_max_size       = 8
  on_demand_desired_size   = 3

  spot_instance_types = ["m6i.large", "m6i.xlarge", "m6a.large", "m6a.xlarge", "m5.large", "m5.xlarge"]
  spot_min_size       = 3
  spot_max_size       = 40
  spot_desired_size   = 4

  autoscaler = "karpenter"
  tags       = local.tags
}

module "security" {
  source = "../../modules/security"

  name                  = var.name
  environment           = "production"
  eks_oidc_provider_arn = module.eks.oidc_provider_arn
  eks_oidc_provider_url = module.eks.oidc_provider_url
  namespace             = "cloudoptix"
  tags                  = local.tags
}

module "rds" {
  source = "../../modules/rds"

  name                        = var.name
  environment                 = "production"
  vpc_id                      = module.network.vpc_id
  database_subnet_ids         = module.network.database_subnet_ids
  allowed_security_group_ids  = [module.eks.node_security_group_id]
  kms_key_arn                 = module.security.app_kms_key_arn

  # Serverless v2 remains the default even in production until sustained
  # load actually justifies a provisioned instance class — see the rds
  # module's README for why that is itself the rightsizing discipline this
  # platform's own product asks of customers. Flip serverless=false and set
  # provisioned_instance_class once real usage says so.
  serverless          = true
  serverless_min_acu  = 1
  serverless_max_acu  = 16
  instance_count      = 2

  deletion_protection         = true
  skip_final_snapshot         = false
  backup_retention_days       = 30
  monitoring_interval_seconds = 60

  tags = local.tags
}

module "redis" {
  source = "../../modules/redis"

  name                        = var.name
  environment                 = "production"
  vpc_id                      = module.network.vpc_id
  database_subnet_ids         = module.network.database_subnet_ids
  allowed_security_group_ids  = [module.eks.node_security_group_id]
  kms_key_arn                 = module.security.app_kms_key_arn
  secret_arn                  = module.security.secret_arns["redis-password"]

  node_type                  = "cache.r7g.large"
  num_cache_clusters         = 3
  automatic_failover_enabled = true
  multi_az_enabled           = true

  tags = local.tags
}

module "storage" {
  source = "../../modules/storage"

  name              = var.name
  environment       = "production"
  app_kms_key_arn   = module.security.app_kms_key_arn
  audit_kms_key_arn = module.security.audit_kms_key_arn

  audit_object_lock_retention_days = 2557 # 7 years — see this module's README for why this cannot be changed downward later

  tags = local.tags
}

module "messaging" {
  source = "../../modules/messaging"

  name        = var.name
  environment = "production"
  kms_key_arn = module.security.app_kms_key_arn
  tags        = local.tags
}

module "observability" {
  source = "../../modules/observability"

  name        = var.name
  environment = "production"

  alarm_sns_topic_arns        = [module.messaging.topic_arns["operational-alerts"]]
  rds_cluster_identifier      = var.name
  redis_replication_group_id = var.name
  eks_cluster_name            = module.eks.cluster_name
  sqs_dlq_names               = { for k, v in module.messaging.dlq_arns : k => "${var.name}-${k}-dlq" }

  log_retention_days = 90
  tags                = local.tags

  depends_on = [module.rds, module.redis, module.messaging, module.eks]
}

locals {
  tags = {
    Project = "cloudoptix"
  }
}
