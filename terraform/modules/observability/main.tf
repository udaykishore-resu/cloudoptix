locals {
  common_tags = merge(var.tags, {
    Module      = "observability"
    Environment = var.environment
    ManagedBy   = "terraform"
  })

  region = data.aws_region.current.name
}

data "aws_region" "current" {}

# ---------------------------------------------------------------------------
# Log groups
# ---------------------------------------------------------------------------

# Application log group. In the common deployment (stdout -> Fluent Bit/
# CloudWatch agent DaemonSet -> CloudWatch Logs) this is the destination
# name the DaemonSet's own config should target; it exists here, rather than
# being auto-created on first write, so retention is enforced from the
# group's very first log event instead of defaulting to "never expire" for
# however long it takes someone to notice and set it (exactly the
# infinite-log-retention pathology terraform/demo provisions on purpose).
resource "aws_cloudwatch_log_group" "application" {
  name              = "/cloudoptix/${var.name}/application"
  retention_in_days = var.log_retention_days
  tags              = local.common_tags
}

# EKS control-plane log group (api/audit/authenticator/controllerManager/
# scheduler) — the eks module enables these log types on the cluster; AWS
# names the group deterministically as /aws/eks/<cluster>/cluster, so this
# resource only exists to attach a retention policy to a group the eks
# module's aws_eks_cluster resource causes to be created.
resource "aws_cloudwatch_log_group" "eks_control_plane" {
  count             = var.eks_cluster_name != "" ? 1 : 0
  name              = "/aws/eks/${var.eks_cluster_name}/cluster"
  retention_in_days = var.log_retention_days
  tags              = local.common_tags
}

# ---------------------------------------------------------------------------
# Alarms
# ---------------------------------------------------------------------------

resource "aws_cloudwatch_metric_alarm" "rds_cpu" {
  count               = var.rds_cluster_identifier != "" ? 1 : 0
  alarm_name          = "${var.name}-rds-cpu-high"
  alarm_description   = "Aurora cluster average CPU above 80% for 10 minutes."
  namespace           = "AWS/RDS"
  metric_name         = "CPUUtilization"
  dimensions          = { DBClusterIdentifier = var.rds_cluster_identifier }
  statistic           = "Average"
  period              = 300
  evaluation_periods  = 2
  threshold           = 80
  comparison_operator = "GreaterThanThreshold"
  treat_missing_data  = "notBreaching"
  alarm_actions       = var.alarm_sns_topic_arns
  ok_actions          = var.alarm_sns_topic_arns
  tags                = local.common_tags
}

resource "aws_cloudwatch_metric_alarm" "rds_storage_low" {
  count               = var.rds_cluster_identifier != "" ? 1 : 0
  alarm_name          = "${var.name}-rds-storage-low"
  alarm_description   = "Aurora cluster free local storage below 2GiB — approaching autoscale storage limits or a runaway write pattern."
  namespace           = "AWS/RDS"
  metric_name         = "FreeLocalStorage"
  dimensions          = { DBClusterIdentifier = var.rds_cluster_identifier }
  statistic           = "Minimum"
  period              = 300
  evaluation_periods  = 2
  threshold           = 2147483648 # 2 GiB, bytes
  comparison_operator = "LessThanThreshold"
  treat_missing_data  = "notBreaching"
  alarm_actions       = var.alarm_sns_topic_arns
  ok_actions          = var.alarm_sns_topic_arns
  tags                = local.common_tags
}

resource "aws_cloudwatch_metric_alarm" "redis_cpu" {
  count               = var.redis_replication_group_id != "" ? 1 : 0
  alarm_name          = "${var.name}-redis-cpu-high"
  alarm_description   = "ElastiCache primary CPU above 75% for 10 minutes."
  namespace           = "AWS/ElastiCache"
  metric_name         = "EngineCPUUtilization"
  dimensions          = { ReplicationGroupId = var.redis_replication_group_id }
  statistic           = "Average"
  period              = 300
  evaluation_periods  = 2
  threshold           = 75
  comparison_operator = "GreaterThanThreshold"
  treat_missing_data  = "notBreaching"
  alarm_actions       = var.alarm_sns_topic_arns
  ok_actions          = var.alarm_sns_topic_arns
  tags                = local.common_tags
}

resource "aws_cloudwatch_metric_alarm" "redis_evictions" {
  count               = var.redis_replication_group_id != "" ? 1 : 0
  alarm_name          = "${var.name}-redis-evictions"
  alarm_description   = "ElastiCache is evicting keys under memory pressure — cloudoptix_cache_misses_total is about to climb because of capacity, not cold cache."
  namespace           = "AWS/ElastiCache"
  metric_name         = "Evictions"
  dimensions          = { ReplicationGroupId = var.redis_replication_group_id }
  statistic           = "Sum"
  period              = 300
  evaluation_periods  = 1
  threshold           = 0
  comparison_operator = "GreaterThanThreshold"
  treat_missing_data  = "notBreaching"
  alarm_actions       = var.alarm_sns_topic_arns
  ok_actions          = var.alarm_sns_topic_arns
  tags                = local.common_tags
}

resource "aws_cloudwatch_metric_alarm" "alb_5xx" {
  count               = var.alb_arn_suffix != "" ? 1 : 0
  alarm_name          = "${var.name}-alb-5xx-rate"
  alarm_description   = "ALB-generated (not app-generated) 5xx responses above 10 in 5 minutes — a load balancer / target health problem, distinct from the app's own 5xx rate which the PrometheusRule SLO alerts already cover."
  namespace           = "AWS/ApplicationELB"
  metric_name         = "HTTPCode_ELB_5XX_Count"
  dimensions          = { LoadBalancer = var.alb_arn_suffix }
  statistic           = "Sum"
  period              = 300
  evaluation_periods  = 1
  threshold           = 10
  comparison_operator = "GreaterThanThreshold"
  treat_missing_data  = "notBreaching"
  alarm_actions       = var.alarm_sns_topic_arns
  ok_actions          = var.alarm_sns_topic_arns
  tags                = local.common_tags
}

# Any message in a dead-letter queue is, by definition, not a threshold
# judgment call — it already failed max_receive_count times. One is worth
# paging on.
resource "aws_cloudwatch_metric_alarm" "dlq_not_empty" {
  for_each            = var.sqs_dlq_names
  alarm_name          = "${var.name}-dlq-${each.key}-not-empty"
  alarm_description   = "The ${each.key} dead-letter queue has at least one message — something failed processing max_receive_count times and needs a human."
  namespace           = "AWS/SQS"
  metric_name         = "ApproximateNumberOfMessagesVisible"
  dimensions          = { QueueName = each.value }
  statistic           = "Maximum"
  period              = 300
  evaluation_periods  = 1
  threshold           = 0
  comparison_operator = "GreaterThanThreshold"
  treat_missing_data  = "notBreaching"
  alarm_actions       = var.alarm_sns_topic_arns
  ok_actions          = var.alarm_sns_topic_arns
  tags                = local.common_tags
}

# ---------------------------------------------------------------------------
# Dashboard — the platform's own SLIs: request latency/error rate, discovery
# coverage, AWS API health, LLM cost, and the infra metrics above, on one
# screen. Widget metric names match internal/infrastructure/telemetry/
# metrics.go exactly; CloudWatch reads these via the ADOT collector's
# Prometheus-to-CloudWatch-EMF pipeline (see the otel collector config
# published below), not by scraping /metrics directly.
# ---------------------------------------------------------------------------

resource "aws_cloudwatch_dashboard" "platform" {
  dashboard_name = "${var.name}-platform-slis"
  dashboard_body = jsonencode({
    widgets = [
      {
        type = "metric", x = 0, y = 0, width = 12, height = 6,
        properties = {
          title = "API request rate & error rate"
          view  = "timeSeries"
          region = local.region
          metrics = [
            ["CloudOptix", "cloudoptix_http_requests_total", { stat = "Sum", label = "requests" }],
          ]
        }
      },
      {
        type = "metric", x = 12, y = 0, width = 12, height = 6,
        properties = {
          title  = "API request latency (p50/p99)"
          view   = "timeSeries"
          region = local.region
          metrics = [
            ["CloudOptix", "cloudoptix_http_request_duration_seconds", { stat = "p50", label = "p50" }],
            ["CloudOptix", "cloudoptix_http_request_duration_seconds", { stat = "p99", label = "p99" }],
          ]
        }
      },
      {
        type = "metric", x = 0, y = 6, width = 8, height = 6,
        properties = {
          title  = "Discovery coverage ratio"
          view   = "timeSeries"
          region = local.region
          metrics = [["CloudOptix", "cloudoptix_discovery_coverage_ratio", { stat = "Average" }]]
        }
      },
      {
        type = "metric", x = 8, y = 6, width = 8, height = 6,
        properties = {
          title  = "AWS API failures & throttles"
          view   = "timeSeries"
          region = local.region
          metrics = [
            ["CloudOptix", "cloudoptix_aws_api_failures_total", { stat = "Sum", label = "failures" }],
            ["CloudOptix", "cloudoptix_aws_api_throttles_total", { stat = "Sum", label = "throttles" }],
          ]
        }
      },
      {
        type = "metric", x = 16, y = 6, width = 8, height = 6,
        properties = {
          title  = "LLM spend (USD)"
          view   = "timeSeries"
          region = local.region
          metrics = [["CloudOptix", "cloudoptix_llm_cost_usd_total", { stat = "Sum" }]]
        }
      },
      {
        type = "metric", x = 0, y = 12, width = 12, height = 6,
        properties = {
          title  = "Aurora CPU / connections"
          view   = "timeSeries"
          region = local.region
          metrics = var.rds_cluster_identifier != "" ? [
            ["AWS/RDS", "CPUUtilization", "DBClusterIdentifier", var.rds_cluster_identifier],
          ] : []
        }
      },
      {
        type = "metric", x = 12, y = 12, width = 12, height = 6,
        properties = {
          title  = "Worker queue depth"
          view   = "timeSeries"
          region = local.region
          metrics = [["CloudOptix", "cloudoptix_worker_queue_depth", { stat = "Maximum" }]]
        }
      },
    ]
  })
}

# ---------------------------------------------------------------------------
# OTel collector config — published to SSM Parameter Store; the ADOT
# collector Deployment (started by helm/cloudoptix as a sidecar or, more
# commonly, a shared collector Deployment in-cluster) reads it via the AWS
# SSM config provider (`${env:AWS_REGION}` + this parameter name), so a
# config change here does not require rebuilding the collector's image or
# touching the chart. Receives OTLP from internal/infrastructure/telemetry's
# tracer (see TelemetryConfig.OTLPEndpoint) and Prometheus scrape from
# every pod's /metrics; exports both to CloudWatch (EMF for metrics, X-Ray
# for traces when enable_xray is set) and to the OTLPEndpoint's own
# downstream if one is configured outside AWS.
# ---------------------------------------------------------------------------

resource "aws_ssm_parameter" "otel_collector_config" {
  name = "/cloudoptix/${var.name}/otel-collector-config"
  type = "String"
  tier = "Advanced" # collector config comfortably exceeds the 4KB Standard-tier limit
  value = yamlencode({
    receivers = {
      otlp = {
        protocols = { grpc = { endpoint = "0.0.0.0:4317" }, http = { endpoint = "0.0.0.0:4318" } }
      }
      prometheus = {
        config = {
          scrape_configs = [{
            job_name        = "cloudoptix"
            scrape_interval = "30s"
            kubernetes_sd_configs = [{ role = "pod" }]
          }]
        }
      }
    }
    processors = {
      batch = {}
      resourcedetection = { detectors = ["env", "eks"] }
    }
    exporters = merge(
      {
        awsemf = {
          namespace                  = "CloudOptix"
          log_group_name             = aws_cloudwatch_log_group.application.name
          dimension_rollup_option    = "NoDimensionRollup"
        }
      },
      var.enable_xray ? { awsxray = {} } : {},
    )
    service = {
      pipelines = merge(
        {
          metrics = {
            receivers  = ["otlp", "prometheus"]
            processors = ["resourcedetection", "batch"]
            exporters  = ["awsemf"]
          }
        },
        var.enable_xray ? {
          traces = {
            receivers  = ["otlp"]
            processors = ["resourcedetection", "batch"]
            exporters  = ["awsxray"]
          }
        } : {},
      )
    }
  })
  tags = local.common_tags
}

# IAM policy statement document for the collector's own IRSA role — attach
# this alongside (not instead of) the per-component policies the security
# module already grants; the collector itself is a separate ServiceAccount
# from any of the six application components.
data "aws_iam_policy_document" "otel_collector" {
  statement {
    sid    = "EMFAndXRayExport"
    effect = "Allow"
    actions = [
      "logs:PutLogEvents", "logs:CreateLogStream", "logs:DescribeLogStreams",
      "cloudwatch:PutMetricData",
      "xray:PutTraceSegments", "xray:PutTelemetryRecords", "xray:GetSamplingRules",
      "xray:GetSamplingTargets",
    ]
    resources = ["*"] # CloudWatch EMF/PutMetricData and X-Ray write APIs are not resource-scopable
  }
  statement {
    sid       = "ReadOwnCollectorConfig"
    effect    = "Allow"
    actions   = ["ssm:GetParameter"]
    resources = [aws_ssm_parameter.otel_collector_config.arn]
  }
}
