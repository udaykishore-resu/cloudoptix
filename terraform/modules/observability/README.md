# observability

CloudWatch log groups, a platform-SLI dashboard, alarms wired to SNS, and
the ADOT (OpenTelemetry) collector configuration bridging CloudOptix's own
metrics/traces into CloudWatch and X-Ray.

## What "the platform's own SLIs" means here

`internal/infrastructure/telemetry/metrics.go` is the single source of
truth for every metric this module's dashboard and alarms reference —
`cloudoptix_http_*` (request rate, latency, in-flight), `cloudoptix_aws_*`
(outbound AWS API health — the thing that fails first when a customer's
onboarding role is misconfigured), `cloudoptix_discovery_coverage_ratio`
(did the last scan actually see everything it expected to),
`cloudoptix_llm_cost_usd_total` (the copilot's own spend), and
`cloudoptix_worker_queue_depth` (paired with `messaging`'s per-worker-kind
SQS queues). If a metric is added there, it belongs on this dashboard next,
not invented separately here.

## Why alarms are conditional on empty-string variables

`rds_cluster_identifier`, `redis_replication_group_id` and `alb_arn_suffix`
all default to `""`, and every alarm that needs them is gated behind
`count = var.x != "" ? 1 : 0`. This is not a style preference — it breaks a
real ordering problem: the ALB does not exist until the
aws-load-balancer-controller creates it from the chart's `Ingress` object,
which happens after both Terraform apply and the Helm release, not before.
An environment composition that wires `observability` in the same apply as
`eks`/`rds`/`redis` can pass those two ARN-shaped variables immediately;
`alb_arn_suffix` realistically needs a second, later apply (or a
data-source lookup keyed on a stable tag) once the ALB exists. Leaving it
unset simply skips the ALB alarms rather than failing the apply.

## Dead-letter queue alarms have no threshold

`aws_cloudwatch_metric_alarm.dlq_not_empty` fires on `> 0`, not on some
tuned number. A message that reached a DLQ already failed
`max_receive_count` (5, by `messaging`'s default) delivery attempts — it is
not a capacity signal to smooth over, it is evidence of a bug or a
persistently failing AWS call that needs a human to look at the message
body.

## The OTel collector config lives in SSM, not in this repo or the chart

`aws_ssm_parameter.otel_collector_config` publishes the full collector YAML
(OTLP receiver for `internal/infrastructure/telemetry`'s tracer, a
Prometheus receiver for the app's own `/metrics`, CloudWatch EMF + X-Ray
exporters) as an Advanced-tier SSM parameter. The collector Deployment
itself is started by `helm/cloudoptix` (see that chart's
`observability.otelCollector` values) with its config source set to the AWS
SSM config provider naming this parameter — so changing routing,
processors, or the export destination is an SSM value update, not a chart
release.

This module does **not** create the collector's own IAM role — that is one
more IRSA role alongside the six `security` module creates for the app
components, and belongs in the environment composition that already wires
`security`'s OIDC trust inputs. `otel_collector_iam_policy_json` is the
policy document to attach to whatever role the environment composition
creates for it.

## X-Ray is optional

`enable_xray` (default true) adds the `awsxray` exporter and a `traces`
pipeline. Set it false in an environment that routes traces to a different
backend via `TelemetryConfig.OTLPEndpoint` instead — the metrics pipeline
(CloudWatch EMF) is unconditional either way, since the dashboard and
alarms in this module depend on it.
