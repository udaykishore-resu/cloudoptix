package compiler

import (
	"regexp"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/simulate"
)

// RawResource is the parser-agnostic shape every dialect normalizes onto.
// pricer.go dispatches purely on Type, so a new source format needs a parser
// that fills this struct and nothing else changes.
type RawResource struct {
	// Address is the human-readable path to the resource: a Terraform
	// resource address, a CloudFormation logical id, or "kind/namespace/name"
	// for a Kubernetes object.
	Address string
	// Type is the canonical resource-type key pricer.go dispatches on — the
	// Terraform provider type name (aws_instance, aws_ebs_volume, ...) for
	// AWS resources, or a "k8s_"-prefixed key for Kubernetes workloads. See
	// the package doc comment for why Terraform's vocabulary was chosen as
	// the canonical one.
	Type   string
	Action simulate.ChangeAction
	Region core.Region

	// Before/After are the resource's attributes before and after the
	// change, nil when the action has no such side (create has no Before,
	// delete has no After). Declarative sources with no diff concept — raw
	// HCL, a CloudFormation template, a Kubernetes manifest — set only After
	// and Action=ChangeCreate; see each parser's doc comment for why that is
	// the honest reading of a desired-state file rather than a diff.
	Before Attrs
	After  Attrs

	Tags map[string]string

	// Warnings carries parser-level caveats specific to this one resource
	// (an HCL count/for_each meta-argument the scanner cannot expand, a
	// container with no resources.requests) so they ride along to the
	// resource's own PricedChange.Warnings rather than being reported
	// disconnected from the thing they are about.
	Warnings []string
}

// Effective returns the attribute bag to price from: After for anything but
// a pure delete, Before for a delete (so a removed resource's savings are
// computed from what it used to cost).
func (r RawResource) Effective() Attrs {
	if r.Action == simulate.ChangeDelete {
		return r.Before
	}
	return r.After
}

// baseAddressPattern strips a trailing count/for_each index — "[0]" or
// ["key"] — from a Terraform resource address, which is how the compiler
// groups an expanded count/for_each family back into one logical resource
// for the CostRisk that flags a fan-out on a priced resource.
var baseAddressPattern = regexp.MustCompile(`\[[^\]]*\]$`)

// BaseAddress strips a trailing [index] or ["key"], or returns the address
// unchanged when it carries none.
func BaseAddress(address string) string {
	return baseAddressPattern.ReplaceAllString(address, "")
}

// terraformKindHints maps a canonical resource type onto the cloud.Kind used
// elsewhere in CloudOptix, purely for PricedChange.Kind — an informational
// field the twin and dashboard can group by. Absence from this map is not an
// error: plenty of priced or free resource types (launch templates, task
// definitions, log groups' retention policy) have no single-resource
// cloud.Kind equivalent, and PricedChange.Kind is simply left blank for them.
var terraformKindHints = map[string]cloud.Kind{
	"aws_instance":                       cloud.KindEC2Instance,
	"aws_autoscaling_group":              cloud.KindAutoScalingGroup,
	"aws_ebs_volume":                     cloud.KindEBSVolume,
	"aws_db_instance":                    cloud.KindRDSInstance,
	"aws_rds_cluster":                    cloud.KindRDSCluster,
	"aws_rds_cluster_instance":           cloud.KindRDSInstance,
	"aws_elasticache_cluster":            cloud.KindElastiCache,
	"aws_elasticache_replication_group":  cloud.KindElastiCache,
	"aws_dynamodb_table":                 cloud.KindDynamoDBTable,
	"aws_s3_bucket":                      cloud.KindS3Bucket,
	"aws_lambda_function":                cloud.KindLambdaFunction,
	"aws_nat_gateway":                    cloud.KindNATGateway,
	"aws_lb":                             cloud.KindALB,
	"aws_cloudfront_distribution":        cloud.KindCloudFront,
	"aws_apigatewayv2_api":               cloud.KindAPIGateway,
	"aws_api_gateway_rest_api":           cloud.KindAPIGateway,
	"aws_eks_cluster":                    cloud.KindEKSCluster,
	"aws_eks_node_group":                 cloud.KindEKSNodeGroup,
	"aws_ecs_service":                    cloud.KindECSService,
	"aws_vpc_endpoint":                   cloud.KindVPCEndpoint,
	"aws_cloudwatch_log_group":           cloud.KindLogGroup,
	"aws_kms_key":                        cloud.KindKMSKey,
	"aws_secretsmanager_secret":          cloud.KindSecret,
	"aws_sqs_queue":                      cloud.KindSQSQueue,
	"aws_msk_cluster":                    cloud.KindMSKCluster,
	"aws_eip":                            cloud.KindElasticIP,
	"aws_transit_gateway":                cloud.KindTransitGateway,
	"aws_transit_gateway_vpc_attachment": cloud.KindTransitGateway,
	"k8s_deployment":                     cloud.KindK8sWorkload,
	"k8s_statefulset":                    cloud.KindK8sWorkload,
	"k8s_daemonset":                      cloud.KindK8sWorkload,
}

// cfnTypeToTerraform translates a CloudFormation resource type onto the
// canonical Terraform-style key pricer.go dispatches on. Only the types this
// compiler prices (or explicitly knows are free) are listed; anything absent
// falls through to Unpriced with the CloudFormation type name in the reason,
// which is honest — the compiler was never taught that type, as opposed to
// having tried and failed to price it.
var cfnTypeToTerraform = map[string]string{
	"AWS::EC2::Instance":                        "aws_instance",
	"AWS::AutoScaling::AutoScalingGroup":        "aws_autoscaling_group",
	"AWS::EC2::LaunchTemplate":                  "aws_launch_template",
	"AWS::EC2::Volume":                          "aws_ebs_volume",
	"AWS::RDS::DBInstance":                      "aws_db_instance",
	"AWS::RDS::DBCluster":                       "aws_rds_cluster",
	"AWS::ElastiCache::CacheCluster":            "aws_elasticache_cluster",
	"AWS::ElastiCache::ReplicationGroup":        "aws_elasticache_replication_group",
	"AWS::DynamoDB::Table":                      "aws_dynamodb_table",
	"AWS::S3::Bucket":                           "aws_s3_bucket",
	"AWS::Lambda::Function":                     "aws_lambda_function",
	"AWS::EC2::NatGateway":                      "aws_nat_gateway",
	"AWS::ElasticLoadBalancingV2::LoadBalancer": "aws_lb",
	"AWS::CloudFront::Distribution":             "aws_cloudfront_distribution",
	"AWS::ApiGatewayV2::Api":                    "aws_apigatewayv2_api",
	"AWS::ApiGateway::RestApi":                  "aws_api_gateway_rest_api",
	"AWS::EKS::Cluster":                         "aws_eks_cluster",
	"AWS::EKS::Nodegroup":                       "aws_eks_node_group",
	"AWS::ECS::Service":                         "aws_ecs_service",
	"AWS::ECS::TaskDefinition":                  "aws_ecs_task_definition",
	"AWS::EC2::VPCEndpoint":                     "aws_vpc_endpoint",
	"AWS::Logs::LogGroup":                       "aws_cloudwatch_log_group",
	"AWS::KMS::Key":                             "aws_kms_key",
	"AWS::SecretsManager::Secret":               "aws_secretsmanager_secret",
	"AWS::SQS::Queue":                           "aws_sqs_queue",
	"AWS::MSK::Cluster":                         "aws_msk_cluster",
	"AWS::EC2::EIP":                             "aws_eip",
	"AWS::EC2::TransitGateway":                  "aws_transit_gateway",
	"AWS::EC2::TransitGatewayAttachment":        "aws_transit_gateway_vpc_attachment",
	"AWS::EC2::VPC":                             "aws_vpc",
	"AWS::EC2::Subnet":                          "aws_subnet",
	"AWS::EC2::RouteTable":                      "aws_route_table",
	"AWS::EC2::SecurityGroup":                   "aws_security_group",
	"AWS::IAM::Role":                            "aws_iam_role",
	"AWS::IAM::Policy":                          "aws_iam_policy",
	"AWS::IAM::InstanceProfile":                 "aws_iam_instance_profile",
}

// cfnPropertyAliases translates the PascalCase CloudFormation property names
// this compiler reads onto the snake_case attribute keys the Terraform-shaped
// pricing functions expect, per resource type. Only properties an actual
// pricing function consults are listed here; an alias missing for a property
// the compiler never reads is not a gap.
var cfnPropertyAliases = map[string]map[string]string{
	"AWS::EC2::Instance": {
		"InstanceType": "instance_type",
	},
	"AWS::EC2::Volume": {
		"Size":       "size",
		"VolumeType": "type",
		"Iops":       "iops",
		"Throughput": "throughput",
	},
	"AWS::RDS::DBInstance": {
		"DBInstanceClass":            "instance_class",
		"Engine":                     "engine",
		"MultiAZ":                    "multi_az",
		"AllocatedStorage":           "allocated_storage",
		"StorageType":                "storage_type",
		"Iops":                       "iops",
		"SourceDBInstanceIdentifier": "replicate_source_db",
	},
	"AWS::RDS::DBCluster": {
		"Engine":           "engine",
		"EngineMode":       "engine_mode",
		"AllocatedStorage": "allocated_storage",
	},
	"AWS::ElastiCache::CacheCluster": {
		"CacheNodeType": "node_type",
		"Engine":        "engine",
		"NumCacheNodes": "num_cache_nodes",
	},
	"AWS::ElastiCache::ReplicationGroup": {
		"CacheNodeType":        "node_type",
		"Engine":               "engine",
		"NumNodeGroups":        "num_node_groups",
		"ReplicasPerNodeGroup": "replicas_per_node_group",
		"NumCacheClusters":     "num_cache_clusters",
	},
	"AWS::DynamoDB::Table": {
		"BillingMode": "billing_mode",
	},
	"AWS::Lambda::Function": {
		"MemorySize":    "memory_size",
		"Timeout":       "timeout",
		"Architectures": "architectures",
	},
	"AWS::ElasticLoadBalancingV2::LoadBalancer": {
		"Type": "load_balancer_type",
	},
	"AWS::EC2::VPCEndpoint": {
		"VpcEndpointType": "vpc_endpoint_type",
		"ServiceName":     "service_name",
	},
	"AWS::Logs::LogGroup": {
		"RetentionInDays": "retention_in_days",
	},
	"AWS::EKS::Nodegroup": {
		"InstanceTypes": "instance_types",
		"ScalingConfig": "scaling_config",
	},
	"AWS::ECS::Service": {
		"LaunchType":   "launch_type",
		"DesiredCount": "desired_count",
	},
	"AWS::ECS::TaskDefinition": {
		"Cpu":    "cpu",
		"Memory": "memory",
	},
	"AWS::EC2::EIP": {
		"InstanceId": "instance",
	},
}

// knownFreeTerraformTypes are resource types this compiler recognizes as
// carrying no AWS bill of their own — their cost, if any, is entirely carried
// by another resource that references them (a launch template's cost is the
// EC2 instances launched from it; an IAM role has no hourly charge). Pricing
// them at zero is a fact, not an omission, which is exactly the "free" half
// of the free-vs-unpriced distinction this package exists to keep honest.
var knownFreeTerraformTypes = map[string]string{
	"aws_launch_template":                      "not independently billed; its cost is carried by the instances launched from it",
	"aws_launch_configuration":                 "not independently billed; its cost is carried by the instances launched from it",
	"aws_ecs_task_definition":                  "not independently billed; its cost is carried by the ECS service that runs it",
	"aws_vpc":                                  "VPCs themselves have no hourly or usage charge",
	"aws_subnet":                               "subnets themselves have no hourly or usage charge",
	"aws_route_table":                          "route tables have no hourly or usage charge",
	"aws_route":                                "routes have no hourly or usage charge",
	"aws_security_group":                       "security groups have no hourly or usage charge",
	"aws_security_group_rule":                  "security group rules have no hourly or usage charge",
	"aws_iam_role":                             "IAM roles have no hourly or usage charge",
	"aws_iam_policy":                           "IAM policies have no hourly or usage charge",
	"aws_iam_instance_profile":                 "IAM instance profiles have no hourly or usage charge",
	"aws_internet_gateway":                     "internet gateways have no hourly or usage charge",
	"aws_network_acl":                          "network ACLs have no hourly or usage charge",
	"aws_transit_gateway":                      "the transit gateway resource itself has no hourly charge; its attachments do",
	"aws_vpc_endpoint_route_table_association": "route table associations have no hourly or usage charge",
}
