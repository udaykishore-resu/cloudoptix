# messaging

One EventBridge bus, one SQS queue + dead-letter queue pair per worker kind,
and the SNS topics `internal/adapters/notify` fans out to.

## Why per-worker-kind queues instead of one shared queue

`internal/adapters/events/doc.go` describes the intended shape: EventBridge
is the bus, and "a rule on the bus routes matching events to one or more
queues, and each queue is its own independent, replayable subscription."
Giving the discovery, optimization, automation, validation and notification
workers each their own queue means:

- one worker kind falling behind (or being scaled to zero) never backs up
  or steals capacity from another kind's queue,
- `cloudoptix_worker_queue_depth` (see
  `internal/infrastructure/telemetry/metrics.go`) is meaningful per kind
  rather than an aggregate that hides which worker is actually behind, and
- the HPA for each worker Deployment in `helm/cloudoptix` can scale on its
  own queue's depth independently.

## Dead-lettering is the queue's job, not the app's

`internal/adapters/events/sqs.go`'s own doc comment is explicit that it does
not implement dead-letter logic itself — "SQS already does exactly that,
correctly, via a queue's native RedrivePolicy and maxReceiveCount." This
module is where that infrastructure actually gets created: every main queue
has a `redrive_policy` pointing at its own DLQ, and every DLQ has a
`redrive_allow_policy` restricting redrive-back to that same main queue
only. A message that fails `max_receive_count` (5) times moves to its DLQ
and stays there for `dlq_message_retention_seconds` (14 days — SQS's
maximum) specifically so an SRE has time to notice and investigate before
it silently expires.

There is also a bus-level `eventbridge-dlq`, distinct from the per-queue
DLQs: it catches events EventBridge itself could not deliver to a target at
all (permissions revoked, the queue over capacity), which is a different
failure mode from "the worker received the message and failed to process
it."

## Routing convention

Producers set `detail-type`/`source` to `cloudoptix.<kind>` (e.g.
`cloudoptix.discovery`); each rule matches on `source` for its kind, so
adding a new event under an existing worker kind never requires a Terraform
change — only a new event pattern warrants one.

## SNS topics carry no subscriptions

Like `security`'s Secrets Manager containers, the topics here are created
empty. A Slack webhook URL, a PagerDuty integration key, or an on-call
email address is an environment-specific, sometimes sensitive value that
does not belong in this module — subscribe to `approval-required`,
`execution-completed` and `operational-alerts` out-of-band, per
environment.
