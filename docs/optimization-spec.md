# Optimization specification

Covers `SPEC-OPT-001..010`, `SPEC-SIM-001`, and `SPEC-UTL-001..002` — the rule engine, its scoring functions, the simulation/compiler engines built on the same "never guess, always state the assumption" discipline, and the statistics layer every rule reads from.

## SPEC-OPT-001 — The Rule / Finding / Recommendation separation

The key design decision, stated in `internal/application/optimization/doc.go`: a naive cost tool conflates three things into one opaque score. CloudOptix keeps them structurally separate.

1. **A Rule fires deterministically on evidence and produces a Finding** — a statement of fact ("this instance's P99 CPU is 11% over 14 days with 92% coverage"), never a recommendation. Two runs against the same inputs always produce the same findings in the same order, independent of model sampling, wall-clock time, or map iteration order (`REQ-OPT-011`).
2. **Confidence, blast radius and risk are computed by dedicated, independently testable functions** (`confidence.go`, `blast.go`, `risk.go`) from structured facts — metric stability, dependency-graph completeness, business criticality, historical calibration — never from an LLM's self-assessment.
3. **The Registry** (`engine.go`) owns which rules run, with what thresholds, for which tenant, loaded from the versioned YAML rule pack in `rules/`. A threshold change is a config diff, not a code deploy.

Every rule guards against three failure modes that make naive cost-optimization tools untrustworthy: acting on insufficient telemetry (an idle-looking resource with 6% metric coverage is a data problem, not a finding), rightsizing on the mean instead of the tail (the exact failure mode that causes an outage the night a batch job runs), and recommending an action the tenant's own risk tolerance, exclusions or SLOs rule out.

`optimize.Recommendation` (`internal/domain/optimize/recommendation.go`) is what a Finding becomes once enriched with a predicted effect, a risk assessment, a blast radius, and an executable `ActionType`. An LLM may explain a recommendation, rank alternatives, or draft its narrative afterward — it never creates one, because the recommendation is the object the execution engine acts on.

## SPEC-OPT-010 — Parameters are the executor's vocabulary

`Recommendation.Parameters` is the only thing an executor reads; it never re-derives intent from prose. The key names belong to the **executor**, not the rule, and the direction is asserted rather than merely documented. `optimize.ParameterContractFor(action)` declares, per action, what an executor for it needs; `executor_contract_test.go` walks every registered rule, builds a representative recommendation, and checks three things at once — that the rule's action is advisory, or has a registered executor whose contract the parameters satisfy, or is on an explicitly enumerated list of actions no executor implements yet; that no contract is declared for an action nothing implements; and that the YAML pack's declared `action` matches what the code emits. A rule that invents its own spelling used to produce a recommendation that passed validation, policy, approval and preflight and then failed at the mutate step — or, worse, succeeded while doing nothing.

## SPEC-OPT-009 — Overlapping recommendations are grouped, never summed

Three rules can each be right about one EKS node group — shrink the node count, shrink the node size, shrink the pod requests that force the node count — and at most one of them can be applied. Adding their savings together names money the estate does not hold, which is the credibility failure this product exists to fix.

`internal/domain/optimize/conflict.go` models it explicitly. Every recommendation carries a `ConflictDomain`: the dimension of one resource's spend the change competes for, declared by the rule and defaulted from the action type. Two recommendations conflict when they target the same resource in the same domain. The domain is deliberately finer than the resource (downsizing an RDS instance's class and shrinking its allocated storage are separate bills that genuinely compose) and coarser than the action ("shrink the node group" and "shrink the pods that size it" are different verbs claiming identical dollars).

`GroupConflicts`, run after `Rank` so the priority formula has scored the set, marks the highest-ranked member of each group the **primary** and the rest **alternatives**. Alternatives are kept — a platform team may legitimately prefer the pod-request fix to the node-count fix — and contribute zero to every aggregate: `TotalPotentialSaving`, `RecommendationSummary`, the savings funnel's potential stage, the executive summary, the efficiency score's identified waste, the twin's per-node saving, and a policy simulation's auto-executable saving all read `Recommendation.CountsTowardTotal()`. Counts still include them, and `MutuallyExclusiveAlternatives` reconciles the two. `RecommendationExplanation.Alternatives` exposes the whole group, so a detail view can say "three ways to fix this, here is the one we recommend and why".

## SPEC-OPT-002 — The rule pack: 48 rules, versioned as data

`rules/*.yaml`, loaded via `rules.Load` (embedded with `go:embed`, so the shipped binary never depends on a `rules/` directory existing on disk at runtime):

| File | Rule count | Category |
|---|---:|---|
| `compute.yaml` | 7 | EC2 rightsizing, prior-generation, never-used, burst-credit, commitment-gap, schedule-offhours, spot-candidacy, stopped-storage |
| `storage.yaml` | 11 | EBS gp2→gp3, orphaned snapshot, overprovisioned, unattached, unused AMI; S3 incomplete-multipart, intelligent-tiering, no-lifecycle, noncurrent-versions, wrong-storage-class |
| `database.yaml` | 8 | RDS Aurora candidacy, backup retention, gp2→gp3, idle, multi-AZ-in-nonprod, overprovisioned storage, oversized, unnecessary replica |
| `network.yaml` | 6 | CloudFront egress, cross-AZ chatter, EIP unattached, load-balancer idle, NAT redundant, NAT-without-VPC-endpoint |
| `serverless.yaml` | 4 | Lambda excessive timeout, Graviton candidacy, memory-cost-curve, provisioned-concurrency |
| `kubernetes.yaml` | 6 | ECS task count, EKS consolidation, EKS nodegroup-no-spot, EKS nodegroup-overprovisioned, Fargate-vs-EC2, pod-request-oversized |
| `observability.yaml` | 3 | CloudWatch high-cardinality metrics, log retention, KMS/Secrets unused |
| `commitment.yaml` | 3 | Reserved Instance / Savings Plan coverage and gap detection |
| **Total** | **48** | |

Splitting the pack into one file per category — rather than one monolith — keeps each diff scoped to the team that owns that part of the estate; an SRE tuning "our staging fleet actually runs hot, raise the underutilization CPU ceiling to 55%" reviews that change as a one-line YAML diff, not a Go pull request.

Two rules carry the pricing/economics logic worth calling out specifically: `rule_ec2_rightsize.go` uses **percentile-based** (not mean-based) rightsizing, and `rule_s3_intelligent_tiering.go` computes a break-even against the storage-class transition's own cost before recommending it — a rule that ignores the transition cost would recommend Intelligent-Tiering on data too small or too cold for the tiering fee itself to pay off.

## SPEC-OPT-004 — Confidence, computed from structured facts

`ComputeConfidence` (`internal/application/optimization/confidence.go`) is a weighted function of six additive factors (weights sum to 1) plus one multiplicative factor applied last:

| Factor | Weight | What it measures |
|---|---:|---|
| Stability | 0.20 | How stable the underlying metric was over the observation window |
| Coverage | 0.18 | What fraction of the expected window was actually observed |
| Window | 0.16 | How long the observation ran, capped at "fully observed" beyond 14 days |
| Dependency | 0.16 | How much of the resource's dependency graph is visible |
| Criticality | 0.15 | How much is at stake if the recommendation is wrong |
| Age | 0.15 | How established the resource is, capped at "established" beyond 30 days |
| Calibration (multiplicative, last) | — | This exact rule's historical accuracy for this tenant (from `internal/application/learning`) |

This is deliberately not an LLM self-report: "an LLM asked 'how confident are you' answers from its own training distribution of confident-sounding text, which is uncorrelated with whether the underlying telemetry actually supports the claim — a model will happily report 90% confidence on a resource with three data points and 95% confidence on a resource with three thousand" (package doc comment). Every factor here is auditable — a reviewer could recompute it by hand from the same finding.

## SPEC-OPT-005 — Blast radius, walked, never estimated

`ComputeBlastRadius` (`blast.go`) walks `cloud.Topology.Dependents(r.ID, 6)` — never estimates — into `ResourcesAffected`, `ServicesAffected`, `CriticalServices`, and `APIsAffected` counts, each saturating against a modest reference count (10 resources, 4 services, 2 critical services, 3 APIs — chosen because a change rippling to ten downstream resources or two critical services is already a large blast radius for one optimization action). Crucially, blast radius also carries a `Completeness` figure derived from how much of the dependency graph is actually visible, so a blast radius computed on a thin graph never reads as a falsely small one.

## SPEC-OPT-006 — Risk assessment

`ComputeRiskAssessment` (`risk.go`) is deterministic from structured facts about the action, the resource, and its context — never a model's judgement, for the same reason as confidence: same inputs always yield the same score, level, and factor list, which is what lets a policy rule key off `MaxRiskLevel` and mean the same thing every time it is evaluated. `capacityReducingActions` (resize, stop) are weighed against a workload's declared SLO headroom (`spec.WorkloadSLO`) when one exists; `sloDeclared` explicitly treats an unset SLO as "we don't know," never as "no headroom risk," so a workload that never filled in its SLO block does not get a falsely reassuring risk score by omission. `storageTouchingActions` (resize/modify-type, short of outright deletion) carry a smaller-than-`Destructive()` but nonzero data-loss risk factor.

## SPEC-OPT-007 / SPEC-OPT-003 — Insufficient telemetry and tail-based rightsizing

Two rules a "naive" optimizer would get wrong, and CloudOptix explicitly guards against: a rule does not fire an idle/rightsizing finding on a resource whose metric coverage falls below its declared minimum (an idle-looking resource with 6% coverage is a data problem, not a finding), and every rightsizing rule evaluates a percentile (P95/P99), never a mean — a mean-based rightsize is the exact failure mode that produces an outage the night a batch job runs, since the mean is exactly the number a burst workload's peak is invisible inside.

## SPEC-OPT-008 — Learning feeds confidence only

See [`automation-spec.md`](automation-spec.md) for the full write-up; in summary here because `SPEC-OPT-008` is cited from both `internal/domain/execute/savings.go` and `internal/application/learning/doc.go`: `learning.Recalibrate` produces `execute.RuleCalibration` multipliers from `execute.Outcome` (predicted vs. actual) records, gated by a minimum-sample guard so two rollbacks out of two attempts does not move a rule's confidence — that would be amplifying noise, not learning. The loop never reads or writes a `govern.Policy`, an `optimize.Rule`, a `spec.Spec`, or a `ValidationCheck`; calibration changes how confident a future recommendation claims to be, never what it is allowed to do.

## SPEC-SIM-001 — Simulation, mutation, counterfactual, and the Cost Compiler

`internal/domain/simulate` and `internal/application/simulation` implement three forward-looking engines that all share one discipline, stated on `simulate.PricedChange` and repeated across the family: **a simulated number is never presented as a measurement.** Every result carries its assumptions, the confidence in each, and the pricing basis used.

- **The Architecture Mutation Engine** generates candidate architectures against the tenant's stored `Inventory`/`Topology` and scores each on eight independent dimensions (`simulate.Dimension`): cost, performance, reliability, scalability, security, operational complexity, migration effort, risk — deliberately not cost alone, because "an architecture that is 40% cheaper and one SPOF worse is not an improvement, and a tool that only ranks by cost will keep recommending it."
- **The Counterfactual Engine** answers "what if" against the same inventory: 5x Black-Friday traffic, removing the NAT gateways, a different commitment posture.
- **The Cost Compiler** (`internal/application/compiler`) prices infrastructure changes from Terraform plan JSON, raw HCL, CloudFormation, Kubernetes manifests, and Helm output — normalized into `RawResource`, keyed by the Terraform provider type name (the most complete, widely recognized AWS vocabulary already in use, so CloudFormation types and Kubernetes kinds translate onto it rather than inventing a fourth vocabulary). `PricedChange.Unpriced` and a genuinely-zero `AfterMonthly` are different, never conflated answers; a usage-dependent resource (Lambda, NAT bytes, S3 growth) is priced with stated, overridable `Assumption`s. `CompilationResult.Coverage`/`Confidence` discount for how much of the projected delta is usage-dependent modelling rather than fixed catalog pricing. `RunRegression` evaluates a suite of `RegressionCheck`s (max increase %, max increase absolute, max cost-per-transaction, forbidden resource, required tags, max unpriced ratio, budget headroom) into a `PASS`/`WARNING`/`FAIL` `Verdict`. See [`examples/cost-compiler/`](../examples/cost-compiler/) for a full CI run, both passing and failing.

Why the compiler's engine logic lives in a separate, dependency-free package rather than inline in `internal/application/simulation`: the compiler is a pure function of an IaC input and a pricing catalog (no persistence, no scope resolution), while the mutation and counterfactual engines are inherently about the tenant's stored estate. Splitting on that seam gives the compiler's parsers and pricing logic the same fast, storage-free test suite whether called through the service or directly (as CI tooling would), and keeps the mutation/counterfactual tests focused on scope resolution and scenario modelling against fake repositories.

## SPEC-UTL-001 — Percentile statistics (domain layer)

`core.SummarizeSamples` (package `core`, not `utilization`) computes percentiles, mean, stddev, and stability from a bare slice of values. This computation has no notion of time and belongs in the domain layer where every package can reach it without an import cycle. *(No source `Traceability:` marker cites this section directly by ID — see [`docs/traceability.md`](traceability.md), Flagged IDs.)*

## SPEC-UTL-002 — Trend and seasonality (application layer)

Trend and seasonality are inherently about *when* a value was observed, not just what it was, so `internal/application/utilization` owns the timestamped half of the computation and hands back the same `core.Percentiles` struct with `Trend`, `Seasonal`, and `PeakHours` filled in — a rule reading a summary never has to know which layer computed which field. Seasonality is detected with **autocorrelation at 24-hour and 168-hour (weekly) lags** on an hourly-resampled series, rather than an FFT or a fitted model — the simplest technique that directly answers the question a rightsizing rule actually asks ("does yesterday's shape predict today's"), and one whose result (a correlation coefficient) a human can sanity-check by eye against the graph, which matters for a signal that gates a scheduling recommendation.

## Current limitations

- 5 of the 48 rules have an individual `_test.go` companion (`rule_ec2_rightsize_test.go`, `rule_ebs_gp2_gp3_test.go`, `rule_nat_vpc_endpoint_test.go`, `rule_k8s_pod_requests_oversized_test.go`, `rule_lambda_memory_cost_curve_test.go`). Every rule is now additionally exercised by `executor_contract_test.go`, which builds a representative firing fixture for each rule whose action has an executor and checks the emitted parameters against that executor's contract; the rules without a dedicated companion still have no assertion on their *saving arithmetic* beyond the registry-level tests (`registry_init_test.go`, `priority_test.go`) and the demo-estate waste-envelope assertions in `awssim/demo_test.go`.
- `rds-gp2-to-gp3` cannot fire against the shipped price book, which carries the same per-GiB rate for RDS gp2 and gp3 (matching AWS's own list price). The real gp3 saving comes from no longer paying for provisioned IOPS, a dimension the rule does not price, so it computes a zero saving on every input. It is listed as such in `executor_contract_test.go` rather than quietly having no fixture.
- `ebs-unattached-volume` and `elastic-ip-unattached` measure how long a resource has been detached from `FirstSeenAt` — discovery presence, since AWS exposes no attachment history — so neither can fire until a tenant has been observed for longer than its age guard. A single-scan demo seed therefore never surfaces them, even against an estate that deliberately contains both.
- The Mutation Engine and Counterfactual Engine are implemented and tested against fake repositories (`internal/application/simulation/{simulation,fakes}_test.go`); neither has been exercised against a real, large discovered estate.
- The Cost Compiler's Terraform-plan and HCL parsers are tested against hand-written fixtures; they have not been run against a real, large Terraform codebase's actual plan output.
