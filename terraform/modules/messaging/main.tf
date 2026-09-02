locals {
  common_tags = merge(var.tags, {
    Module      = "messaging"
    Environment = var.environment
    ManagedBy   = "terraform"
  })
}

# ---------------------------------------------------------------------------
# EventBridge — the domain-event bus. internal/adapters/events/eventbridge.go
# publishes here; rules below route matching detail-types to each worker
# kind's own SQS queue, giving every subscriber an independent, replayable
# position in the stream rather than a shared competing-consumer queue.
# ---------------------------------------------------------------------------

resource "aws_cloudwatch_event_bus" "this" {
  name = "${var.name}-events"
  tags = local.common_tags
}

# Anything EventBridge itself cannot deliver to a target (the target queue
# is over capacity, permissions were revoked, etc.) lands here instead of
# being silently dropped — a bus-level backstop distinct from each queue's
# own per-message DLQ below.
resource "aws_sqs_queue" "eventbridge_dlq" {
  name                      = "${var.name}-eventbridge-dlq"
  message_retention_seconds = var.dlq_message_retention_seconds
  kms_master_key_id         = var.kms_key_arn
  tags                      = local.common_tags
}

# ---------------------------------------------------------------------------
# Per-worker-kind queues
# ---------------------------------------------------------------------------

resource "aws_sqs_queue" "dlq" {
  for_each                  = var.queues
  name                      = "${var.name}-${each.value}-dlq"
  message_retention_seconds = var.dlq_message_retention_seconds
  kms_master_key_id         = var.kms_key_arn
  tags                      = merge(local.common_tags, { Queue = each.value })
}

resource "aws_sqs_queue" "this" {
  for_each                   = var.queues
  name                       = "${var.name}-${each.value}"
  visibility_timeout_seconds = var.visibility_timeout_seconds
  message_retention_seconds  = var.message_retention_seconds
  kms_master_key_id          = var.kms_key_arn

  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.dlq[each.value].arn
    maxReceiveCount     = var.max_receive_count
  })

  tags = merge(local.common_tags, { Queue = each.value })
}

# The DLQ's own redrive-allow-policy: only the matching main queue may
# redrive messages back out of its dead-letter queue (via
# aws sqs start-message-move-task), not an unrelated queue in the account.
resource "aws_sqs_queue_redrive_allow_policy" "dlq" {
  for_each  = var.queues
  queue_url = aws_sqs_queue.dlq[each.value].id
  redrive_allow_policy = jsonencode({
    redrivePermission = "byQueue"
    sourceQueueArns   = [aws_sqs_queue.this[each.value].arn]
  })
}

resource "aws_sqs_queue_policy" "allow_eventbridge" {
  for_each  = var.queues
  queue_url = aws_sqs_queue.this[each.value].id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid       = "AllowEventBridgeDelivery"
      Effect    = "Allow"
      Principal = { Service = "events.amazonaws.com" }
      Action    = "sqs:SendMessage"
      Resource  = aws_sqs_queue.this[each.value].arn
      Condition = {
        ArnEquals = { "aws:SourceArn" = aws_cloudwatch_event_rule.route[each.value].arn }
      }
    }]
  })
}

# One rule per queue, matching events tagged for that worker kind. Producers
# (internal/adapters/events/eventbridge.go callers) set detail-type to
# "cloudoptix.<kind>" — discovery.requested, optimization.requested,
# automation.plan_approved, validation.requested, notification.requested —
# so routing is a straightforward prefix match per kind rather than a
# maintained list of every event name.
resource "aws_cloudwatch_event_rule" "route" {
  for_each       = var.queues
  name           = "${var.name}-route-${each.value}"
  event_bus_name = aws_cloudwatch_event_bus.this.name
  description    = "Routes cloudoptix.${each.value}.* domain events to the ${each.value} worker's queue."

  event_pattern = jsonencode({
    source = ["cloudoptix.${each.value}"]
  })

  tags = local.common_tags
}

resource "aws_cloudwatch_event_target" "route" {
  for_each       = var.queues
  rule           = aws_cloudwatch_event_rule.route[each.value].name
  event_bus_name = aws_cloudwatch_event_bus.this.name
  arn            = aws_sqs_queue.this[each.value].arn

  dead_letter_config {
    arn = aws_sqs_queue.eventbridge_dlq.arn
  }

  retry_policy {
    maximum_event_age_in_seconds = 3600
    maximum_retry_attempts       = 3
  }
}

# ---------------------------------------------------------------------------
# SNS topics — outbound notification fan-out (internal/adapters/notify).
# Terraform provisions the topics only; per-environment subscriptions
# (a Slack webhook, a PagerDuty integration, an email address) are added
# out-of-band by an operator or a separate, environment-specific
# subscription stack, for the same "no secret/endpoint value lives in this
# module" reason security's Secrets Manager containers are placeholders.
# ---------------------------------------------------------------------------

resource "aws_sns_topic" "this" {
  for_each          = var.notification_topics
  name              = "${var.name}-${each.value}"
  kms_master_key_id = var.kms_key_arn
  tags              = merge(local.common_tags, { Topic = each.value })
}
