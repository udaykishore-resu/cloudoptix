# Automation specification

Covers `SPEC-AUTO-001..006` and `SPEC-GOV-001` — `internal/domain/govern`, `internal/application/governance`, `internal/domain/execute`, `internal/application/automation`, `internal/adapters/aws/executor` / `internal/adapters/awssim`'s executors.

## SPEC-GOV-001 — Policy-as-code, a pure function

`govern.Evaluate(policy, input)` (`internal/domain/govern/policy.go:319`) is intentionally a pure function — no I/O, no clock reads beyond `input.Now`, no repository calls — because a decision must be reproducible months later during an audit. The application layer, `internal/application/governance`, is where the impurity lives: it is the one place responsible for assembling a complete, honest `govern.Input` from the recommendation, the resource, the tenant's approved specification, and the current economic error budgets, and for persisting the `Decision` that comes back. `buildInput`'s assembly is the security-critical half of governance, not the rule evaluation itself — a rule can only be as strict as the facts it is given, so a missing or zero-valued field (an empty `AccountID`, a `Region` that silently defaulted) would silently weaken every guard downstream without the policy document itself ever looking wrong. `buildInput` therefore validates that every field `govern.Match` reads is actually populated and fails closed — returns an error, produces no `Decision` — rather than evaluate against a partially-blank input.

### Evaluation order

1. Every rule whose `Match` is satisfied by the input is collected.
2. Among the rules that matched, the **most restrictive effect wins**: `prohibit` (rank 3) beats `require_approval` (rank 1) beats `advisory_only` (rank 2 — note: `advisory_only`'s rank sits between `require_approval` and `prohibit` in `Effect.Rank()`) beats `auto_execute` (rank 0), regardless of the order rules appear in the file.
3. If no rule matched, `default_effect` applies. `Policy.Validate()` refuses `default_effect: auto_execute` outright — an unmatched action is by definition one nobody has written a rule for yet, and CloudOptix does not act alone on the untested case.
4. A short, fixed set of **platform invariants** apply last, after every tenant rule, and cannot be overridden by any policy document:
   - A destructive action (`optimize.ActionType.Destructive()`) is never auto-executed.
   - Nothing auto-executes while `spec.Automation.Enabled == false`.
   - An exhausted economic error budget with `freeze_cost_increases` hard-`prohibit`s any cost-increasing change; a budget under pressure but not exhausted downgrades `auto_execute` to `require_approval` **for a cost-increasing change**. Both guards read `MonthlyCostDelta`, because both derive from `EconomicErrorBudget.AllowsCostIncrease()`, whose whole subject is spending more. A change that *reduces* run-rate is never escalated by a budget signal — a tenant over budget is the one whose savings should be least obstructed, and escalating those made the platform stop saving money at the moment the budget said it needed to.

### What no policy can weaken (`Policy.Validate()`, blocking checks)

- `default_effect: auto_execute` — refused.
- `auto_execute` given to a destructive action — refused.
- An `auto_execute` rule with no `match.actions`/`match.categories`/`match.rule_ids` at all — an auto-execute rule must name what it permits, not match everything — refused.
- `auto_execute` reaching production (or an empty `match.environments`, which reaches every environment including production) without `min_confidence >= 0.85` — refused.

These four checks are `SeverityCritical`/`SeverityHigh` and blocking — a policy tripping one cannot be saved, not merely warned about.

### Spec-level exclusions and change freezes apply as a tightening pass

Two constraints live in `internal/application/governance`, not in a `govern.Policy` rule, because they are authored by a different flow — the tenant's approved specification (excluded actions/resources/tags, change-freeze windows) — with its own review and approval process, not a policy a tenant edits directly. `govern.Match` deliberately has no vocabulary for "was this specific resource ID excluded" or "is today inside a declared freeze window," and should not grow one: a specification-level exclusion must not be expressible or overridable from inside a policy rule. The application layer applies both as a post-processing pass over the `Decision` the domain package already returned, using the identical "most restrictive wins" rule the domain layer uses for its own invariants: an exclusion or freeze can only make an outcome stricter, never looser.

## The four reference policy packs (`policies/`)

| Pack | `auto_execute` at all? | Production posture | Segregation of duties |
|---|---|---|---|
| `conservative.yaml` | Never | 2 approvals, distinct approvers | Yes, production only |
| `balanced.yaml` (platform default) | Unambiguous waste only, non-production only | 1 approval | No |
| `aggressive.yaml` | High-confidence, fast-reversible changes, even in production, inside a declared maintenance window | 1 approval outside those narrow conditions | No |
| `regulated.yaml` | Never | 2 approvals, distinct approvers, **everywhere** (not only production) | Yes, everywhere, plus a tag-triggered change-freeze prohibition |

`balanced.yaml`'s single `auto_execute` rule, `balanced.waste.non-production.auto`, requires `categories: [waste]`, a fixed small action set (stop, release EIP, abort multipart uploads, set log retention, remove idle provisioned concurrency, schedule shutdown), `environments: [development, staging, sandbox, test]`, `min_confidence: 0.85`, `max_risk_level: LOW`, `max_critical_services: 0`, `min_reversibility: fast`. Rightsizing (`balanced.rightsizing.always-approval`) always requires approval, everywhere, because a wrong estimate degrades performance instead of merely costing a few minutes to reverse.

**A change freeze is expressed as a tag, not a calendar**, because `govern.Match` has no "not during this window" selector — only "allowed window" selectors (`require_maintenance_window`), which express the opposite. `regulated.yaml` models a freeze as `tag_selector: {change_freeze: active}` → `effect: prohibit`; an operations team applies and removes the tag for the freeze's duration. `policies/README.md` documents this as the honest match for what the current schema can express, not a workaround.

**A YAML schema gap confirmed empirically:** `govern.Match.MaxMonthlySavingImpact` is typed `core.Money` (unexported fields, custom JSON marshal/unmarshal, no `MarshalYAML`/`UnmarshalYAML`); `gopkg.in/yaml.v3` errors outright on any rule setting `max_monthly_saving_impact` from a plain scalar. None of the four packs use that field, for exactly this reason. Fixing it needs a `core.Money` YAML (un)marshaler or a different field type — an `internal/domain` change outside the scope of the policies directory.

**A verified correction to `policies/README.md`.** That file's own "Known limitation" section claims `auto_execute` is mathematically unreachable due to a rule-fold bug. Reading the current `govern.Evaluate` and testing it directly against `balanced.yaml` shows this is **no longer true** — see [The AI safety model](../README.md#the-ai-safety-model) in the root README for the full verification. `policies/README.md` was not updated after the fix landed; treat the code, not that comment, as ground truth.

## SPEC-AUTO-001 — The four-phase execution discipline

Every change goes through the same four phases as separate, independently auditable calls, implemented by `internal/application/automation`:

```
Plan          build forward steps + snapshot steps + rollback plan — no AWS call, nothing mutates
Execute       re-check governance one more time, then run the plan step by step, idempotent, retried
Validate      watch the change for its declared observation window, render a verdict against baseline
Rollback      reverse it
```

Nothing skips a phase: `PlanExecution`'s output is not executable on its own, `Execute` refuses to run a plan whose rollback was not built, and `Validate` never runs against a plan that has not finished executing. The obvious alternative — one long "just do it" method — would make every one of those refusals implicit and untestable; keeping the phases as separate calls with separate persisted state is what lets an operator or a test stop the story at any point and see exactly what has and has not happened yet.

## SPEC-AUTO-002 — Rollback built before anything runs

`execute.Plan.Executable` requires a non-nil `Rollback` and refuses a plan that mutates without a preceding snapshot step — enforced a second time at the orchestration layer, on top of what the domain type and each `ports.Executor` implementation already enforce, because "CloudOptix never makes a change it cannot undo, or at minimum cannot honestly describe as undoable" is exactly the kind of invariant that must not depend on every future executor implementation remembering to preserve it. A plan whose rollback is infeasible (a deletion, a released IP) is not blocked from proceeding — some savings are only available by doing something irreversible — but it is never silently treated as feasible: `DataLossRisk` and `InfeasibleReason` travel with the plan into the approval screen.

## SPEC-AUTO-003 — Idempotency is the retry story

Every mutating step carries an `IdempotencyKey` the executor's `Apply` method is contractually required to honor (`ports.Executor`'s doc comment; `awssim`'s generic executor implements it by checking whether the target already matches the desired state before mutating). A step that fails on a transient AWS error (throttling, a timeout) is retried with exponential backoff up to a bounded attempt count; a retry that actually landed on the first attempt but failed to *report* success (a network cut after the API call succeeded) is indistinguishable from one that never landed — both are safe to retry because `Apply` is idempotent on the key, not because CloudOptix trusts the network to behave.

## SPEC-AUTO-004 — The autonomous loop's own blast radius

`ProcessAutonomous` is the one entry point that acts without a human in the loop for that call, so it caps itself independently of any per-plan safety check: a per-cycle count of how many changes it will plan and execute, the tenant's declared maintenance windows, a running total against `spec.Automation.MaxMonthlyImpact`, and per-tenant concurrency all bound what one invocation can do before the next governance re-check happens on the next recommendation. A bug that caused every recommendation in a large estate to look auto-executable should degrade to "capped and reported," never to "every resource changed in one pass."

**Governance is re-checked a second time, immediately before the first AWS call**, inside `Execute` — not only trusting whatever state the plan carried from `PlanExecution` time. A plan can sit approved for hours: a maintenance window can close, an economic error budget can freeze, a change freeze can start, an approval can expire. If the fresh decision no longer permits the change, `Execute` refuses — even for a plan that was perfectly valid when approved.

## SPEC-AUTO-005 — The six-stage savings ladder

See the root README's [Autonomous Optimization differentiator](../README.md#autonomous-optimization-and-the-six-stage-savings-ladder). `execute.SavingsStage` is advanced by `internal/application/automation` as each phase completes: `Planned` when `PlanExecution` succeeds, `Executed` when the AWS mutation lands, `Validated` when the observation window closes without a critical regression, `Realized` when *that same measurement* — the next invoice — is what produced the number, never the original prediction restated. A rollback (mid-plan execution failure, or a post-validation critical regression) marks the record `Lost` with the reason, rather than continuing to advance it or silently disappearing it — hiding a lost saving would make the funnel's conversion rate a marketing number instead of an operational one.

## SPEC-AUTO-006 — Post-change validation

`internal/domain/execute/plan.go`'s `ValidationCheck`/`ValidationResult` machinery: each check names a `Metric`, a `Statistic` (p95/p99/avg/max/sum), and a `Comparison` against baseline (`no_worse_than_pct`, `below_absolute`, `above_absolute`, `improved_by_pct`), with `Critical` distinguishing a check whose failure triggers immediate rollback (`AutoRollbackOn`) from one that merely alerts. `MinSamples` guards against declaring success on a quiet weekend. `Verdict` is one of `success, partial_success, failure, inconclusive` — `inconclusive` is a real, honest outcome for insufficient or ambiguous signal, never forced toward success or failure.

## Worked scenario

See the root README's [example optimization workflow](../README.md#example-optimization-workflow-end-to-end) and the fuller [`examples/optimization-scenarios/`](../examples/optimization-scenarios/), including one that rolls back.

## Current limitations

- `govern.Evaluate` is implemented and its rule-fold logic verified directly (see the corrected `policies/README.md` claim above), but `internal/domain/govern` has no dedicated `_test.go` file of its own — its correctness is exercised only through `internal/application/governance`'s tests (`evaluate_test.go`, `approval_test.go`, `maintenance_test.go`).
- The four-phase discipline and idempotent retry logic are implemented and tested against `awssim`'s executors (`internal/application/automation/{execute,plan,rollback}_test.go`); no execution phase has ever run against a real AWS account's actual throttling/eventual-consistency behaviour.
- `ProcessAutonomous`'s per-cycle caps are implemented and unit-tested (`autonomous_test.go`) at the scale of the demo estate; they have not been exercised at the scale where the cap is actually the binding constraint (a very large estate with many simultaneously-qualifying recommendations).
