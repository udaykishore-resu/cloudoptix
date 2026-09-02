output "event_bus_name" {
  value = aws_cloudwatch_event_bus.this.name
}

output "event_bus_arn" {
  value = aws_cloudwatch_event_bus.this.arn
}

output "queue_urls" {
  description = "Map of worker kind -> its SQS queue URL."
  value       = { for k, v in aws_sqs_queue.this : k => v.id }
}

output "queue_arns" {
  value = { for k, v in aws_sqs_queue.this : k => v.arn }
}

output "dlq_arns" {
  value = { for k, v in aws_sqs_queue.dlq : k => v.arn }
}

output "topic_arns" {
  description = "Map of notification topic name -> SNS topic ARN."
  value       = { for k, v in aws_sns_topic.this : k => v.arn }
}
