variable "region" {
  type    = string
  default = "us-east-1"
}

variable "name" {
  type    = string
  default = "cloudoptix-demo"
}

variable "enable_eks" {
  description = "The EKS control plane alone costs ~$73/mo regardless of node size — turn this off if you only need the non-Kubernetes pathologies for a shorter/cheaper demo."
  type        = bool
  default     = true
}

variable "enable_cross_az_pair" {
  description = "Two t3.micro instances in different AZs, chatting constantly, to generate real cross-AZ data transfer charges. Cheap (~$15/mo of instances) but easy to skip if you don't need this specific pathology."
  type        = bool
  default     = true
}

# A trivial key pair is intentionally NOT created by this configuration —
# every instance here uses user_data only and nobody needs to SSH in for a
# discovery demo. If you need shell access for debugging, use SSM Session
# Manager (the instance profile below already grants it) rather than
# provisioning a keypair whose private key would need to live somewhere.
