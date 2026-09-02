data "aws_availability_zones" "available" {
  state = "available"
}

data "aws_ami" "al2023" {
  most_recent = true
  owners      = ["amazon"]
  filter {
    name   = "name"
    values = ["al2023-ami-*-x86_64"]
  }
}

# ---------------------------------------------------------------------------
# Network: reuses the platform network module, but with the two switches
# flipped the "wrong" way on purpose — single_nat_gateway=false without
# enable_vpc_endpoints is exactly "three NAT gateways with no S3 endpoint",
# the pathology this estate is required to demonstrate. Everything else
# about the module (flow logs, tiering) stays on; this demo is about cost
# waste, not about also skipping security basics that aren't the point.
# ---------------------------------------------------------------------------

module "network" {
  source = "../modules/network"

  name                 = var.name
  environment          = "demo"
  az_count             = 3
  single_nat_gateway   = false # three NAT gateways...
  enable_vpc_endpoints = false # ...paying full price for S3/DynamoDB traffic that a gateway endpoint would make free
  enable_flow_logs     = true

  tags = { Pathology = "three-nat-gateways-no-s3-endpoint" }
}

resource "aws_security_group" "demo" {
  name        = "${var.name}-instances"
  description = "Demo estate instances — outbound only, nothing needs to be reachable from outside the VPC for a discovery demo."
  vpc_id      = module.network.vpc_id

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
  # Allow the two chatter instances (see below) to reach each other on the
  # port their loop script uses.
  ingress {
    from_port = 5000
    to_port   = 5000
    protocol  = "tcp"
    self      = true
  }

  tags = { Name = "${var.name}-instances" }
}

resource "aws_iam_role" "instance" {
  name = "${var.name}-instance"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{ Effect = "Allow", Principal = { Service = "ec2.amazonaws.com" }, Action = "sts:AssumeRole" }]
  })
}

resource "aws_iam_role_policy_attachment" "instance_ssm" {
  role       = aws_iam_role.instance.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore" # Session Manager access, no keypair needed — see variables.tf
}

resource "aws_iam_instance_profile" "instance" {
  name = "${var.name}-instance"
  role = aws_iam_role.instance.name
}

# ---------------------------------------------------------------------------
# Oversized, chronically-idle compute — the smallest instance type that
# still reads as "oversized" against a workload that does nothing: t3.large
# (2 vCPU / 8GB) running only a sleep loop. Matches
# internal/adapters/awssim/waste.go's idleOversizedThreshold story (a
# CPUBaselineP50 well under 10%).
# ---------------------------------------------------------------------------

resource "aws_instance" "oversized_idle" {
  ami                    = data.aws_ami.al2023.id
  instance_type          = "t3.large"
  subnet_id              = module.network.private_subnet_ids[0]
  vpc_security_group_ids = [aws_security_group.demo.id]
  iam_instance_profile   = aws_iam_instance_profile.instance.name

  user_data = <<-EOT
    #!/bin/bash
    # Deliberately does nothing CPU-intensive — this instance exists to be
    # an oversized, idle rightsizing candidate. Real workload: none.
    while true; do sleep 3600; done
  EOT

  tags = { Name = "${var.name}-oversized-idle", Pathology = "oversized-idle-compute" }
}

# Old-generation instance type still running — t2 predates t3's burstable
# credit model and Nitro; a m5/t3 successor is strictly better and usually
# cheaper. t2.medium is the smallest type that still reads clearly as
# "previous generation" (t2.micro/small are so cheap the absolute dollar
# waste is unconvincing as a demo).
resource "aws_instance" "old_generation" {
  ami                    = data.aws_ami.al2023.id
  instance_type          = "t2.medium"
  subnet_id              = module.network.private_subnet_ids[0]
  vpc_security_group_ids = [aws_security_group.demo.id]
  iam_instance_profile   = aws_iam_instance_profile.instance.name

  user_data = <<-EOT
    #!/bin/bash
    while true; do sleep 3600; done
  EOT

  tags = { Name = "${var.name}-old-generation", Pathology = "old-generation-compute" }
}

# A permanently-stopped instance, kept only so its attached volume (below)
# demonstrates "volume attached only to a stopped instance" rather than
# "volume never attached to anything" (that pathology is
# aws_ebs_volume.unattached, separately).
resource "aws_instance" "stopped" {
  ami                    = data.aws_ami.al2023.id
  instance_type          = "t3.micro"
  subnet_id              = module.network.private_subnet_ids[0]
  vpc_security_group_ids = [aws_security_group.demo.id]
  iam_instance_profile   = aws_iam_instance_profile.instance.name

  tags = { Name = "${var.name}-stopped", Pathology = "stopped-instance-with-attached-volume" }

  # Terraform provisions an instance in the running state; there is no
  # native resource attribute to launch it pre-stopped. Stop it once,
  # out-of-band, after apply:
  #   aws ec2 stop-instances --instance-ids $(terraform output -raw stopped_instance_id)
  # A null_resource/local-exec auto-stop was deliberately left out — this
  # module should never assume the AWS CLI or working AWS credentials are
  # available in whatever environment happens to run `terraform apply`.
}

# ---------------------------------------------------------------------------
# EBS
# ---------------------------------------------------------------------------

resource "aws_ebs_volume" "unattached" {
  availability_zone = module.network.availability_zones[0]
  size              = 20 # smallest size that still reads as a deliberate provision, not a rounding artifact
  type              = "gp2" # gp2, not gp3, doubles as the gp2->gp3 pathology on top of being unattached
  tags              = { Name = "${var.name}-unattached", Pathology = "unattached-volume-and-gp2" }
}

resource "aws_ebs_volume" "stopped_instance_volume" {
  availability_zone = module.network.availability_zones[0]
  size              = 20
  type              = "gp3"
  tags              = { Name = "${var.name}-stopped-instance-volume", Pathology = "volume-on-stopped-instance" }
}

resource "aws_volume_attachment" "stopped_instance_volume" {
  device_name = "/dev/sdf"
  volume_id   = aws_ebs_volume.stopped_instance_volume.id
  instance_id = aws_instance.stopped.id
}

resource "aws_ebs_snapshot" "stale" {
  volume_id   = aws_ebs_volume.unattached.id
  description = "Demo stale snapshot — nothing restores from this; it exists only to age past any reasonable retention window."
  tags        = { Name = "${var.name}-stale-snapshot", Pathology = "stale-snapshot" }
}

# ---------------------------------------------------------------------------
# Unassociated Elastic IP
# ---------------------------------------------------------------------------

resource "aws_eip" "unattached" {
  domain = "vpc"
  tags   = { Name = "${var.name}-unattached-eip", Pathology = "unattached-elastic-ip" }
}

# ---------------------------------------------------------------------------
# S3 bucket with no lifecycle policy — contrast with terraform/modules/storage,
# which always attaches one. No versioning, no lifecycle rule, no
# abort-incomplete-multipart-upload rule.
# ---------------------------------------------------------------------------

resource "aws_s3_bucket" "no_lifecycle" {
  bucket = "${var.name}-no-lifecycle-${data.aws_caller_identity.current.account_id}"
  tags   = { Pathology = "no-lifecycle-policy" }
}

resource "aws_s3_bucket_public_access_block" "no_lifecycle" {
  # Wasteful, not insecure — public access stays blocked even in the demo
  # estate; CloudOptix's cost story and its security story are different
  # products, and this module is only demonstrating the former.
  bucket                  = aws_s3_bucket.no_lifecycle.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

data "aws_caller_identity" "current" {}

# ---------------------------------------------------------------------------
# RDS — oversized primary, idle replica. db.t3.medium is the smallest
# instance class Aurora and standard RDS both support, so it is used for
# both members; the pathology is the *existence* of a replica nothing
# reads from and a primary sized without ever having been measured, not the
# absolute instance size.
# ---------------------------------------------------------------------------

resource "aws_db_subnet_group" "demo" {
  name       = "${var.name}-db"
  subnet_ids = module.network.database_subnet_ids
}

resource "aws_security_group" "rds" {
  name        = "${var.name}-rds"
  description = "Postgres from the demo estate's own instances only."
  vpc_id      = module.network.vpc_id
  ingress {
    from_port       = 5432
    to_port         = 5432
    protocol        = "tcp"
    security_groups = [aws_security_group.demo.id]
  }
  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
  tags = { Name = "${var.name}-rds" }
}

resource "aws_db_instance" "oversized_primary" {
  identifier     = "${var.name}-primary"
  engine         = "postgres"
  engine_version = "16.4"
  instance_class = "db.t3.medium"
  allocated_storage = 20

  db_name  = "demo"
  username = "demo"
  # A random, Terraform-managed password is acceptable here specifically
  # because this is disposable demo infrastructure with no real data —
  # terraform/modules/rds's production-grade module uses
  # manage_master_user_password instead precisely to avoid this; see that
  # module's README for why the distinction matters in a real environment.
  password = random_password.rds.result

  db_subnet_group_name   = aws_db_subnet_group.demo.name
  vpc_security_group_ids = [aws_security_group.rds.id]

  storage_encrypted   = true
  skip_final_snapshot = true # disposable — see README's "tearing down" section
  deletion_protection = false
  publicly_accessible = false
  multi_az            = false # a Multi-AZ standby would itself be a cost/complexity question this demo isn't trying to raise — the pathology here is the read replica below, not this flag

  tags = { Name = "${var.name}-oversized-primary", Pathology = "oversized-rds-primary" }
}

resource "random_password" "rds" {
  length  = 24
  special = false
}

resource "aws_db_instance" "idle_replica" {
  identifier          = "${var.name}-idle-replica"
  replicate_source_db = aws_db_instance.oversized_primary.identifier
  instance_class      = "db.t3.medium"

  publicly_accessible = false
  skip_final_snapshot = true
  deletion_protection = false

  # No application ever connects to this replica — that's the pathology.
  # aws_db_instance provides no "traffic" knob to fake; the point is
  # structural (a replica exists, nothing reads from it), which CloudOptix's
  # discovery + CloudWatch ReadIOPS/DatabaseConnections metrics over time
  # are what actually detects in a real account.
  tags = { Name = "${var.name}-idle-replica", Pathology = "idle-rds-read-replica" }
}

# ---------------------------------------------------------------------------
# Cross-AZ chatter — two t3.micro instances, placed in different AZs on
# purpose, each pinging the other in a tight loop. Cross-AZ traffic bills
# at AWS's inter-AZ data transfer rate on both sides of the conversation;
# same-AZ placement (or a topology-aware architecture) would eliminate it
# entirely for a workload this chatty.
# ---------------------------------------------------------------------------

resource "aws_instance" "chatty_a" {
  count                  = var.enable_cross_az_pair ? 1 : 0
  ami                    = data.aws_ami.al2023.id
  instance_type          = "t3.micro"
  subnet_id              = module.network.private_subnet_ids[0] # AZ 0
  vpc_security_group_ids = [aws_security_group.demo.id]
  iam_instance_profile   = aws_iam_instance_profile.instance.name

  user_data = <<-EOT
    #!/bin/bash
    yum install -y nmap-ncat
    # Just listens and discards — chatty_b (a different AZ) is the side
    # that generates the repeating cross-AZ connections.
    while true; do nc -l -p 5000 >/dev/null 2>&1; done
  EOT

  tags = { Name = "${var.name}-chatty-a", Pathology = "cross-az-chatter" }
}

resource "aws_instance" "chatty_b" {
  count                  = var.enable_cross_az_pair ? 1 : 0
  ami                    = data.aws_ami.al2023.id
  instance_type          = "t3.micro"
  subnet_id              = module.network.private_subnet_ids[length(module.network.private_subnet_ids) > 1 ? 1 : 0] # a different AZ
  vpc_security_group_ids = [aws_security_group.demo.id]
  iam_instance_profile   = aws_iam_instance_profile.instance.name

  user_data = <<-EOT
    #!/bin/bash
    yum install -y nmap-ncat
    while true; do echo ping | nc -w1 ${aws_instance.chatty_a[0].private_ip} 5000 2>/dev/null; sleep 1; done
  EOT

  tags = { Name = "${var.name}-chatty-b", Pathology = "cross-az-chatter" }

  depends_on = [aws_instance.chatty_a]
}

# ---------------------------------------------------------------------------
# EKS with an over-declared-requests pod — see manifests/oversized-pod-
# requests.yaml, applied post-cluster (not by Terraform — this repo's
# convention keeps Kubernetes-object-level manifests separate from
# infrastructure provisioning; see the deployments/k8s README for why).
# ---------------------------------------------------------------------------

module "eks" {
  count  = var.enable_eks ? 1 : 0
  source = "../modules/eks"

  name                   = var.name
  environment            = "demo"
  vpc_id                 = module.network.vpc_id
  private_subnet_ids     = module.network.private_subnet_ids
  public_subnet_ids      = module.network.public_subnet_ids
  endpoint_public_access = true

  # A single, small On-Demand node group only — no Spot group, no Karpenter
  # — is itself part of the pathology: a workload's pod resource requests
  # (see manifests/oversized-pod-requests.yaml) are what force this group
  # larger than the cluster's real usage needs, exactly the "EKS node group
  # with wildly over-declared pod requests" this estate is required to
  # demonstrate.
  on_demand_instance_types = ["t3.medium"]
  on_demand_min_size       = 1
  on_demand_max_size       = 3
  on_demand_desired_size   = 2

  spot_instance_types = ["t3.medium"]
  spot_min_size       = 0
  spot_max_size       = 0
  spot_desired_size   = 0

  autoscaler                           = "cluster-autoscaler"
  install_aws_load_balancer_controller = false

  tags = { Pathology = "eks-oversized-pod-requests" }
}

# ---------------------------------------------------------------------------
# Infinite log retention — retention_in_days is deliberately never set.
# CloudWatch's own default for an unset retention is "Never expire."
# ---------------------------------------------------------------------------

resource "aws_cloudwatch_log_group" "infinite_retention" {
  name = "/cloudoptix-demo/${var.name}/never-expires"
  tags = { Pathology = "infinite-log-retention" }
}
