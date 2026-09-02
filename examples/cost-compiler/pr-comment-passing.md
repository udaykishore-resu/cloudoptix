<!--
This is what CloudOptix's CI integration actually posts as a PR comment
body (rendered from regression_report + compilation_result in
compiler-output-passing.json). Shown here as the file CI would produce,
not as documentation prose.
-->

## CloudOptix Cost Compiler — ✅ PASS

**Monthly impact: +$139.07** ($1,668.84/year) · Confidence: 96% · Coverage: 100% (4/4 resources priced)

All 4 cost checks passed.

| Check | Result |
|---|---|
| No forbidden resources (NAT gateways) | ✅ PASS — 0 `aws_nat_gateway` resources |
| Production monthly increase ≤ 10% | ✅ PASS — 0.98% ($139.07 / $14,200.00 baseline) |
| New resources tagged (Application, Team, Environment) | ✅ PASS — all 4 resources fully tagged |
| Pricing coverage ≥ 90% | ✅ PASS — 100% priced |

<details>
<summary>Priced changes (4 created, 0 updated, 0 deleted)</summary>

| Resource | Action | Before | After | Δ/mo |
|---|---|---|---|---|
| `module.search_warmer.aws_autoscaling_group.warmer` | create | $0.00 | $105.12 | +$105.12 |
| `module.search_warmer.aws_lb.warmer` ⚠ usage-modelled | create | $0.00 | $33.95 | +$33.95 |
| `module.search_warmer.aws_lb_target_group.warmer` | create | $0.00 | $0.00 | $0.00 |
| `module.search_warmer.aws_launch_template.warmer` | create | $0.00 | $0.00 | $0.00 |

⚠ `aws_lb.warmer`'s LCU consumption is usage-dependent and modelled from 6
comparable internal ALBs in the search namespace (median 3 LCU-hours/hour,
trailing 30 days) — not measured directly, since this load balancer does
not exist yet. Actual cost may differ once real traffic is observed.

</details>

<details>
<summary>💡 Opportunity noticed while pricing (not required, informational)</summary>

`module.search_warmer.aws_autoscaling_group.warmer`: this is stateless
request-handling capacity behind an internal ALB, and `optimization.spotAllowed`
is `true` for this tenant. A mixed-instances policy (1 on-demand base + 2
Spot) would save an estimated **$68.06/month** on this specific group. Not
blocking — surfaced because it was nearly free to notice while pricing this
change.

</details>

---
*Evaluated against `platform-cost-guardrails` v3 at 2026-08-29T14:02:11Z. Pricing date: 2026-08-01. This comment reflects `internal/domain/simulate`'s deterministic Cost Compiler — no model call was involved in computing any figure above.*
