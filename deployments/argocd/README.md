# deployments/argocd

Application manifests for each environment plus an ApplicationSet that
generates them from a single template, so adding a fourth environment is a
list entry, not a new file.

## Sync waves

Argo CD applies resources within a sync in ascending `sync-wave` order,
waiting for each wave to be healthy before starting the next:

| Wave | What | Why |
|---|---|---|
| `-1` | `helm/cloudoptix`'s migration Job (via its own `helm.sh/hook: pre-install,pre-upgrade` annotation, which Argo CD's Helm integration honours directly) | Schema must be current before any component starts. |
| `0` | Every Deployment, Service, ServiceAccount, ConfigMap, Secret/ExternalSecret | The application itself. |
| `1` | Ingress, ServiceMonitor, PrometheusRule, NetworkPolicy, PDB, HPA | Depend on the Services/Deployments existing first — an Ingress pointing at a Service that isn't there yet is a transient, self-resolving state, not a hard failure, but ordering it after wave 0 avoids the noise. |

Helm's own hook weights (the migration Job's `helm.sh/hook-weight: "0"`)
and Argo CD's `argocd.argoproj.io/sync-wave` annotation are two different
mechanisms that happen to express the same intent here — see
`helm/cloudoptix/templates/migration-job.yaml`'s comment. Argo CD
respects Helm hooks natively when the source is a Helm chart (as these
Applications are configured), so the migration Job's ordering is already
correct without any additional annotation on this side; the wave numbers
in `applicationset.yaml` are there for the handful of chart resources this
directory does NOT template (see `deployments/k8s/`) that Argo CD applies
outside the Helm hook lifecycle.

## Files

- `application-dev.yaml`, `application-staging.yaml`,
  `application-production.yaml` — one Application per environment, each
  pointing at `helm/cloudoptix` with that environment's values file.
  Useful when environments genuinely diverge in more than values (e.g. a
  different target revision during a staged rollout).
- `applicationset.yaml` — the same three environments generated from one
  list-generator template. Prefer this over the three static files once
  they're all just "same chart, different values file, different
  namespace" — which is the common case.

Both are provided because different teams settle on different conventions
here; pick one path (static Applications, or the ApplicationSet) per
environment, not both — applying both would create two Argo CD
Applications managing the same release and fighting over it.
