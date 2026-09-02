# bootstrap

Run once per AWS account/region, before initialising any environment in
`terraform/environments/`. Creates the S3 bucket and DynamoDB table every
environment's remote state backend points at, using local state itself
(there is nothing left to bootstrap this configuration onto).

```
cd terraform/bootstrap
terraform init
terraform apply -var="bucket_name=cloudoptix-tfstate-<your-account-id-or-org>"
```

Then fill in each environment's `backend.hcl` (see
`terraform/environments/*/backend.hcl.example`) with the bucket and table
names this prints.

## Why this isn't just "one shared backend config"

The bucket must exist *before* any environment's `terraform init -backend-config=...`
can succeed — there is no way to have an S3 backend create its own bucket.
This module is deliberately small, uses local state, and is expected to be
applied by a human once, not by CI on every change — `prevent_destroy` is
set on both resources specifically so an accidental `terraform destroy` in
the wrong directory cannot take out every environment's state at once.
