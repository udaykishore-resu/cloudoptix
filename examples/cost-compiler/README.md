# Cost Compiler examples

Two complete Cost Compiler runs — a passing one and a failing one — each
showing the full chain: a Terraform plan JSON input, the
`simulate.CompilationResult` the compiler produces from it, the
`simulate.RegressionReport` a cost test suite renders against that
compilation, and the PR comment a CI integration would actually post. See
[`docs/cost-engine-spec.md`](../../docs/cost-engine-spec.md) (SPEC-COST-*)
and the root README's [Cost Compiler section](../../README.md#the-cost-compiler)
for the concepts these files instantiate.

Pricing figures throughout are taken directly from
`internal/adapters/pricing/pricebook.json` at its recorded `pricing_date`
(`m5.large` on-demand `$0.048/hr`; NAT gateway `$0.045/hr` + `$0.045/GB`
processed; `db.r5.large`/`db.r5.2xlarge` PostgreSQL single-AZ
`$0.24`/`$0.96` per hour). All monthly figures use a 730-hour month, the
same convention `internal/domain/core.Money`'s pricing helpers use
elsewhere in this codebase.

| File | What it is |
|---|---|
| [`regression-suite.yaml`](regression-suite.yaml) | The `simulate.RegressionSuite` both runs below are evaluated against — one suite, two different plans |
| [`tf-plan-passing.json`](tf-plan-passing.json) | A Terraform plan adding a small autoscaling group and load balancer for a new search-warming service |
| [`compiler-output-passing.json`](compiler-output-passing.json) | The priced `CompilationResult` for that plan |
| [`pr-comment-passing.md`](pr-comment-passing.md) | The PR comment CI would post — all checks pass |
| [`tf-plan-failing.json`](tf-plan-failing.json) | A Terraform plan adding three NAT gateways and upsizing a production database |
| [`compiler-output-failing.json`](compiler-output-failing.json) | The priced `CompilationResult` for that plan, including a `CostRisk` the compiler surfaces independent of the regression suite |
| [`pr-comment-failing.md`](pr-comment-failing.md) | The PR comment CI would post — two checks fail, architecture review required |

## Why the same suite produces different renderings

Both plans are evaluated against `regression-suite.yaml` unchanged — the
suite is exactly the kind of policy-as-code artifact `docs/cost-engine-spec.md`
describes: reviewed and versioned like the infrastructure it gates, not
tuned per pull request. The failing example fails specifically because it
trips a `forbidden_resource` check on `aws_nat_gateway` (this tenant
requires architecture review before any new NAT gateway, having already
discovered $12,348.00/month of avoidable NAT waste in its existing estate —
see [`examples/optimization-scenarios/nat-vpc-endpoint-elimination.md`](../optimization-scenarios/nat-vpc-endpoint-elimination.md))
and a `max_monthly_increase_pct` check, not because of any difference in how
the compiler itself was invoked.
