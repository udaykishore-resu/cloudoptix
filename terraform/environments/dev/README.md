# environments/dev

The cheapest composition of every platform module that still exercises all
of them: single NAT gateway, Aurora Serverless v2 at its floor with no
reader, one Redis node, short log/backup retention, deletion protection
off. Not used for anything a real customer's data passes through.

## First-time setup

```sh
# once per AWS account (shared across dev/staging/production):
cd ../../bootstrap && terraform apply -var="bucket_name=<your-unique-bucket-name>"

cd ../environments/dev
cp backend.hcl.example backend.hcl   # fill in from bootstrap's outputs; backend.hcl is git-ignored
terraform init -backend-config=backend.hcl
```

## The two-apply cluster bootstrap

`module.eks` installs the aws-load-balancer-controller, Karpenter (or
Cluster Autoscaler) and Karpenter's NodePool/EC2NodeClass via
`helm_release`/`kubernetes_manifest`, whose providers (`providers.tf`) need
a cluster that does not exist yet on this environment's very first apply.
On a brand-new environment:

```sh
terraform apply -target=module.network -target=module.eks
terraform apply
```

Every subsequent apply is a normal single `terraform apply` — this is only
a first-run concern. See `terraform/modules/eks`'s README for why.

## After apply

Set `helm/cloudoptix`'s `values-dev.yaml` (or a per-environment values
override) from this configuration's outputs: `component_role_arns` for
each ServiceAccount's `eks.amazonaws.com/role-arn` annotation,
`database_endpoint`/`database_master_secret_arn` and
`redis_endpoint`/`redis_auth_secret_arn` for the ExternalSecrets sync
documented in the `rds` and `redis` modules' READMEs, and `waf_web_acl_arn`
for the chart's Ingress annotation.
