variable "name" {
  type = string
}

variable "environment" {
  type = string
}

variable "kubernetes_version" {
  description = "EKS control plane version. Pinned explicitly, never \"latest\" — an in-place upgrade should always be a deliberate variable change and apply, not an incidental side effect of a provider refresh."
  type        = string
  default     = "1.30"
}

variable "vpc_id" {
  type = string
}

variable "private_subnet_ids" {
  description = "Nodes and pods run here. Also where the cluster's ENIs for the control plane's cross-account elastic network interfaces attach."
  type        = list(string)
}

variable "public_subnet_ids" {
  description = "Only used for the public API endpoint's security group placement rationale and, if enabled, a public-facing NLB; nodes never run here."
  type        = list(string)
}

variable "endpoint_public_access" {
  description = "Whether the EKS API server endpoint is reachable from outside the VPC. true (dev/staging default, restricted by endpoint_public_access_cidrs) trades a small exposure for not needing a bastion/VPN for kubectl; production should set this false and require the private endpoint plus a VPN/Session-Manager path."
  type        = bool
  default     = true
}

variable "endpoint_public_access_cidrs" {
  type    = list(string)
  default = ["0.0.0.0/0"]
}

variable "cluster_log_types" {
  description = "EKS control-plane log types shipped to CloudWatch. All five by default — api/audit are the security-relevant ones; the observability module attaches retention to the log group these create."
  type        = list(string)
  default     = ["api", "audit", "authenticator", "controllerManager", "scheduler"]
}

# ---------------------------------------------------------------------------
# Node groups
# ---------------------------------------------------------------------------

variable "on_demand_instance_types" {
  description = "Instance types for the on-demand system node group, which runs anything that must not be interrupted mid-work: the migration Job, and (via a nodeSelector/toleration the chart sets) anything an operator explicitly pins off Spot."
  type        = list(string)
  default     = ["m6i.large"]
}

variable "on_demand_min_size" {
  type    = number
  default = 2
}

variable "on_demand_max_size" {
  type    = number
  default = 6
}

variable "on_demand_desired_size" {
  type    = number
  default = 2
}

variable "spot_instance_types" {
  description = <<-EOT
    Instance types for the Spot worker node group, spread across several
    families/sizes deliberately — Spot capacity is per (instance type, AZ)
    pool, and a single-type request is what actually causes interruption
    storms in practice; diversifying across types with similar vCPU/memory
    ratios gives the Spot allocator more pools to satisfy the request from.

    This is also where CloudOptix eats its own dog food on the flip side of
    terraform/demo's wastefulness story: the discovery/optimization/
    validation workers are stateless, retryable, queue-driven consumers
    (see internal/adapters/events) — exactly the workload profile Spot is
    for, and exactly what the platform's own enable_spot recommendation
    (internal/adapters/aws/executor's action of the same name) tells
    customers to do with equivalent workloads in their own accounts.
  EOT
  type = list(string)
  default = ["m6i.large", "m6a.large", "m5.large", "m5a.large"]
}

variable "spot_min_size" {
  type    = number
  default = 1
}

variable "spot_max_size" {
  type    = number
  default = 20
}

variable "spot_desired_size" {
  type    = number
  default = 2
}

variable "node_disk_size_gb" {
  type    = number
  default = 50
}

# ---------------------------------------------------------------------------
# Autoscaling: Karpenter (preferred) or Cluster Autoscaler
# ---------------------------------------------------------------------------

variable "autoscaler" {
  description = <<-EOT
    "karpenter" (default) or "cluster-autoscaler" or "none". Karpenter
    provisions right-sized nodes directly against EC2 rather than scaling a
    fixed-shape managed node group, which is the better fit once the Spot
    node group's instance-type diversity above stops being enough — but it
    needs its own IAM role/instance profile and a NodePool CRD the
    aws-load-balancer-controller-style helm_release below installs.
    cluster-autoscaler is the simpler, more conservative choice: it only
    ever scales the exact managed node groups this module already defines,
    which is easier to reason about for a first production rollout.
  EOT
  type = string
  default = "karpenter"
  validation {
    condition     = contains(["karpenter", "cluster-autoscaler", "none"], var.autoscaler)
    error_message = "autoscaler must be karpenter, cluster-autoscaler, or none."
  }
}

variable "karpenter_version" {
  type    = string
  default = "1.0.6"
}

variable "cluster_autoscaler_version" {
  type    = string
  default = "9.37.0" # chart version
}

variable "aws_load_balancer_controller_version" {
  type    = string
  default = "1.8.1" # chart version
}

variable "install_aws_load_balancer_controller" {
  type    = bool
  default = true
}

variable "addons" {
  description = "EKS-managed addons and the versions to pin (empty string = let AWS pick the default compatible version for kubernetes_version, resolved at apply time — acceptable for vpc-cni/coredns/kube-proxy, which AWS keeps in lockstep with the control plane version)."
  type = object({
    vpc_cni    = optional(string, "")
    coredns    = optional(string, "")
    kube_proxy = optional(string, "")
    ebs_csi    = optional(string, "")
  })
  default = {}
}

variable "tags" {
  type    = map(string)
  default = {}
}
