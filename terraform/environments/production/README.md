# environments/production

Full HA composition: one NAT gateway per AZ, a private-only EKS API
endpoint, Aurora Serverless v2 sized for real headroom (with the
provisioned-instance escape hatch documented in the `rds` module once load
justifies it), Redis with automatic failover and Multi-AZ, deletion
protection everywhere, 30-day RDS backups and a 7-year object-lock audit
archive.

## Setup

Same bootstrap and two-apply cluster sequence as `environments/dev` — see
that environment's README — substituting `production` throughout.

## Before the first production apply

- `endpoint_public_access = false` on `module.eks` means `kubectl` access
  requires being on the VPN/Session-Manager path into the private subnets —
  set that up (or a bastion) before you need it mid-incident, not during
  one.
- Confirm `backend.hcl` points at the bootstrap bucket/table, not a
  dev/staging state key — the three environments share one bucket with
  different `key` paths (set in each `versions.tf`); a wrong `backend.hcl`
  cannot silently write into the wrong *state file* (the key is fixed in
  this repo), but double-check regardless before the first `terraform
  init`.
- Review `terraform plan` in full. This environment has
  `deletion_protection = true` and `skip_final_snapshot = false`
  everywhere that matters, but the plan is still the last chance to catch
  an unintended replace-in-place on the Aurora cluster or the audit bucket.
