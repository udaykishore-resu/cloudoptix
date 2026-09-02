# CloudOptix rule pack

This directory is the versioned, documented configuration for the
deterministic optimization rule engine in
`internal/application/optimization`. Each YAML file is one category of
rules; `rules.go` embeds all of them into the binary and parses them into the
`Pack` the `Registry` is built from.

## Files

| File | Category |
|---|---|
| `compute.yaml` | EC2 rightsizing, waste, scheduling |
| `storage.yaml` | EBS, AMI, S3 |
| `database.yaml` | RDS / Aurora |
| `network.yaml` | NAT, Elastic IP, load balancers, CloudFront, cross-AZ |
| `serverless.yaml` | Lambda |
| `kubernetes.yaml` | EKS, ECS, Fargate |
| `observability.yaml` | CloudWatch logs/metrics, KMS, Secrets Manager |
| `commitment.yaml` | Spot, Reserved/Savings Plan coverage, DynamoDB billing mode |

## Schema

```yaml
version: 1          # schema version of this file; bump on a breaking shape change
pack: compute        # matches the filename, used in error messages
rules:
  - id: ec2-underutilized-rightsize   # stable, never reused — the learning
                                       # loop keys historical accuracy on this
    name: "Human-readable name"
    category: rightsizing             # an optimize.Category value
    action: resize_instance           # an optimize.ActionType value, or
                                       # advisory_only if no executor exists
    description: >
      What the rule detects and why, in prose a reviewer can act on without
      reading the Go source.
    kinds: [aws.ec2.instance]         # cloud.Kind values the rule applies to
    enabled: true                     # shipped default; a tenant override can
                                       # flip this without touching the file
    thresholds:
      cpu_p99_max: 55                 # every threshold is documented by name;
                                       # units are stated in the rule's Go doc
                                       # comment and in the description above
```

A rule ID is permanent. If a rule's *logic* changes materially (not just a
threshold), it gets a new ID rather than silently inheriting the old one's
calibration history — see `execute.RuleCalibration`'s doc comment for why
that matters.

## Tuning a threshold

Edit the value in the relevant YAML file and redeploy. Every threshold here
is a *platform default* — the value used when no tenant-specific override
exists. There is no code change involved in a pure threshold edit.

## Per-tenant overrides

The `Registry` (see `internal/application/optimization/engine.go`) accepts
per-tenant overrides on top of these defaults through `SetTenantOverride`:
a tenant can disable a rule entirely, or override one or more of its
threshold values, without touching this file or affecting any other tenant.
Overrides are looked up first; a threshold with no override falls back to
the value declared here, and a rule with no `enabled` override falls back to
this file's `enabled` flag.

Precedence, highest first:

1. Tenant override (`Registry.SetTenantOverride`)
2. This file's default (`thresholds.<key>`, `enabled`)
3. The rule implementation's own hard-coded fallback (used only if a
   threshold key is missing from both of the above — this should not happen
   in a correctly maintained pack, and its presence is a safety net, not a
   configuration surface)

## Adding a rule

1. Pick a stable, descriptive kebab-case ID and add its entry to the
   appropriate YAML file (or a new file, registered in `rules.go`'s
   `packFiles`).
2. Implement the `Rule` and `ActionBuilder` interfaces in
   `internal/application/optimization`, reading every threshold through
   `EvalContext.Thresholds` (never a Go constant) so the YAML entry is the
   single source of truth for tuning.
3. Register the rule in `registry_init.go`.
4. Add a table-driven test covering at least: the happy path, the
   insufficient-data guard, and the minimum-saving / exclusion guards.

## Removing or disabling a rule

Prefer `enabled: false` over deleting the entry — a disabled rule keeps its
YAML documentation and its calibration history intact, and re-enabling it is
a one-line diff. Delete the entry only when the rule is being retired for
good; deleting the ID also orphans any stored `RuleCalibration` for it.
