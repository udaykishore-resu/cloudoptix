variable "region" {
  type    = string
  default = "us-east-1"
}

variable "name" {
  description = "Resource name prefix for this environment."
  type        = string
  default     = "cloudoptix-dev"
}
