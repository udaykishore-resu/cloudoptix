# network

Provisions the VPC every other platform module runs inside: three tiers
(public, private, database) across up to six Availability Zones (three by
default), NAT egress, gateway/interface VPC endpoints, and flow logs.

## Why three tiers

- **public** — the ALB/NLB the aws-load-balancer-controller creates, and
  nothing else. Tagged `kubernetes.io/role/elb` so the controller
  auto-discovers it.
- **private** — EKS nodes and pods. Tagged `kubernetes.io/role/internal-elb`
  for internal load balancers. Routes to the internet through NAT.
- **database** — RDS/Aurora and ElastiCache subnet groups. No route to the
  internet at all (no NAT, no IGW route) — the tier has no legitimate reason
  to originate outbound traffic, so it gets no path to, rather than a
  security-group rule against, doing so.

## The NAT gateway decision

`single_nat_gateway` is the one variable in this module worth reading
carefully before you set it. A NAT gateway bills per-hour and per-GB
processed whether or not anything behind it is doing useful work — which is
exactly the shape of waste CloudOptix's own recommendation engine flags
(`create_vpc_endpoint`, `remove_nat_gateway` in
`internal/adapters/aws/executor`). `terraform/demo` deliberately builds
three NAT gateways with no S3 endpoint so the platform has a real instance
of this to find.

- `single_nat_gateway = true` (dev, staging default): one NAT gateway,
  shared by every AZ's private subnets. Cheaper, and an acceptable
  single-point-of-failure when nothing pages anyone over it.
- `single_nat_gateway = false` (production default): one NAT gateway per AZ,
  so losing an AZ never removes egress from the other two.

## VPC endpoints

`enable_vpc_endpoints` (on by default) creates gateway endpoints for S3 and
DynamoDB (free) and interface endpoints for ECR (image pulls), Secrets
Manager (config secret resolution), STS (every AssumeRole call — both the
platform's own identity and every customer credential broker call in
`internal/adapters/aws/sts`), CloudWatch, and Logs. A platform whose product
recommends "add an S3 gateway endpoint, it'll pay for itself" should use one
itself.

## Flow logs

Enabled by default, to CloudWatch Logs with a configurable retention. This
is both a security control (the audit trail for "what talked to what") and
the raw data behind any "why are we paying so much for NAT data processing"
investigation — the same question `remove_nat_gateway`'s savings estimate is
built on.

## Inputs / outputs

See `variables.tf` and `outputs.tf` — every variable and output carries its
own doc comment; this README covers the decisions, not a restatement of the
schema.
