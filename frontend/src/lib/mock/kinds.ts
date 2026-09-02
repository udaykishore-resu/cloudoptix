import type { ResourceCategory } from "@/types/domain";

export interface KindSpec {
  kind: string;
  label: string;
  service: string;
  category: ResourceCategory;
  /** Typical monthly cost range in USD for one instance of this kind. */
  costRange: [number, number];
  mutable: boolean;
  icon: string; // lucide icon name, resolved in components/shared/resource-icon.tsx
}

export const KIND_CATALOG: Record<string, KindSpec> = {
  "aws.ec2.instance": { kind: "aws.ec2.instance", label: "EC2 Instance", service: "EC2", category: "compute", costRange: [25, 1400], mutable: true, icon: "Server" },
  "aws.ebs.volume": { kind: "aws.ebs.volume", label: "EBS Volume", service: "EBS", category: "storage", costRange: [3, 220], mutable: true, icon: "HardDrive" },
  "aws.ebs.snapshot": { kind: "aws.ebs.snapshot", label: "EBS Snapshot", service: "EBS", category: "storage", costRange: [1, 40], mutable: true, icon: "Camera" },
  "aws.ec2.elastic_ip": { kind: "aws.ec2.elastic_ip", label: "Elastic IP", service: "EC2", category: "network", costRange: [3.6, 3.6], mutable: true, icon: "Globe" },
  "aws.ec2.image": { kind: "aws.ec2.image", label: "AMI", service: "EC2", category: "storage", costRange: [1, 15], mutable: true, icon: "Layers" },
  "aws.autoscaling.group": { kind: "aws.autoscaling.group", label: "Auto Scaling Group", service: "EC2", category: "compute", costRange: [0, 0], mutable: true, icon: "Boxes" },
  "aws.rds.instance": { kind: "aws.rds.instance", label: "RDS Instance", service: "RDS", category: "database", costRange: [40, 2600], mutable: true, icon: "Database" },
  "aws.rds.cluster": { kind: "aws.rds.cluster", label: "Aurora Cluster", service: "RDS", category: "database", costRange: [120, 4200], mutable: true, icon: "Database" },
  "aws.rds.snapshot": { kind: "aws.rds.snapshot", label: "RDS Snapshot", service: "RDS", category: "database", costRange: [2, 60], mutable: false, icon: "Camera" },
  "aws.dynamodb.table": { kind: "aws.dynamodb.table", label: "DynamoDB Table", service: "DynamoDB", category: "database", costRange: [5, 900], mutable: true, icon: "Table" },
  "aws.s3.bucket": { kind: "aws.s3.bucket", label: "S3 Bucket", service: "S3", category: "storage", costRange: [2, 640], mutable: true, icon: "Archive" },
  "aws.lambda.function": { kind: "aws.lambda.function", label: "Lambda Function", service: "Lambda", category: "compute", costRange: [0, 320], mutable: true, icon: "Zap" },
  "aws.ecs.cluster": { kind: "aws.ecs.cluster", label: "ECS Cluster", service: "ECS", category: "compute", costRange: [0, 0], mutable: false, icon: "Container" },
  "aws.ecs.service": { kind: "aws.ecs.service", label: "ECS Service", service: "ECS", category: "compute", costRange: [30, 900], mutable: true, icon: "Container" },
  "aws.eks.cluster": { kind: "aws.eks.cluster", label: "EKS Cluster", service: "EKS", category: "compute", costRange: [73, 73], mutable: false, icon: "Ship" },
  "aws.eks.nodegroup": { kind: "aws.eks.nodegroup", label: "EKS Node Group", service: "EKS", category: "compute", costRange: [200, 3200], mutable: true, icon: "Ship" },
  "aws.elbv2.application": { kind: "aws.elbv2.application", label: "Application Load Balancer", service: "ELB", category: "network", costRange: [18, 90], mutable: false, icon: "Waypoints" },
  "aws.elbv2.network": { kind: "aws.elbv2.network", label: "Network Load Balancer", service: "ELB", category: "network", costRange: [18, 90], mutable: false, icon: "Waypoints" },
  "aws.cloudfront.distribution": { kind: "aws.cloudfront.distribution", label: "CloudFront Distribution", service: "CloudFront", category: "network", costRange: [10, 380], mutable: false, icon: "Cloud" },
  "aws.apigateway.api": { kind: "aws.apigateway.api", label: "API Gateway", service: "API Gateway", category: "network", costRange: [5, 210], mutable: false, icon: "Waypoints" },
  "aws.ec2.nat_gateway": { kind: "aws.ec2.nat_gateway", label: "NAT Gateway", service: "VPC", category: "network", costRange: [32, 480], mutable: true, icon: "Router" },
  "aws.ec2.vpc": { kind: "aws.ec2.vpc", label: "VPC", service: "VPC", category: "network", costRange: [0, 0], mutable: false, icon: "Network" },
  "aws.ec2.vpc_endpoint": { kind: "aws.ec2.vpc_endpoint", label: "VPC Endpoint", service: "VPC", category: "network", costRange: [7, 7], mutable: true, icon: "Network" },
  "aws.elasticache.cluster": { kind: "aws.elasticache.cluster", label: "ElastiCache Cluster", service: "ElastiCache", category: "database", costRange: [25, 900], mutable: true, icon: "Gauge" },
  "aws.msk.cluster": { kind: "aws.msk.cluster", label: "MSK Cluster", service: "MSK", category: "messaging", costRange: [400, 2400], mutable: false, icon: "GitBranch" },
  "aws.sqs.queue": { kind: "aws.sqs.queue", label: "SQS Queue", service: "SQS", category: "messaging", costRange: [0, 60], mutable: false, icon: "ListOrdered" },
  "aws.sns.topic": { kind: "aws.sns.topic", label: "SNS Topic", service: "SNS", category: "messaging", costRange: [0, 20], mutable: false, icon: "Radio" },
  "aws.kinesis.stream": { kind: "aws.kinesis.stream", label: "Kinesis Stream", service: "Kinesis", category: "messaging", costRange: [15, 400], mutable: true, icon: "Waves" },
  "aws.logs.log_group": { kind: "aws.logs.log_group", label: "CloudWatch Log Group", service: "CloudWatch", category: "observability", costRange: [1, 260], mutable: true, icon: "ScrollText" },
  "aws.kms.key": { kind: "aws.kms.key", label: "KMS Key", service: "KMS", category: "security", costRange: [1, 3], mutable: false, icon: "KeyRound" },
  "aws.secretsmanager.secret": { kind: "aws.secretsmanager.secret", label: "Secret", service: "Secrets Manager", category: "security", costRange: [0.4, 0.4], mutable: false, icon: "Lock" },
};

export const KIND_LABEL = (k: string) => KIND_CATALOG[k]?.label ?? k;
export const KIND_ICON = (k: string) => KIND_CATALOG[k]?.icon ?? "Box";
export const KIND_CATEGORY = (k: string) => KIND_CATALOG[k]?.category ?? "other";
export const KIND_SERVICE = (k: string) => KIND_CATALOG[k]?.service ?? k.split(".")[1] ?? k;

export const CATEGORY_LABEL: Record<ResourceCategory, string> = {
  compute: "Compute",
  database: "Database",
  storage: "Storage",
  network: "Network",
  messaging: "Messaging",
  observability: "Observability",
  security: "Security",
  other: "Other",
};
