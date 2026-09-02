# rds

Aurora PostgreSQL, switchable between Serverless v2 and fixed-size
provisioned instances, backing the schema in `migrations/`.

## Serverless v2 vs provisioned

`var.serverless` picks the mode. Serverless v2 (the default) scales ACUs
within `[serverless_min_acu, serverless_max_acu]` continuously and is the
right choice whenever load is unpredictable or not yet characterised — dev,
staging, and a production environment still early in its life. Provisioned
(`serverless = false`, fixed `provisioned_instance_class`) is cheaper once
load is high and steady enough that serverless's autoscaling headroom stops
buying anything over a correctly-sized fixed instance — which is the same
rightsizing judgment call this platform's own product
(`internal/application/optimization`, the `resize_rds` action in
`internal/adapters/aws/executor/rds.go`) exists to help a customer make
about their own databases. Eating that dog food means this module supports
both rather than picking one and calling it done.

## The master password never touches Terraform

`manage_master_user_password = true` delegates credential generation,
storage and rotation entirely to RDS and Secrets Manager. Terraform's state
and every plan it ever produces contain zero bytes of the actual password —
only the ARN of the secret RDS itself manages, exposed as
`master_user_secret_arn`.

To get that password into the running application: point an
`ExternalSecret` (see `helm/cloudoptix`'s ExternalSecrets support) at
`master_user_secret_arn`, syncing its `password` field into the
`DATABASE_PASSWORD` key of the chart's database Secret — which the
Deployment then injects as `CLOUDOPTIX_DATABASE_PASSWORD`. That is a literal
value arriving via a process environment variable, which
`internal/infrastructure/config/secret.go`'s `Secret` type explicitly
permits (only a *committed file* may not hold a literal secret) — the two
layers agree on the same provenance invariant by construction, not by
convention.

## instance_count and the "idle replica" pathology

Aurora readers are cheap to add for failover/read-scaling relative to a
single-instance RDS replica, so `instance_count = 2` (one writer, one
reader) is a reasonable production floor here. This module does **not**
model the "oversized RDS with an idle replica" waste pathology —
`terraform/demo` does, deliberately and separately, as one of the things
CloudOptix's own `resize_rds` / replica-utilisation recommendations should
find.

## Parameter group

`log_min_duration_statement = 1000` (log queries over 1s) plus connection
logging. This is the slow-query trail an SRE actually reaches for when a
latency regression traces back to a specific query — CloudOptix's own
`cloudoptix_http_request_duration_seconds` metric tells you *that* p99 grew,
this tells you *why*.
