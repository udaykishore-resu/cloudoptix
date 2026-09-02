# security

KMS keys, Secrets Manager containers, per-component IRSA roles, and the
public-ALB WAF.

## Secrets are containers, not values

`aws_secretsmanager_secret_version.placeholder` writes the literal string
`REPLACE_ME_OUT_OF_BAND` and then ignores all future changes to
`secret_string`. This is the load-bearing decision in this module: it means
`terraform plan`/`apply` can never contain a real secret value — not in the
CLI output, not in a CI log, not in the state file's diff history. Real
values are set afterward, out of band, by whichever of these fits the
environment:

- an operator running `aws secretsmanager put-secret-value` once,
- a bootstrap script wired into the release pipeline,
- or (for anything mounted into a pod) the ExternalSecrets Operator, which
  `helm/cloudoptix` supports natively — see that chart's README.

Terraform's job is to create the container with the right name, encryption
key and access policy; putting a value in it is deliberately not
Terraform's job, the same way `internal/infrastructure/config.Secret`
refuses to accept a literal value from a committed YAML file. Same
invariant, enforced at a different layer.

## Why two KMS keys

`app` covers RDS, ElastiCache, Secrets Manager and the artefacts/CUR S3
buckets — application state that is all restorable and shares the same
blast radius. `audit` is separate and backs only the object-lock retained
audit bucket (`terraform/modules/storage`), so that bucket's guarantee
("this data survives even a compromise of the application's usual access
path") does not implicitly depend on the same key an incident might already
have touched.

## IRSA roles: one per component, same shape

Every entry in `service_accounts` gets an IAM role trusting the EKS
cluster's OIDC provider, scoped by `sub` to that exact Kubernetes
ServiceAccount. All six roles carry the same three statements today (read
their own secrets, decrypt with the app key, assume a customer's onboarding
role), but they are separate resources rather than one shared role so:

1. CloudTrail in a customer's account and this account's own IAM Access
   Analyzer can distinguish which CloudOptix component performed an action,
   not just "CloudOptix did something".
2. A future asymmetry (e.g. only the automation worker ever needing to write
   to a CloudOptix-owned queue) is a change to one role's policy document,
   not a new conditional inside a shared one.

`AssumeCustomerOnboardingRoles` is scoped to
`arn:aws:iam::*:role/CloudOptix-*` — the exact naming convention
`terraform/modules/cloudoptix-onboarding-role` uses
(`CloudOptix-<tenant>-<Read|Analyze|Plan|Execute>`) — never to `"*"`. A
compromised pod identity can reach only roles a customer explicitly created
for CloudOptix, never an unrelated role in the same account.

## WAF

Three AWS-managed rule groups (common, known-bad-inputs, SQLi) plus a
per-IP rate limit, associated with the ALB the aws-load-balancer-controller
creates for the chart's Ingress. This is deliberately not a hand-maintained
signature set — CloudOptix's public surface is a JSON API behind OIDC/JWT,
and the managed groups track AWS's own threat intelligence without this
module needing to.

The rate-based rule is a second, independent layer from the application's
own per-tenant/per-token limiter (`internal/infrastructure/config`'s
`llm.rate_limit_*` / `aws.rate_limit_*` govern outbound calls; inbound
per-request limiting lives in the HTTP middleware chain) — it exists
specifically to catch unauthenticated volumetric abuse against the public
`/api/v1/onboarding` routes before it reaches a pod at all.
