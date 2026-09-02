# environments/staging

Production's exact topology at production's smallest viable sizes:
multi-AZ NAT (one gateway per AZ, not shared), Aurora with a real reader,
Redis with automatic failover, deletion protection on. The point of
staging is catching a topology, IAM, or upgrade-path bug before production
sees it — a cheaper-shaped staging (e.g. `single_nat_gateway = true`) would
not exercise the same code paths production actually runs, so staging does
not take dev's cost shortcuts.

## Setup

Same as `environments/dev` — see that environment's README for the
bootstrap and two-apply cluster steps, substituting `staging` throughout
(`backend.hcl`'s key is already set to
`cloudoptix/staging/terraform.tfstate`).
