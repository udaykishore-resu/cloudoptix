# deployments/k8s

Raw manifests for cluster-level objects the Helm chart deliberately does
not own, because they are either cluster infrastructure that predates any
one release (namespaces, the ExternalSecrets `ClusterSecretStore`) or
objects a Helm chart's own lifecycle is a poor fit for.

`helm/cloudoptix` owns everything release-scoped: Deployments, Services,
ConfigMaps, the per-release `ExternalSecret` objects, HPAs, PDBs,
NetworkPolicies, the Ingress. Deleting the release (or its namespace)
should not delete the objects here.

## Files

- `namespace.yaml` — the three environment namespaces
  (`cloudoptix-dev`/`-staging`/`-production`), each labelled for
  Prometheus Operator discovery and Pod Security Admission. Applied once,
  outside any Helm release — `terraform/environments/production`'s README
  notes this explicitly for production (`helm install --create-namespace`
  is fine for dev/staging, not for production's own audit trail).
- `clustersecretstore.yaml` — one `ClusterSecretStore` per environment
  (external-secrets.io), backed by AWS Secrets Manager via IRSA. This is
  what `secrets.externalSecrets.secretStoreRef` in `helm/cloudoptix`'s
  values points at. It needs its own IRSA role (see the file's comment for
  the exact IAM this role needs) distinct from any of the six
  application-component roles `terraform/modules/security` creates —
  provision it there, in the same environment composition, and reference
  its ARN here.

## Applying

```sh
kubectl apply -f deployments/k8s/namespace.yaml
kubectl apply -f deployments/k8s/clustersecretstore.yaml
```

Before the second command, the External Secrets Operator itself must
already be installed in the cluster (its own Helm chart, not part of this
repository) and `EXTERNAL_SECRETS_IRSA_ROLE_ARN` below must be substituted
for a real role ARN — this file ships as a template with a placeholder,
not as something safe to `kubectl apply` unmodified.
