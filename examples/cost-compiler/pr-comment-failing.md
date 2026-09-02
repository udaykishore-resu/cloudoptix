<!--
Rendered from regression_report + compilation_result in
compiler-output-failing.json, exactly as CI would post it.
-->

## CloudOptix Cost Compiler — ❌ FAIL

**Monthly impact: +$2,424.15** ($29,089.80/year) · Confidence: 74% · Coverage: 100% (4/4 resources priced, 73% of projected cost usage-modelled)

**Architecture review required.** 2 of 4 cost checks failed.

| Check | Result |
|---|---|
| No forbidden resources (NAT gateways) | ❌ **FAIL** — 3 `aws_nat_gateway` resources found |
| Production monthly increase ≤ 10% | ❌ **FAIL** — 13.10% ($2,424.15 / $18,500.00 baseline) |
| New resources tagged (Application, Team, Environment) | ✅ PASS — all 3 new resources fully tagged |
| Pricing coverage ≥ 90% | ✅ PASS — 100% priced (see confidence note below) |

### ⚠️ No forbidden resources (NAT gateways) — FAILED

New NAT gateways require architecture review before merge. This estate
already carries **$12,348.00/month** of avoidable NAT data-processing waste
on its *existing* gateways (no S3 VPC endpoint present — see
`examples/optimization-scenarios/nat-vpc-endpoint-elimination.md`). Adding
three more gateways compounds a cost category this tenant is actively
working to reduce, without addressing the existing finding first.

Offending resources:
- `module.orders_platform_multi_az.aws_nat_gateway.az_a`
- `module.orders_platform_multi_az.aws_nat_gateway.az_b`
- `module.orders_platform_multi_az.aws_nat_gateway.az_c`

**Suggested remediation:** confirm whether an S3 (and other applicable)
gateway VPC endpoint is planned for this new topology before merging, and
whether 3 independent gateways are actually required versus a consolidated
design.

### ⚠️ Production monthly increase ≤ 10% — FAILED

13.10% increase against the `orders-api` workload's $18,500.00/mo current
baseline ($2,424.15/mo against a $1,850.00/mo allowed threshold).

<details>
<summary>Priced changes (3 created, 1 updated, 0 deleted)</summary>

| Resource | Action | Before | After | Δ/mo |
|---|---|---|---|---|
| `aws_nat_gateway.az_a` ⚠ usage-modelled | create | $0.00 | $632.85 | +$632.85 |
| `aws_nat_gateway.az_b` ⚠ usage-modelled | create | $0.00 | $632.85 | +$632.85 |
| `aws_nat_gateway.az_c` ⚠ usage-modelled | create | $0.00 | $632.85 | +$632.85 |
| `aws_db_instance.orders` (`db.r5.large` → `db.r5.2xlarge`) | update | $175.20 | $700.80 | +$525.60 |

⚠ NAT gateway data-processing cost (73% of the projected total) is a
modelled assumption — 40,000 GB/month combined, based on the `orders-api`
workload's current outbound traffic profile applied to a topology that
does not exist yet. Actual cost will differ if real traffic differs from
that profile.

📎 The `aws_db_instance.orders` instance-class change is not associated
with any open `optimize.Recommendation` — no utilization finding in
CloudOptix prompted this upsize. If this is intentional (e.g. anticipated
load ahead of a known event), state the reason in the PR description so
reviewers aren't left to guess why a $525.60/month increase has no
corresponding evidence trail.

</details>

---
*Evaluated against `platform-cost-guardrails` v3 at 2026-08-29T16:41:03Z. Pricing date: 2026-08-01. This comment reflects `internal/domain/simulate`'s deterministic Cost Compiler — no model call was involved in computing any figure above. A `WARNING`-level check would not block merge; both failures above are `FAIL`-level and do.*
