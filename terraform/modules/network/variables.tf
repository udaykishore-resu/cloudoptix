variable "name" {
  description = "Name prefix for every resource this module creates (e.g. \"cloudoptix-production\")."
  type        = string
}

variable "environment" {
  description = "Environment tag (development|staging|production). Drives no logic here, only tagging — see the single_nat_gateway variable for the actual cost/HA decision."
  type        = string
}

variable "vpc_cidr" {
  description = "CIDR block for the VPC. /16 gives room for growth across three tiers x three AZs without renumbering later."
  type        = string
  default     = "10.0.0.0/16"
}

variable "az_count" {
  description = "Number of Availability Zones to span. CloudOptix's own SLOs assume 3 for production; kept configurable so dev can run on 2 in regions with fewer AZs."
  type        = number
  default     = 3
  validation {
    condition     = var.az_count >= 2 && var.az_count <= 6
    error_message = "az_count must be between 2 and 6."
  }
}

variable "single_nat_gateway" {
  description = <<-EOT
    When true, all private subnets route through ONE NAT gateway instead of one per AZ.

    This is the single biggest lever in this module for the exact problem CloudOptix
    exists to catch: a NAT gateway costs ~$0.045/hr (~$32/mo) plus ~$0.045/GB processed,
    per gateway, regardless of whether the subnet behind it does anything. Three NAT
    gateways sitting mostly idle in a dev VPC is textbook waste — see terraform/demo,
    which provisions exactly that on purpose so the platform has something to find.

    Trade-off: a single NAT gateway is also a single point of failure and a cross-AZ
    data-transfer cost (traffic from AZ-b and AZ-c's private subnets crosses the AZ
    boundary to reach the NAT in AZ-a). That is an acceptable trade in dev/staging,
    where nothing pages anyone at 3am over a NAT gateway restart. It is NOT
    recommended for production — the production environment composition below pins
    this to false and one NAT gateway per AZ.
  EOT
  type        = bool
  default     = false
}

variable "enable_flow_logs" {
  description = "Enable VPC Flow Logs to CloudWatch Logs. Required for the audit trail CloudOptix's own security story depends on, and for investigating any 'why did we get charged for this' cross-AZ or egress question."
  type        = bool
  default     = true
}

variable "flow_log_retention_days" {
  description = "CloudWatch Logs retention for VPC flow logs."
  type        = number
  default     = 30
}

variable "enable_vpc_endpoints" {
  description = <<-EOT
    Create interface/gateway VPC endpoints for S3, DynamoDB, ECR, Secrets Manager, STS
    and CloudWatch. A platform whose product recommends "add a gateway endpoint so this
    NAT gateway stops billing you for S3 traffic" (see
    internal/adapters/aws/executor's create_vpc_endpoint action) should not itself run
    without the endpoints it tells customers to add. Interface endpoints carry an
    hourly + per-GB cost of their own, so this is still a real trade-off in a
    low-traffic dev environment, but the default is on.
  EOT
  type        = bool
  default     = true
}

variable "tags" {
  description = "Tags applied to every resource in this module."
  type        = map(string)
  default     = {}
}
