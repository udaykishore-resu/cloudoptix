variable "name" {
  type = string
}

variable "environment" {
  type = string
}

variable "kms_key_arn" {
  description = "KMS key encrypting queue and topic contents at rest."
  type        = string
}

variable "queues" {
  description = <<-EOT
    One SQS queue per worker kind (see helm/cloudoptix's five worker
    Deployments and internal/infrastructure/config.WorkerConfig), each
    getting its own dead-letter queue and redrive policy —
    internal/adapters/events/sqs.go relies entirely on the queue's native
    RedrivePolicy for dead-lettering; it does not implement that logic
    itself (see that package's doc comment).
  EOT
  type    = set(string)
  default = ["discovery", "optimization", "automation", "validation", "notification"]
}

variable "max_receive_count" {
  description = "Deliveries before a message moves to its queue's dead-letter queue."
  type        = number
  default     = 5
}

variable "visibility_timeout_seconds" {
  description = "Should exceed the slowest realistic handler for the longest-running queue (automation: an execution plan's Apply step can call a mutating AWS API and wait for it to settle). 300s gives 5x the AWS SDK's own default per-call timeout headroom."
  type        = number
  default     = 300
}

variable "message_retention_seconds" {
  description = "How long an undelivered message stays in the main queue. 4 days is SQS's own default and comfortably covers a worker outage over a weekend."
  type        = number
  default     = 345600
}

variable "dlq_message_retention_seconds" {
  description = "Dead-letter queues get the maximum SQS allows (14 days) — a message here represents a bug or a persistently failing AWS call, and needs enough time for an SRE to actually notice and investigate before it silently expires."
  type        = number
  default     = 1209600
}

variable "notification_topics" {
  description = "SNS topics for outbound platform notifications — see internal/adapters/notify. Named for what they carry, not for a specific channel, since a topic can fan out to email/Slack/PagerDuty subscriptions independently of this module."
  type        = set(string)
  default     = ["approval-required", "execution-completed", "operational-alerts"]
}

variable "tags" {
  type    = map(string)
  default = {}
}
