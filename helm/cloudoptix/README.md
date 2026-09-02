# cloudoptix

Deploys the CloudOptix API, five independently-scaling workers (discovery,
optimization, automation, validation, notification), and a pre-install/
pre-upgrade migration Job, as one Helm release.

## Architecture

`templates/deployment.yaml` ranges over `.Values.components` once rather
than shipping six near-identical Deployment templates — every component
gets the same shape (security context, probes, topology spread, volume
mounts) with only the values that are supposed to differ (image args,
resources, replica count, scaling). The same pattern repeats in
`service.yaml`, `hpa.yaml`, `pdb.yaml`, `networkpolicy.yaml`,
`serviceaccount.yaml` and `servicemonitor.yaml`. If you're reviewing this
chart, `deployment.yaml` and `values.yaml`'s `components` block are the two
files that actually describe the six workloads; everything else is either
shared infrastructure (Ingress, migration Job) or generated per-component
from that same map.

## Install

```sh
helm install cloudoptix . -n cloudoptix --create-namespace \
  -f values.yaml -f values-production.yaml
```

Values files layer as **partial overlays**: `values.yaml` sets every key
with a safe default; `values-dev.yaml` / `values-production.yaml` only set
what actually differs for that environment. Always pass `values.yaml`
first, the environment overlay second — never use an overlay alone.

## Every value, by section

### `global`

| Key | Meaning |
|---|---|
| `environment` | Matches `internal/infrastructure/config.Config.Environment` — gates the dev static-token issuer and permissive CORS. |
| `imageRegistry` / `imageTag` | Image reference; `imageTag` empty falls back to `.Chart.AppVersion`. |
| `imagePullPolicy`, `imagePullSecrets` | Standard pull settings. |

### `config`

Every key here maps 1:1 onto an `internal/infrastructure/config.Config`
field. Two delivery paths exist simultaneously (see `templates/configmap.yaml`'s
header comment for why): a flat `CLOUDOPTIX_*`-keyed ConfigMap for every
field `config.go`'s `envBindings()` table actually reads from the
environment, and a full rendered `config.yaml` (mounted at
`/etc/cloudoptix/config.yaml`, loaded via `--config`) for the fields that
have **no** environment-variable binding at all — `database.ssl_mode`,
`redis.tls_enabled`, most of `worker.*`, and every `features.*` flag except
`autonomous_execution`. If you add a `config.*` key to `values.yaml`,
add it to **both** `templates/configmap.yaml` blocks, or it will silently
never reach the running app.

`config.llm.provider: scripted` (the default, deterministic, no-API-key
provider) is refused by the application's own `Config.Validate()` once
`config.environment` is `production` — `values-production.yaml` sets it to
`anthropic`. This is caught at container startup either way, not silently.

### `secrets`

`secrets.externalSecrets.enabled` picks one of two mutually-exclusive
sources for `<release>-secrets` (see `templates/secret.yaml` vs.
`templates/externalsecret.yaml`, and each file's own header comment):

- `false` (dev default): a plain `Secret` rendered from `secrets.values.*`
  Helm values. **Never use this in production** — `helm get values` and
  `helm history` retain the plaintext value in Helm's own release storage
  indefinitely.
- `true` (production default): an `ExternalSecret` (external-secrets.io)
  syncing from `secrets.externalSecrets.secretStoreRef` — point it at the
  Secrets Manager entries `terraform/modules/security` creates, or (for the
  database password specifically) the RDS-managed secret
  `terraform/modules/rds` outputs as `master_user_secret_arn`, using
  `secrets.externalSecrets.database.property: password` for that case.

### `components.<name>`

One entry per Deployment: `api`, `workerDiscovery`, `workerOptimization`,
`workerAutomation`, `workerValidation`, `workerNotification`.

| Key | Meaning |
|---|---|
| `enabled` | Render this component at all. |
| `kind` | `api` or `worker` — drives the rendered object name (`api` vs. `worker-<workerKind>`) and a few conditional template branches (Ingress backend, NetworkPolicy's open-to-any-source rule). |
| `workerKind` | Worker-only: `discovery\|optimization\|automation\|validation\|notification`, passed as `--kind=<value>` and used in the rendered name. |
| `replicaCount` | Used only when `autoscaling.enabled` is false. |
| `image` | Per-component image override (`repository`/`tag`/`pullPolicy`); empty (the default) falls back to `global`. |
| `args` | CLI args — `["serve"]` for the API, `["worker", "--kind=<x>"]` for a worker. `--config=/etc/cloudoptix/config.yaml` is always appended by the template. |
| `containerPort` | The health/metrics HTTP listener every component runs, API or worker alike — probed by `livenessProbe`/`readinessProbe`/`startupProbe` and scraped by the ServiceMonitor. |
| `service.port` | API only — the ClusterIP Service port the Ingress targets. |
| `resources` | Honest, not copy-pasted: `workerAutomation`'s ceiling is deliberately lower than `workerDiscovery`'s (see the comment in `values.yaml`) because it calls mutating AWS APIs — scaling it aggressively scales blast radius, not just throughput. |
| `autoscaling` | HPA config: `minReplicas`/`maxReplicas`/CPU and memory utilization targets. |
| `pdb` | `enabled`/`minAvailable`. Off by default for `workerValidation`/`workerNotification` at `replicaCount: 1` — a PDB with `minAvailable: 1` on a single-replica Deployment blocks every voluntary eviction (node drains, cluster upgrades) outright. |
| `serviceAccount.roleArn` | IRSA — `eks.amazonaws.com/role-arn`, from `terraform/modules/security`'s `component_role_arns` output. |
| `extraEnv` | Escape hatch: literal extra `env:` entries, appended after the ConfigMap/Secret-sourced ones. |

### `migration`

The pre-install/pre-upgrade hook Job (`templates/migration-job.yaml`)
running `cloudoptix migrate`. `backoffLimit`/`activeDeadlineSeconds` bound
how long a stuck migration blocks the release before Helm gives up;
`serviceAccount.roleArn` is typically the same role as `api`'s, since
migrations need the same database credential path.

### `ingress`

Renders an ALB Ingress (`kubernetes.io/ingress.class: alb`, IP target-type)
when `enabled`. `wafAclArn` sets the
`alb.ingress.kubernetes.io/wafv2-acl-arn` annotation from
`terraform/modules/security`'s `waf_web_acl_arn` output; `certificateArn`
sets the ALB's ACM certificate directly (the common path) as an
alternative to `tls.secretName` (a cert-manager-issued Secret, for a
non-ACM TLS setup).

### `networkPolicy`

Default-deny ingress per component with two allows: Prometheus scraping
`/metrics` from `monitoringNamespaceLabels`, and (API only) any source on
the container port — see `templates/networkpolicy.yaml`'s header comment
for why the API's rule can't be scoped to a pod selector (the AWS Load
Balancer Controller's IP target-type means ALB traffic arrives from VPC
ENIs, not from an in-cluster pod). Egress is left open; the boundary for
outbound AWS calls is IAM, not NetworkPolicy — see that file's comment.

### `serviceMonitor` / `prometheusRule`

Prometheus Operator CRDs. `prometheusRule`'s three threshold values
(`errorRateThreshold`, `latencyP99ThresholdSeconds`,
`discoveryCoverageThreshold`) parameterize the exact alert expressions in
`templates/prometheusrule.yaml` — read that file before changing a
threshold; every rule cites the `internal/infrastructure/telemetry/metrics.go`
metric it fires on and what to check first when it does.

### `podSecurityContext` / `securityContext`

Non-root (uid/gid `65532`, distroless nonroot's fixed value — see
`deployments/docker/Dockerfile`), read-only root filesystem, all
capabilities dropped, `RuntimeDefault` seccomp. `extraVolumes`/
`extraVolumeMounts` (an `emptyDir` at `/tmp` by default) is what makes
read-only-root-filesystem actually work for anything that needs to write a
temp file.

### `topologySpreadConstraints`, `nodeSelector`, `tolerations`, `affinity`

Applied identically to every component. The default spread constraint
(`topology.kubernetes.io/zone`, `maxSkew: 1`, `whenUnsatisfiable:
ScheduleAnyway`) is what keeps a component's replicas from landing in a
single AZ — soft (`ScheduleAnyway`), not hard, so a small cluster or a
single-AZ dev environment never fails to schedule a pod over a spread
constraint it cannot possibly satisfy.

## `values-dev.yaml` vs `values-staging.yaml` vs `values-production.yaml`

All three are **partial** overlays, not full copies of `values.yaml` — see
each file's own header comment. `values-dev.yaml` turns off autoscaling,
PDBs, Ingress and NetworkPolicy (a local kind/minikube cluster's CNI often
doesn't enforce NetworkPolicy at all) and enables the dev static-token
auth issuer. `values-production.yaml` raises every replica floor/ceiling,
switches secrets to ExternalSecrets, and pins the LLM provider away from
the deterministic `scripted` default. `values-staging.yaml` sits between
the two: production's shape (HA-ish replica counts, NetworkPolicy,
ExternalSecrets all on) at smaller scale, so staging exercises the same
failure modes production would rather than dev's simplified one. It was
added after the original three-file design because
`deployments/argocd/applicationset.yaml` needs a uniform
`values.yaml` + `values-<env>.yaml` convention across every environment
it manages — see that file's header comment for the ApplicationSet
templating limitation that drove it.

## Validating this chart

`helm lint` and `helm template` were not run against this chart before
publishing — see the top-level task report for why (network policy in
this environment blocks fetching the Helm binary). Every template was
reviewed by hand for balanced `{{ if/range/with/define }}` /
`{{ end }}` pairs and valid YAML once rendered; run both commands
yourself before deploying:

```sh
helm lint . -f values.yaml -f values-production.yaml
helm template cloudoptix . -f values.yaml -f values-production.yaml | kubectl apply --dry-run=client -f -
```
