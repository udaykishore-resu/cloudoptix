# CloudOptix system requirements

Every requirement below has a stable ID matching a `Traceability:` marker actually present in the Go source (grepped and reconciled — see [`docs/traceability.md`](traceability.md) for the full REQ → SPEC → API → package → test matrix, and its "Flagged IDs" section for the handful of IDs this documentation set had to introduce because a source marker cited a SPEC with no corresponding REQ). Priority is `P0` (blocking — the platform is unsafe or non-functional without it), `P1` (core value proposition), or `P2` (quality-of-life / hardening).

Where a source file's `Traceability:` marker cites a range (e.g. `REQ-DSC-001..014`), that range is taken as authoritative for how many requirements this document defines in that prefix; where two files cite different ranges for the same prefix (e.g. `internal/adapters/aws/discovery/common.go` cites `REQ-DSC-001..010` while `internal/application/discovery/doc.go` cites `REQ-DSC-001..014`), the wider range is used and the narrower-citing file is understood to implement a subset.

## AI (REQ-AI-001 .. 013)

Governs every LLM-touching code path: providers, middleware, onboarding extraction, the copilot, RAG retrieval.

| ID | Title | Priority | Acceptance criteria |
|---|---|---|---|
| REQ-AI-001 | Pluggable LLM provider interface | P0 | Every provider (`anthropic`, `bedrock`, `deterministic`) implements exactly `ports.LLMProvider{Complete, Embed, Healthy}` with no method capable of mutation, approval, or persistence. |
| REQ-AI-002 | Bedrock as an alternate transport | P1 | `internal/adapters/llm/bedrock` serves the same Anthropic model family via SigV4-signed `InvokeModel`, sharing wire types with `anthropicwire`. |
| REQ-AI-003 | Deterministic provider with no API key | P0 | `internal/adapters/llm/deterministic` independently drives onboarding end-to-end and answers copilot questions by reading tool-result JSON, producing identical output for identical input, with zero external calls. |
| REQ-AI-004 | Standard middleware ordering | P1 | `middleware.Chain` applies sanitization before caching, quota/rate-limit before the circuit breaker, and tracing around the whole decorated call, in that documented order. |
| REQ-AI-005 | Prompt-injection defence on tool results | P0 | Every tool result and retrieved document passes through sanitization inside the middleware layer, regardless of whether the calling code remembers to sanitize its own prompt assembly. |
| REQ-AI-006 | Read-only tool registry | P0 | `copilot.Registry.Register` rejects, at registration time, any `ports.Tool` whose `ToolDefinition.ReadOnly` is not `true`. |
| REQ-AI-007 | Provider fallback on outage | P0 | `internal/adapters/llm/fallback` degrades every call to the deterministic provider when the configured primary provider is unhealthy, with no unhandled error surfaced to the caller. |
| REQ-AI-008 | Structured, schema-validated output for extraction | P0 | Every onboarding extraction call sets `CompletionRequest.ResponseSchema`; free-form prose is never parsed into `spec.Spec` fields. |
| REQ-AI-009 | Per-tenant rate limiting and quota | P1 | Middleware enforces a per-tenant daily token quota (`tenancy.Quotas.MaxCopilotTokensPerDay`) and rate limit, rejecting calls that exceed it before invoking the underlying provider. |
| REQ-AI-010 | Hybrid lexical + vector retrieval | P1 | `internal/adapters/rag` blends cosine similarity with a BM25-style lexical score into one hybrid rank rather than pure vector search. |
| REQ-AI-011 | Tenant-partitioned knowledge store | P0 | `KnowledgeStore.Search` has no tenant-filter parameter to omit; every query is structurally intersected with (platform-wide) ∪ (querying tenant's own) documents before ranking. |
| REQ-AI-012 | Deterministic embedding fallback | P1 | `HashEmbedder` produces a stable, genuinely useful cosine-similarity embedding with no API key, used whenever no `ports.LLMProvider.Embed` succeeds. |
| REQ-AI-013 | RAG grounds onboarding and copilot only | P2 | Retrieved passages are never asserted as fact directly; the calling agent remains responsible for grounding any claim against the tenant's structured data. |

## API (REQ-API-001 .. 050)

The HTTP surface, `internal/transport/http`. Grouped roughly two routes per requirement, in route-table order — see [`docs/traceability.md`](traceability.md) for the exact operation-to-requirement mapping against all 111 routes (103 authenticated + 8 public onboarding).

| ID | Title | Priority | Acceptance criteria |
|---|---|---|---|
| REQ-API-001 | Onboarding conversation lifecycle | P0 | `POST /onboarding`, `POST /onboarding/{id}/messages(/stream)` are reachable with no principal/permission required (public route table), never mounted under `RequirePermission`. |
| REQ-API-002 | Onboarding review and approval | P0 | `GET .../summary`, `PATCH .../{id}`, `POST .../approve`, `POST .../cancel` are all public routes that only ever produce a mutable draft `spec.Spec`, never a tenant. |
| REQ-API-003 | Specification retrieval and diff | P1 | `GET /specs/active`, `GET /specs/diff`, `GET /specs` require `spec:read`. |
| REQ-API-004 | Specification revision and import | P1 | `POST /specs/revisions`, `POST /specs/import` require `spec:write`; imported YAML round-trips through `spec.Spec`'s tags exactly. |
| REQ-API-005 | Specification version export | P2 | `GET /specs/{id}`, `GET /specs/{id}/export` require `spec:read` and return the exact YAML a customer would commit to git. |
| REQ-API-006 | Specification approve/reject | P0 | `POST /specs/{id}/approve`, `POST /specs/{id}/reject` require `spec:approve`, distinct from `spec:write`. |
| REQ-API-007 | Tenant profile management | P1 | `GET /tenant`, `PATCH /tenant` require `tenant:administer`. |
| REQ-API-008 | User directory | P1 | `GET /tenant/users`, `POST /tenant/users` require `tenant:administer`. |
| REQ-API-009 | User role and membership management | P1 | `PATCH /tenant/users/{id}/roles`, `DELETE /tenant/users/{id}` require `tenant:administer`. |
| REQ-API-010 | AWS account registration | P0 | `POST /aws-accounts` requires `aws:connect`; `GET /aws-accounts` requires `resource:read`. |
| REQ-API-011 | AWS account detail and verification | P0 | `GET /aws-accounts/{id}` requires `resource:read`; `POST /aws-accounts/{id}/verify` requires `aws:connect` and drives an `AssumeRole` probe. |
| REQ-API-012 | AWS account suspend / remove | P0 | `POST /aws-accounts/{id}/suspend`, `DELETE /aws-accounts/{id}` require `aws:connect`. |
| REQ-API-013 | Connection instructions | P2 | `GET /aws-accounts/{id}/instructions` requires `aws:connect` and returns the exact trust-policy/role-ARN instructions for the account's current state. |
| REQ-API-014 | Discovery run trigger and listing | P0 | `POST /discovery/runs` requires `discovery:run`; `GET /discovery/runs` requires `resource:read`. |
| REQ-API-015 | Discovery status and streaming | P1 | `GET /discovery/status`, `GET /discovery/runs/{id}/stream` require `resource:read` and reflect per-(service,region) job status. |
| REQ-API-016 | Discovery run detail | P2 | `GET /discovery/runs/{id}` requires `resource:read` and includes any denied IAM actions per failed job. |
| REQ-API-017 | Twin graph and cost-flow | P1 | `GET /architecture/graph` requires `resource:read`; `GET /architecture/cost-flow` requires `cost:read`. |
| REQ-API-018 | Twin rebuild | P1 | `POST /architecture/rebuild` requires `discovery:run` and recomputes the graph from the current inventory without a new AWS scan. |
| REQ-API-019 | Twin node detail and dependents | P2 | `GET /architecture/nodes/{id}`, `GET /architecture/nodes/{id}/dependents` require `resource:read`. |
| REQ-API-020 | Resource listing and detail | P1 | `GET /resources`, `GET /resources/{id}` require `resource:read`. |
| REQ-API-021 | Cost ingestion trigger | P0 | `POST /costs/ingest` requires `cost:read` and records `IngestResult.Source` (CUR or Cost Explorer). |
| REQ-API-022 | Cost summary and series | P1 | `GET /costs/summary`, `GET /costs/series` require `cost:read`. |
| REQ-API-023 | Cost breakdown and forecast | P1 | `GET /costs/breakdown`, `GET /costs/forecast` require `cost:read`. |
| REQ-API-024 | Cost explanation | P1 | `GET /costs/explain` requires `cost:read` and decomposes a change into named drivers. |
| REQ-API-025 | Anomaly detection and listing | P1 | `POST /costs/anomalies/detect` and `GET /costs/anomalies` require `cost:read`; detection uses robust (median/MAD) z-scores. |
| REQ-API-026 | Economic footprint computation | P0 | `POST /economics/compute` requires `economics:read` and returns direct/indirect/shared/unattributed decomposition. |
| REQ-API-027 | Footprint listing and retrieval | P1 | `GET /economics/footprints`, `GET /economics/footprints/{id}` require `economics:read`. |
| REQ-API-028 | Transaction unit economics | P0 | `GET /economics/transactions`, `.../unit-economics`, `.../unit-economics/history` require `economics:read`. |
| REQ-API-029 | Efficiency score and executive summary | P1 | `GET /economics/efficiency-score`, `GET /economics/executive-summary` require `economics:read`. |
| REQ-API-030 | Cost SLO authoring | P0 | `POST /cost-slos` requires `slo:write`; `DELETE /cost-slos/{id}` requires `slo:write`. |
| REQ-API-031 | Cost SLO evaluation and budget state | P0 | `GET /cost-slos`, `POST /cost-slos/evaluate`, `GET /cost-slos/budget-states` require `economics:read`. |
| REQ-API-032 | Recommendation generation | P0 | `POST /recommendations/analyze` requires `recommendation:run`. |
| REQ-API-033 | Recommendation listing and summary | P1 | `GET /recommendations`, `GET /recommendations/summary`, `GET /recommendations/rules` require `recommendation:read`. |
| REQ-API-034 | Recommendation detail and explanation | P1 | `GET /recommendations/{id}`, `GET /recommendations/{id}/explain` require `recommendation:read`. |
| REQ-API-035 | Recommendation dismiss / snooze | P1 | `POST /recommendations/{id}/dismiss`, `.../snooze` require `recommendation:run`. |
| REQ-API-036 | Execution plan and policy decision | P0 | `POST /recommendations/{id}/execution-plan` requires `execution:start`; `GET .../policy-decision` requires `policy:read`. |
| REQ-API-037 | Simulation: mutation and counterfactual | P1 | `POST /simulations/mutate`, `POST /simulations/counterfactual` require `simulation:run`. |
| REQ-API-038 | Simulation listing and retrieval | P2 | `GET /simulations`, `GET /simulations/{id}` require `simulation:run`. |
| REQ-API-039 | Cost Compiler run | P0 | `POST /compiler/compile` requires `compiler:run` and returns `simulate.CompilationResult`. |
| REQ-API-040 | Compilation retrieval and regression run | P1 | `GET /compiler/compilations/{id}`, `POST .../regression` require `compiler:run`. |
| REQ-API-041 | Regression suite management | P1 | `GET /regression/suites`, `POST /regression/suites` require `compiler:run`. |
| REQ-API-042 | Policy retrieval and versions | P1 | `GET /policies/active`, `GET /policies/versions` require `policy:read`. |
| REQ-API-043 | Policy save, validate, simulate, activate | P0 | `PUT /policies` and `POST /policies/{id}/activate` require `policy:write`; `POST /policies/validate`, `.../simulate` require `policy:read`. |
| REQ-API-044 | Approval workflow | P0 | `GET /approvals`, `POST /approvals`, `GET /approvals/{id}` require `approval:read`; `POST /approvals/{id}/decide` requires `approval:decide`, a distinct permission. |
| REQ-API-045 | Automation cycle triggers | P0 | `POST /automation/process`, `POST /automation/learn` require `automation:write`. |
| REQ-API-046 | Execution listing and detail | P1 | `GET /executions`, `GET /executions/{id}`, `GET /executions/{id}/stream` require `execution:read`. |
| REQ-API-047 | Execution start / cancel | P0 | `POST /executions/{id}/execute` requires `execution:start`; `POST /executions/{id}/cancel` requires `execution:cancel`. |
| REQ-API-048 | Execution validate / rollback | P0 | `POST /executions/{id}/validate` requires `execution:read`; `POST /executions/{id}/rollback` requires `rollback:start`, a distinct permission. |
| REQ-API-049 | Savings funnel and audit query | P1 | `GET /savings/funnel` requires `execution:read`; `GET /audit`, `GET /audit/verify`, `.../timeline` require `audit:read`. |
| REQ-API-050 | Copilot conversation surface | P0 | `POST /copilot/ask(/stream)`, `GET /copilot/conversations(/{id})`, `GET /copilot/suggestions` require `copilot:use`. |

## Audit (REQ-AUD-001 .. 009)

| ID | Title | Priority | Acceptance criteria |
|---|---|---|---|
| REQ-AUD-001 | Closed, exact action vocabulary | P1 | Every audit `Action` is one of a fixed, enumerated set (`tenant.created`, `spec.approved`, `aws.assume_role`, ...) — never a free-text string. |
| REQ-AUD-002 | Hash-chained records | P0 | Each `audit.Record` stores `PrevHash` computed over the prior record for the same tenant; `ComputeHash` is deterministic over a canonical field subset. |
| REQ-AUD-003 | Chain verification | P0 | `audit.VerifyChain` walks an ordered record slice and reports the first break, if any, with the offending sequence number. |
| REQ-AUD-004 | Tenant-scoped chain | P0 | The hash chain is computed per-tenant; one tenant's records never link into another's. |
| REQ-AUD-005 | Every consequential action is audited | P0 | Spec approval, AWS connection, discovery, cost anomaly, SLO breach, recommendation lifecycle, policy decision, execution and rollback each produce a record. |
| REQ-AUD-006 | Immutable in production storage | P1 | Production deployments additionally write records to object storage under a retention lock, independent of the mutable database copy. |
| REQ-AUD-007 | Actor attribution, human vs. machine | P1 | Every record distinguishes a human actor from `core.SystemPrincipal`/worker-originated actions. |
| REQ-AUD-008 | Queryable by tenant, action, time range, subject | P1 | `GET /audit` supports filtering by these dimensions without a full-table scan in the reference store. |
| REQ-AUD-009 | Timeline view per recommendation | P2 | `GET /audit/recommendations/{id}/timeline` reconstructs the full decision-to-execution history for one recommendation. |

## Automation (REQ-AUTO-001 .. 009)

| ID | Title | Priority | Acceptance criteria |
|---|---|---|---|
| REQ-AUTO-001 | Autonomous processing entry point | P0 | `ProcessAutonomous` is the sole entry point that acts without a human approval step already recorded. |
| REQ-AUTO-002 | Per-cycle change cap | P0 | Each autonomous cycle caps the number of plans it will build and execute, independent of any per-plan safety check. |
| REQ-AUTO-003 | Maintenance-window enforcement | P0 | An autonomous change outside a declared `spec.MaintenanceWindow` does not execute. |
| REQ-AUTO-004 | Monthly impact cap | P0 | A running total is tracked against `spec.Automation.MaxMonthlyImpact`; exceeding it halts further autonomous execution for the period. |
| REQ-AUTO-005 | Per-tenant concurrency bound | P1 | Concurrent autonomous executions for one tenant are bounded independent of global worker capacity. |
| REQ-AUTO-006 | Re-check governance before every AWS call | P0 | `Execute` calls `governance.Evaluate` again immediately before the first mutating AWS call, not only trusting the plan's original decision. |
| REQ-AUTO-007 | Idempotent retry on transient failure | P0 | Every mutating step carries an `IdempotencyKey`; a step is retried with exponential backoff only for `core.Retryable` errors. |
| REQ-AUTO-008 | Automation master switch | P0 | `spec.Automation.Enabled == false` blocks every autonomous execution regardless of policy. |
| REQ-AUTO-009 | Autonomous cycle audit trail | P1 | Every plan and execution `ProcessAutonomous` produces is individually auditable exactly like a human-triggered one. |

## Cost Compiler (REQ-CC-001 .. 008)

| ID | Title | Priority | Acceptance criteria |
|---|---|---|---|
| REQ-CC-001 | Terraform plan JSON parsing | P0 | `terraform_plan.go` normalizes `resource_changes` into `RawResource` keyed by canonical Terraform type. |
| REQ-CC-002 | Terraform HCL parsing | P1 | `terraform_hcl.go` parses raw `.tf` HCL (not only plan JSON) into the same intermediate shape. |
| REQ-CC-003 | CloudFormation parsing (JSON and YAML) | P1 | `cloudformation.go` normalizes both encodings into `RawResource`. |
| REQ-CC-004 | Kubernetes / Helm manifest parsing | P1 | `kubernetes.go` normalizes manifests and Helm-rendered output, translating Kubernetes kinds onto the Terraform-type vocabulary. |
| REQ-CC-005 | Unpriced vs. free distinction | P0 | A resource with no catalog data produces `Unpriced: true` with a reason; a genuinely zero-cost resource produces `Unpriced: false, AfterMonthly: 0`. Never conflated. |
| REQ-CC-006 | Usage-dependent modelling with stated assumptions | P0 | A resource whose cost depends on unobservable usage carries `UsageDependent: true` and one or more `Assumption` entries, each overridable. |
| REQ-CC-007 | Coverage and confidence discounting | P1 | `CompilationResult.Confidence` is discounted by the share of the projected delta that is usage-dependent modelling rather than fixed pricing. |
| REQ-CC-008 | Regression check suite | P0 | `RunRegression` evaluates every `RegressionCheck` (max increase %, max increase abs, cost-per-transaction, forbidden resource, required tags, max unpriced ratio, budget headroom) and returns a `Verdict`. |

## AI Cost Copilot (REQ-COP-001 .. 008)

| ID | Title | Priority | Acceptance criteria |
|---|---|---|---|
| REQ-COP-001 | Provider-agnostic agentic loop | P1 | The tool-calling loop drives identical control flow against the deterministic provider and a real function-calling model. |
| REQ-COP-002 | Bounded tool-call rounds | P1 | The loop terminates after a fixed maximum number of rounds regardless of provider behaviour. |
| REQ-COP-003 | Tool result convention | P1 | Every tool result is appended as `Role: tool, Name: <tool>, Content: JSON with a "summary" field`. |
| REQ-COP-004 | Grounding verification | P0 | Every resource id, account id and dollar figure in a final answer is checked against tool results returned in that conversation. |
| REQ-COP-005 | Regenerate-once on ungrounded answer | P0 | An answer failing grounding is regenerated exactly once before being returned with an explicit caveat. |
| REQ-COP-006 | Conversation history and retrieval | P1 | `GET /copilot/conversations`, `.../{id}` return the full turn history including tool calls. |
| REQ-COP-007 | Proactive suggestions | P2 | `GET /copilot/suggestions` surfaces relevant questions/findings without the user having to ask. |
| REQ-COP-008 | Permission-scoped tools | P0 | Each `ToolDefinition.RequiredPermission` is checked against the calling principal before the tool runs. |

## Costing (REQ-COST-001 .. 008)

| ID | Title | Priority | Acceptance criteria |
|---|---|---|---|
| REQ-COST-001 | CUR-preferred ingestion | P0 | `IngestResult.Source` records CUR when available, Cost Explorer otherwise; the caller is never left guessing which regime produced a record. |
| REQ-COST-002 | Amortized basis as primary | P0 | `BasisAmortized` is the default stored basis; a Savings Plan purchase does not appear as a one-off spike. |
| REQ-COST-003 | Charge-type separation | P1 | Credits, refunds, taxes and fees are never spread across workloads as consumption. |
| REQ-COST-004 | Multi-granularity roll-ups | P1 | Hourly, daily and monthly roll-ups are all derivable from the same ingested line items. |
| REQ-COST-005 | Forecasting | P1 | `costs/forecast` produces a projection with a stated confidence, not a bare number. |
| REQ-COST-006 | Robust anomaly detection | P0 | Anomaly detection uses median/MAD (robust z-score), not mean/stddev, so a real spike does not get absorbed into its own baseline. |
| REQ-COST-007 | Cost explanation / decomposition | P1 | `costs/explain` attributes a period-over-period change to named drivers (volume, rate, new resource, discount). |
| REQ-COST-008 | Idempotent re-ingestion | P1 | Re-running ingestion for an already-ingested period does not duplicate line items. |

## Discovery (REQ-DSC-001 .. 014)

| ID | Title | Priority | Acceptance criteria |
|---|---|---|---|
| REQ-DSC-001 | Per-(service, region) job concurrency | P0 | Discovery runs one bounded-pool job per (service × region) unit; one job's throttling or permission failure does not stop other jobs. |
| REQ-DSC-002 | Provider-neutral resource normalization | P0 | Every discovered item normalizes to `cloud.Resource` with a typed `Kind` from a closed enum; an unmodelled type yields `KindUnknown` plus a warning, never a mistyped resource. |
| REQ-DSC-003 | Scoped tombstoning | P0 | `MarkAbsent` is called only for (kind, region) pairs a *successful* job covered this run; a failed job's kind is left untouched, never deleted. |
| REQ-DSC-004 | Exponential backoff with full jitter | P1 | Retries for transient errors use full-jitter exponential backoff, bounded attempt count. |
| REQ-DSC-005 | No retry on permission error | P0 | A denied IAM action fails the job immediately and reports the exact denied action, never retried. |
| REQ-DSC-006 | Three-source attribution | P0 | Attribution resolves from a recognised tag, then an `AttributionRule` (priority order, first match), then the account's declared environment as weakest fallback. |
| REQ-DSC-007 | Provenance recorded per attribution | P0 | Every attributed field records which source won via `core.Provenance`. |
| REQ-DSC-008 | Topology edge discovery | P1 | Security groups, routing, and declared spec dependencies produce `cloud.Topology` edges (`routes_to`, `depends_on`, `egress_via`, etc.). |
| REQ-DSC-009 | Incremental re-scan | P1 | A subsequent run updates existing resources in place rather than duplicating them. |
| REQ-DSC-010 | Discovery run status and history | P1 | `DiscoveryRun` records per-job outcome, resources found, and any denied actions, queryable after the run completes. |
| REQ-DSC-011 | Streaming run progress | P2 | `GET /discovery/runs/{id}/stream` surfaces job-level progress as it happens. |
| REQ-DSC-012 | Multi-account scope | P1 | Discovery scope spans every `connected` AWS account for the tenant unless explicitly narrowed. |
| REQ-DSC-013 | Multi-region scope | P1 | Discovery scope spans every declared region on an account. |
| REQ-DSC-014 | Cost of a run never exceeds worst-case IAM footprint | P0 | Every discovery API call uses only `ScopeRead`/`ScopeAnalyze`-tier permissions, never an execute-scoped session. |

## Architecture Economics (REQ-ECON-001 .. 012)

| ID | Title | Priority | Acceptance criteria |
|---|---|---|---|
| REQ-ECON-001 | Multi-scope footprint computation | P0 | `Scope` spans organization, account, environment, application, workload, business capability, API, transaction, resource — same algorithm, different membership set. |
| REQ-ECON-002 | Direct/indirect/shared classification | P0 | Every attributed dollar carries a `CostClass` of exactly one of `direct`, `indirect`, `shared`. |
| REQ-ECON-003 | Consumers-graph-based splitting | P0 | Shared cost is split using `cloud.Topology.Consumers`' measured per-consumer shares, never an even split. |
| REQ-ECON-004 | Unattributed remainder shown, never hidden | P0 | A structural dependency with no recorded consumer edge is added to `Unattributed` and displayed, never silently divided across scopes. |
| REQ-ECON-005 | Business denominators as first-class objects | P0 | `spec.TransactionSpec` (name, monthly volume) is stored and versioned, not computed ad hoc from a spreadsheet. |
| REQ-ECON-006 | Cost-per-transaction computation | P0 | Unit economics divides a scope's total attributed cost by its declared transaction volume for the same period. |
| REQ-ECON-007 | Period-over-period unit-economics trend | P1 | `.../unit-economics/history` shows the trend, with a stated driver (volume change vs. rate change) when derivable. |
| REQ-ECON-008 | Executive summary roll-up | P1 | `economics/executive-summary` composes footprint, SLO, and efficiency-score data into one response. |
| REQ-ECON-009 | Footprint provenance | P1 | Every footprint figure records the provenance of the underlying attribution (confirmed tag vs. inferred rule). |
| REQ-ECON-010 | Cloud Efficiency Score | P0 | `ComputeEfficiencyScore` combines weighted factors (`StandardFactorWeights`) into one 0–100 score with a letter grade, each factor independently reported. |
| REQ-ECON-011 | Tenant-configurable factor weights | P2 | A tenant may override `StandardFactorWeights` (a serverless-first startup and a lift-and-shift estate do not share the same levers). |
| REQ-ECON-012 | Footprint recomputation on topology change | P1 | A footprint is recomputed after a discovery run changes the topology it was derived from, not served stale indefinitely. |

## Events (REQ-EVT-001 .. 008)

| ID | Title | Priority | Acceptance criteria |
|---|---|---|---|
| REQ-EVT-001 | At-least-once delivery | P0 | Neither `InProcess` nor the EventBridge/SQS pair promises exactly-once; both may redeliver, and callers are expected to use `IdempotencyKey`. |
| REQ-EVT-002 | In-process bus for zero-infra runtime | P0 | `events.InProcess` delivers, retries with backoff, and dead-letters, entirely in memory. |
| REQ-EVT-003 | EventBridge/SQS production bus | P1 | `EventBridgePublisher` and `SQSSubscriber` form one logical bus over real AWS services. |
| REQ-EVT-004 | No client-side DLQ reimplementation | P1 | `SQSSubscriber` never deletes a message its handler failed to process and does not run its own retry-count bookkeeping — SQS's native redrive policy owns that. |
| REQ-EVT-005 | Tenant-scoped events, fail closed | P0 | `Publish` refuses an event with an empty `core.TenantID` rather than treating it as platform-wide. |
| REQ-EVT-006 | Dead-letter visibility | P1 | Exhausted-retry events are queryable, not silently dropped. |
| REQ-EVT-007 | Ordered delivery not guaranteed | P2 | Neither implementation promises ordering across events for the same tenant; consumers are documented as needing to handle out-of-order delivery. |
| REQ-EVT-008 | Event schema versioning | P2 | `ports.Event` carries a type and payload shape a consumer can version-check before deserializing. |

## Execution (REQ-EXE-001 .. 014)

| ID | Title | Priority | Acceptance criteria |
|---|---|---|---|
| REQ-EXE-001 | Four-phase discipline | P0 | Plan, Execute, Validate, Rollback are separate, independently auditable calls; no single "just do it" path exists. |
| REQ-EXE-002 | Rollback plan required before execution | P0 | `execute.Plan.Executable` requires a non-nil `Rollback` and refuses a plan lacking a preceding snapshot step. |
| REQ-EXE-003 | Infeasible rollback marked, not hidden | P0 | A plan with an infeasible rollback (deletion, released IP) carries `DataLossRisk`/`InfeasibleReason` visibly rather than being blocked or silently treated as feasible. |
| REQ-EXE-004 | Idempotent step application | P0 | Every executor's `Apply` is idempotent on `IdempotencyKey`, verified by checking current state before mutating. |
| REQ-EXE-005 | Governance re-checked before AWS call | P0 | `Execute` re-evaluates governance immediately before the first mutating call, using current state, not the plan's original decision. |
| REQ-EXE-006 | Bounded retry on transient AWS error | P0 | A step failing on a classified-transient error is retried with exponential backoff up to a bounded attempt count. |
| REQ-EXE-007 | Precondition checks abort safely | P1 | A failed `StepPrecondition` aborts the plan before any mutation, always safely. |
| REQ-EXE-008 | Snapshot before mutation | P0 | Every mutating step is preceded by a snapshot step capturing rollback-relevant state. |
| REQ-EXE-009 | Post-change validation window | P0 | `Validate` observes the declared window and renders a `Verdict` against baseline before a change is considered final. |
| REQ-EXE-010 | Critical-check auto-rollback | P0 | A check named in `AutoRollbackOn` failing triggers an immediate rollback rather than merely an alert. |
| REQ-EXE-011 | Minimum-sample validation guard | P1 | `MinSamples` prevents declaring success on a quiet observation window with too little traffic to mean anything. |
| REQ-EXE-012 | Execution streaming | P2 | `GET /executions/{id}/stream` surfaces step-level progress in real time. |
| REQ-EXE-013 | Manual cancellation | P1 | `POST /executions/{id}/cancel` stops a plan before it reaches a terminal state, where feasible. |
| REQ-EXE-014 | Execution audit trail | P0 | Every phase transition of every plan is individually audited. |

## Governance (REQ-GOV-001 .. 011)

| ID | Title | Priority | Acceptance criteria |
|---|---|---|---|
| REQ-GOV-001 | Pure-function policy evaluation | P0 | `govern.Evaluate(policy, input)` performs no I/O, no clock read beyond `input.Now`, and is reproducible from its inputs alone. |
| REQ-GOV-002 | Deny-biased rule selection | P0 | Among matching rules, the most restrictive effect wins regardless of file order; the seeded default never suppresses a more permissive matching rule incorrectly, nor can a matching rule de-escalate below the correctly-computed most-restrictive match. |
| REQ-GOV-003 | Default-effect fallback only when nothing matches | P0 | `default_effect` applies only when zero rules matched; `Policy.Validate` refuses `default_effect: auto_execute`. |
| REQ-GOV-004 | Destructive-action guard, unconditional | P0 | `auto_execute` is force-downgraded to `require_approval` whenever `Input.Destructive` is true, regardless of policy. |
| REQ-GOV-005 | Automation-disabled guard | P0 | `auto_execute` is force-downgraded whenever `!Input.AutomationEnabled`. |
| REQ-GOV-006 | Economic error budget freeze | P0 | `Input.BudgetFreeze && MonthlyCostDelta > 0` forces `EffectProhibit` regardless of any matching rule. |
| REQ-GOV-007 | Economic error budget escalation | P1 | `Input.BudgetRequiresApproval` downgrades an otherwise-`auto_execute` decision to `require_approval`. |
| REQ-GOV-008 | Spec-level exclusions apply post-evaluation | P0 | Excluded actions/resources/tags from the approved spec tighten (never loosen) the domain decision, applied as a post-processing pass. |
| REQ-GOV-009 | Change-freeze windows | P1 | A resource/tag matching a declared freeze window is prohibited, applied the same way as spec-level exclusions. |
| REQ-GOV-010 | Segregation of duties | P1 | `RequireDistinctApprover` forbids the requester approving their own change when set by a rule. |
| REQ-GOV-011 | Policy validate / simulate / activate lifecycle | P0 | A policy is validated before it can be saved, can be simulated (diffed) against historical decisions, and only an activated policy is live for evaluation. |

## Learning (REQ-LRN-001 .. 006)

| ID | Title | Priority | Acceptance criteria |
|---|---|---|---|
| REQ-LRN-001 | Outcome-driven calibration | P1 | `Recalibrate` derives `execute.RuleCalibration` multipliers only from `execute.Outcome` records (predicted vs. actual). |
| REQ-LRN-002 | Minimum-sample guard | P0 | `execute.Calibrate` refuses to move a rule's multiplier away from neutral until a minimum outcome count is observed. |
| REQ-LRN-003 | No write access to policy or rules | P0 | The learning package never reads or writes `govern.Policy`, `optimize.Rule`, `spec.Spec`, or `ValidationCheck`. |
| REQ-LRN-004 | Calibration affects confidence claims only | P0 | A calibration multiplier changes a future recommendation's stated confidence/predicted-saving, never what it is allowed to do. |
| REQ-LRN-005 | Outcomes feed the RAG corpus | P1 | Validated outcomes are written as searchable tenant documents so the copilot can answer "how has this gone for us before." |
| REQ-LRN-006 | Learning is per-tenant | P0 | Calibration multipliers are scoped to the tenant whose outcomes produced them; no cross-tenant leakage. |

## Notifications (REQ-NOT-001 .. 010)

| ID | Title | Priority | Acceptance criteria |
|---|---|---|---|
| REQ-NOT-001 | Multi-channel delivery | P1 | SMTP, SES, Slack incoming webhook, and generic HMAC-signed webhook are each implemented as a `Notifier`. |
| REQ-NOT-002 | Secrets never in the specification | P0 | `NotificationChannel.SecretRef` is a reference resolved only at send time via `ports.SecretResolver`; no channel credential is ever stored in `spec.Spec`. |
| REQ-NOT-003 | Dispatch is not delivery | P0 | `Dispatch` only renders and persists `ports.Notification` records; a separate `SendPending` call performs the actual send. |
| REQ-NOT-004 | Durable across restart | P0 | Because dispatch and send are separate, a process restart between them loses nothing — pending notifications remain claimable. |
| REQ-NOT-005 | Retryable-failure re-enqueue | P1 | `SendPending` leaves a retryable failure unmarked (not `MarkFailed`) so the next sweep retries it. |
| REQ-NOT-006 | Terminal failure marking | P1 | A non-retryable failure or one exceeding `MaxSendAttempts` is marked failed for good. |
| REQ-NOT-007 | Quiet hours, critical bypass | P1 | A quiet-hours window suppresses everything below `SeverityCritical`; critical alerts always deliver. |
| REQ-NOT-008 | No re-delivery of suppressed quiet-hours messages | P2 | Once a quiet-hours window ends, whatever it suppressed is not re-delivered stale. |
| REQ-NOT-009 | Best-effort deduplication window | P1 | A short, bounded, in-memory dedup window (tenant, event type, subject, channel) suppresses retry-storm duplicates; not persisted, not exactly-once. |
| REQ-NOT-010 | Event-type-to-channel subscription mapping | P1 | `spec.Notifications.Subscriptions` maps event types to named channels, resolved at dispatch time. |

## Onboarding (REQ-ONB-001 .. 012)

| ID | Title | Priority | Acceptance criteria |
|---|---|---|---|
| REQ-ONB-001 | Eight-stage conversational flow | P1 | The agent moves through organization, application, aws, workloads, business, objectives, governance, review — governing question order only, not what may be recorded. |
| REQ-ONB-002 | Full-conversation re-extraction every turn | P0 | Every turn re-extracts from the FULL conversation so far, so an answer volunteered ahead of schedule is captured immediately. |
| REQ-ONB-003 | Schema-driven extraction, never prose parsing | P0 | Every turn builds a JSON Schema from remaining fields and calls `Complete` with `ResponseSchema` set. |
| REQ-ONB-004 | Provenance on every recorded value | P0 | Every value carries `CONFIRMED`, `INFERRED` (with a one-sentence rationale), `UNKNOWN`, or `REQUIRES_USER_CONFIRMATION`. |
| REQ-ONB-005 | Draft immutability outside the agent | P0 | `spec.StatusDraft` is the only mutable status; nothing but `Approve` transitions it, and nothing about a draft creates a tenant. |
| REQ-ONB-006 | Approve validates first | P0 | `Version.Approve` calls `spec.Validate()` and refuses over any blocking issue. |
| REQ-ONB-007 | Resumable across sessions | P1 | `Spec.OpenQuestions` is stored as part of the spec, so an interrupted onboarding resumes days later on another device. |
| REQ-ONB-008 | Mid-conversation value revision | P1 | A user may revise an already-`CONFIRMED` value later in the conversation; the later statement supersedes the earlier one. |
| REQ-ONB-009 | Completeness scoring | P1 | `Completeness` reports confirmed/inferred/unknown/needs-confirmation counts and a `ReadyForReview` boolean. |
| REQ-ONB-010 | Blocking vs. quality-reducing open questions | P1 | `OpenQuestion.Blocking` distinguishes questions that must be answered from ones that merely reduce quality. |
| REQ-ONB-011 | Identical behaviour across providers | P0 | The same interpreter (`application.go`) applies to the structured extraction result whether it came from the deterministic provider or a real model. |
| REQ-ONB-012 | Conversation-to-version linkage | P1 | `Version.ConversationID` links an approved spec back to the exact chat that produced it. |

## Operations (REQ-OPS-001 .. 004)

| ID | Title | Priority | Acceptance criteria |
|---|---|---|---|
| REQ-OPS-001 | Graceful lifecycle | P0 | Shutdown waits for in-flight requests within a bounded deadline; liveness never depends on an external dependency, readiness always does. |
| REQ-OPS-002 | Twelve-factor layered configuration | P0 | Config resolves `defaults → config.yaml → environment variables → flags`, each layer overriding the last. |
| REQ-OPS-003 | Secret-shaped config fields reject literal values | P1 | A config field typed `Secret` fails to load from a literal YAML value rather than accepting it — enforced by the type's unmarshaller, not code review. *(No source `Traceability:` marker cites REQ-OPS-003 directly; introduced by this document to give `config.go`'s documented secret-handling behaviour a requirement ID — flagged in `docs/traceability.md`.)* |
| REQ-OPS-004 | Structured observability | P0 | Every span, metric and log line is emitted through the shared telemetry wiring, correlated (trace id in the log line) even without a live OTLP exporter. |

## Optimization (REQ-OPT-001 .. 014)

| ID | Title | Priority | Acceptance criteria |
|---|---|---|---|
| REQ-OPT-001 | Rule registry ownership | P0 | `Registry` owns which rules run, at what threshold, for which tenant, loaded from `rules/`'s versioned YAML. |
| REQ-OPT-002 | Rule pack is configuration, not code | P0 | A threshold change is a YAML diff; the engine's Go code does not change. |
| REQ-OPT-003 | Compute rightsizing and waste rules | P0 | EC2 rightsizing, prior-generation, never-used, burst-credit, commitment-gap, schedule-offhours, spot-candidacy and stopped-storage rules are each implemented. |
| REQ-OPT-004 | Storage waste rules | P0 | EBS gp2→gp3, orphaned snapshot, overprovisioned, unattached and unused-AMI rules are each implemented. |
| REQ-OPT-005 | Database waste rules | P0 | RDS Aurora candidacy, backup retention, gp2→gp3, idle, multi-AZ-in-nonprod, overprovisioned storage, oversized, unnecessary-replica; S3 lifecycle/incomplete-multipart/noncurrent-versions/wrong-storage-class rules are each implemented. |
| REQ-OPT-006 | Confidence computed from structured facts | P0 | `ComputeConfidence` weighs stability, coverage, window, dependency completeness, criticality and age — never an LLM self-report — and is multiplicatively adjusted by historical calibration last. |
| REQ-OPT-007 | Risk and network-waste rules | P0 | `ComputeRiskAssessment` scores capacity-reducing and storage-touching actions against SLO headroom; NAT-redundant, NAT-VPC-endpoint, cross-AZ-chatter, EIP-unattached, LB-idle and CloudFront-egress rules are each implemented. |
| REQ-OPT-008 | Blast radius and serverless rules | P0 | `ComputeBlastRadius` walks the real dependency graph to depth 6; Lambda excessive-timeout, Graviton-candidacy, memory-cost-curve and provisioned-concurrency rules are each implemented. |
| REQ-OPT-009 | Kubernetes/container waste rules | P0 | ECS task-count, EKS consolidation, EKS nodegroup-no-spot, EKS nodegroup-overprovisioned, Fargate-vs-EC2 and pod-request-oversized rules are each implemented. |
| REQ-OPT-010 | Observability-cost rules | P1 | CloudWatch high-cardinality-metric and log-retention rules are implemented; KMS/Secrets-unused rule is implemented. |
| REQ-OPT-011 | Deterministic finding ordering | P0 | Two runs against identical inputs produce identical findings in identical order — no dependence on model sampling, wall-clock time, or map iteration order. |
| REQ-OPT-012 | Recommendation enrichment | P0 | The recommendation builder enriches a `Finding` into a `Recommendation` (predicted effect, risk, blast radius, executable action); an LLM may narrate one but never create one. |
| REQ-OPT-013 | Insufficient-telemetry guard | P0 | A rule does not fire a rightsizing/idle finding on a resource whose metric coverage is below the rule's declared minimum. |
| REQ-OPT-014 | Tail-based, not mean-based, rightsizing | P0 | Rightsizing evaluates a percentile (P95/P99), never the mean, avoiding the exact failure mode of an outage the night a batch job runs. |
| REQ-OPT-015 | Overlapping recommendations are never summed | P0 | Recommendations competing for the same dimension of one resource's spend form a conflict group; the priority formula picks one primary, the rest are retained as alternatives, and every aggregate that reports a total counts primaries only. |
| REQ-OPT-016 | Rule/executor parameter contract | P0 | A rule's `Recommendation.Parameters` use the executor's key vocabulary; a rule whose action has a registered executor must satisfy that action's declared parameter contract, and an action with no executor is either advisory or explicitly enumerated as unbuilt. |

## Savings (REQ-SAV-001 .. 007)

| ID | Title | Priority | Acceptance criteria |
|---|---|---|---|
| REQ-SAV-001 | Six-rung savings ladder | P0 | `SavingsStage` is exactly one of `potential, approved, planned, executed, validated, realized`, with a stable `Order()`. |
| REQ-SAV-002 | Realized requires post-change billing confirmation | P0 | `StageRealized` is set only from billing data observed after the change, never from the original prediction restated. |
| REQ-SAV-003 | Lost savings recorded, not hidden | P0 | A rollback at any stage marks the `SavingsRecord` `Lost` with a `LostReason`, never silently removed from the funnel. |
| REQ-SAV-004 | Funnel query by stage | P1 | `GET /savings/funnel` reports count and dollar value at every rung for a period. |
| REQ-SAV-005 | Per-rule savings attribution | P1 | Every `SavingsRecord` links back to the `optimize.RuleID` that produced it. |
| REQ-SAV-006 | Stage transitions are monotonic | P1 | A record's stage only advances forward (or is marked `Lost`) — it never regresses without an explicit rollback event. |
| REQ-SAV-007 | Predicted vs. realized delta tracked | P1 | The gap between predicted saving and realized saving is stored, feeding the learning loop's calibration input. |

## Security (REQ-SEC-001 .. 005)

| ID | Title | Priority | Acceptance criteria |
|---|---|---|---|
| REQ-SEC-001 | AssumeRole-only AWS access | P0 | No function, constructor option, or struct field in `internal/adapters/aws/sts` accepts a static AWS key; every credential is minted by `sts:AssumeRole` with the account's `ExternalID`. |
| REQ-SEC-002 | Authenticated principal on every call | P0 | Every application-service method takes an explicit `core.Principal`; there is no ambient identity. |
| REQ-SEC-003 | Tenant isolation at every layer | P0 | `core.GuardTenant` is structural inside every repository method; `KnowledgeStore.Search` has no tenant filter to omit. |
| REQ-SEC-004 | AI input/output sanitization | P0 | Tool results and retrieved documents pass through prompt-injection defence before reaching a model, and a model's structured output is validated before use. *(Source markers cite `SPEC-SEC-004` from `auth/doc.go`, `middleware/doc.go` and `http/doc.go` without a dedicated `REQ-SEC-004`; introduced here to give it a home requirement — flagged in `docs/traceability.md`.)* |
| REQ-SEC-005 | Table-driven RBAC | P0 | `rolePermissions` is the single source of truth for authorization; a role's permission set is never computed per-user or duplicated per handler. |

## Simulation (REQ-SIM-001 .. 010)

| ID | Title | Priority | Acceptance criteria |
|---|---|---|---|
| REQ-SIM-001 | Simulated AWS matches real port contracts | P0 | `awssim` reads and writes one in-memory `Estate`; discovered resources, billed cost, sampled metrics and applied mutations are all internally consistent. |
| REQ-SIM-002 | Deterministic, seeded demo estate | P0 | `BuildDemoEstate` with a fixed seed produces byte-identical output across runs (verified by `demo_test.go`'s two-build equality assertion). |
| REQ-SIM-003 | Real reversible mutation in simulation | P0 | `awssim.Apply` performs a genuine, reversible state change, not a stub response. |
| REQ-SIM-004 | Architecture Mutation Engine candidate generation | P0 | The mutation engine generates scored candidates against the tenant's stored `Inventory`/`Topology`. |
| REQ-SIM-005 | Eight-dimension candidate scoring | P0 | Every candidate is scored on cost, performance, reliability, scalability, security, operational complexity, migration effort and risk — never cost alone. |
| REQ-SIM-006 | Counterfactual Engine | P0 | The counterfactual engine reprices the current inventory under a stated hypothetical (traffic multiplier, topology removal, commitment posture change). |
| REQ-SIM-007 | Assumptions are stated and overridable | P0 | Every unobservable input to a mutation or counterfactual result travels as a `simulate.Assumption` with provenance and sensitivity, user-overridable. |
| REQ-SIM-008 | Model vs. measurement distinction | P0 | Every simulated number is presented with a confidence and pricing basis, never as an equal-footing measurement. |
| REQ-SIM-009 | Compiler wiring shares the same service surface | P1 | `SimulationService` bundles `Compile`, `GetCompilation`, `RunRegression`, `UpsertRegressionSuite`, `ListRegressionSuites` alongside mutation/counterfactual. |
| REQ-SIM-010 | Simulation listing and retrieval | P2 | `GET /simulations`, `GET /simulations/{id}` return prior runs for the tenant. |

## Cost SLOs (REQ-SLO-001 .. 006)

| ID | Title | Priority | Acceptance criteria |
|---|---|---|---|
| REQ-SLO-001 | Six SLO kinds | P0 | `SLOKind` covers absolute spend, cost-per-transaction, cost-per-request, cost-per-customer, waste ratio, and efficiency score. |
| REQ-SLO-002 | Configurable window | P1 | `SLOWindow` supports calendar month, rolling 7d/30d, and calendar quarter. |
| REQ-SLO-003 | Pro-rated consumption | P0 | Budget consumption is computed against a pro-rated target, not a full-window target compared to month-to-date spend. |
| REQ-SLO-004 | Burn rate and exhaustion projection | P0 | `EvaluateBudget` computes `BurnRate` and, when `> 1`, a projected `ExhaustionDate` within the window. |
| REQ-SLO-005 | Declared breach response | P0 | Every `CostSLO` names `BreachActions` in advance; a triggered breach applies exactly those actions (or the platform default if none declared). |
| REQ-SLO-006 | Budget state feeds governance | P0 | `AllowsCostIncrease()` is consulted by `govern.Input.BudgetFreeze`/`BudgetRequiresApproval`, tightening — never loosening — a policy decision. |

## Specification (REQ-SPEC-001 .. 015)

| ID | Title | Priority | Acceptance criteria |
|---|---|---|---|
| REQ-SPEC-001 | Complete, versioned specification schema | P0 | `spec.Spec` covers organization, application, AWS, workloads, business, objectives, optimization, automation, governance, security, observability, notifications, teams. |
| REQ-SPEC-002 | YAML round-trip fidelity | P0 | `cloudoptix.yaml` round-trips through `spec.Spec`'s `yaml` tags exactly — no lossy re-serialization. |
| REQ-SPEC-003 | Field-level provenance wrapper | P0 | `Field[T]` carries value, provenance, source, rationale, and timestamps, distinct from the bare value a customer edits in YAML. |
| REQ-SPEC-004 | Draft mutability, approved immutability | P0 | Only `StatusDraft` is mutable; `StatusApproved`, `StatusSuperseded`, `StatusRejected` are terminal and immutable. |
| REQ-SPEC-005 | Monotonic versioning | P1 | Each approved change produces a new `Version` with an incrementing number and a `ParentID` link. |
| REQ-SPEC-006 | Structural diffing | P0 | `Diff` flattens both versions to dotted paths and compares values; a reordered YAML block produces no diff. |
| REQ-SPEC-007 | Diff impact annotation | P1 | Each `Change` carries an `Impact` string explaining its downstream consequence, not just before/after values. |
| REQ-SPEC-008 | Deterministic pre-approval validation | P0 | `Validate()` performs exact, deterministic checks (account id format, external id presence, maintenance window sanity) with no model call. |
| REQ-SPEC-009 | Blocking vs. non-blocking validation issues | P0 | `ValidationResult.HasBlocking()` distinguishes issues that must be fixed from ones that are advisory. |
| REQ-SPEC-010 | Approve requires clean validation | P0 | `Version.Approve` refuses when `Validation.HasBlocking()` is true. |
| REQ-SPEC-011 | Diff computed at every version creation | P1 | `Version.Diff` is computed and stored at creation time, not recomputed on every read. |
| REQ-SPEC-012 | Secret-free specification | P0 | No field of `spec.Spec` stores a resolvable secret value directly; channel/credential secrets are `SecretRef` indirections only. |
| REQ-SPEC-013 | Import from external YAML | P1 | `POST /specs/import` accepts a hand-authored `cloudoptix.yaml` and validates it identically to an onboarding-produced one. |
| REQ-SPEC-014 | Completeness scoring on every version | P1 | Every `Version` carries a `Completeness` snapshot alongside its `Validation`. |
| REQ-SPEC-015 | Open questions persisted with the spec | P1 | `Spec.OpenQuestions` is part of the persisted spec, not ephemeral conversation state. |

## Tenancy (REQ-TEN-001 .. 008)

| ID | Title | Priority | Acceptance criteria |
|---|---|---|---|
| REQ-TEN-001 | Plan-based quotas, not feature gating | P0 | `QuotasFor(plan)` sets numeric limits; every tenant on every plan gets policy, approvals, rollback and audit — no safety feature is tier-gated. |
| REQ-TEN-002 | Distinct tenant identifier type | P0 | `core.TenantID` is a distinct Go type, not a bare string, everywhere it is used. |
| REQ-TEN-003 | Tenant lifecycle states | P1 | A tenant progresses through onboarding, active, suspended states with recorded transitions. |
| REQ-TEN-004 | Multi-account membership | P0 | A tenant may register multiple `cloud.AWSAccount`s, each independently connected/suspended. |
| REQ-TEN-005 | User invitation and role assignment | P1 | `POST /tenant/users` invites a user with an initial role set; roles are updatable independently. |
| REQ-TEN-006 | Retention-days quota enforcement | P2 | `Quotas.RetentionDays` bounds how long tenant data (audit, cost history) is retained per plan. |
| REQ-TEN-007 | Resource-count quota enforcement | P1 | `Quotas.MaxResources` bounds inventory size per plan tier. |
| REQ-TEN-008 | Concurrent-discovery quota enforcement | P2 | `Quotas.MaxConcurrentDiscovery` bounds simultaneous discovery jobs per tenant. |

## Testing (REQ-TEST-001 .. 002)

| ID | Title | Priority | Acceptance criteria |
|---|---|---|---|
| REQ-TEST-001 | Deterministic reproducibility across the AI-dependent surface | P0 | Onboarding, extraction, copilot tool routing and grounding all produce identical output on identical input against the deterministic provider. *(No source `Traceability:` marker cites REQ-TEST-001 directly; introduced here as the natural pair to REQ-TEST-002 — flagged in `docs/traceability.md`.)* |
| REQ-TEST-002 | Platform runs end to end with no external dependency | P0 | `memstore` + `awssim` + `deterministic` + `events.InProcess` together are sufficient to exercise every application service in tests and the demo tenant, with no Postgres, Redis, or AWS account reachable. |

## Digital Twin (REQ-TWIN-001 .. 009)

| ID | Title | Priority | Acceptance criteria |
|---|---|---|---|
| REQ-TWIN-001 | One graph, multiple view projections | P0 | The underlying resource/relationship graph never changes shape; a view picks which fields drive size/colour. |
| REQ-TWIN-002 | Uniform node shape across views | P0 | Every view produces the same `TwinNode` type with different fields populated, documented by `TwinGraph.Legend`. |
| REQ-TWIN-003 | Collapse-safe synthetic nodes | P1 | A collapsed subgraph satisfies the identical `TwinNode` shape as any other node. |
| REQ-TWIN-004 | Cost view | P0 | Cost view sizes nodes by `MonthlyCost`, colours by spend tier. |
| REQ-TWIN-005 | Reliability view | P0 | Reliability view sizes nodes by blast radius, colours by risk. |
| REQ-TWIN-006 | Cost-flow conservation, provable by construction | P0 | Every resource's own cost is injected exactly once, at its own node; summing every node's displayed amount at any level equals the sum of resources' own billed cost, plus the honest unattributed remainder. |
| REQ-TWIN-007 | Dependents traversal | P0 | `Dependents(id, maxDepth)` returns the transitive downstream closure along request-path edges, discounted by edge confidence. |
| REQ-TWIN-008 | Rebuild without a new AWS scan | P1 | `POST /architecture/rebuild` recomputes the graph from the current stored inventory. |
| REQ-TWIN-009 | Node detail and dependents endpoints | P2 | `GET /architecture/nodes/{id}`, `.../dependents` are independently queryable. |

## Utilization (REQ-UTL-001 .. 007)

| ID | Title | Priority | Acceptance criteria |
|---|---|---|---|
| REQ-UTL-001 | Percentile summary computation | P0 | `core.SummarizeSamples` computes percentiles, mean, stddev and stability from a bare value slice, in the domain layer. |
| REQ-UTL-002 | Trend computation from timestamped points | P0 | The utilization package computes `Trend` from timestamped samples, distinct from the time-agnostic percentile computation. |
| REQ-UTL-003 | Seasonality via autocorrelation | P0 | Seasonality is detected via 24-hour and 168-hour lag autocorrelation on an hourly-resampled series, not FFT or a fitted model. |
| REQ-UTL-004 | Peak-hours identification | P1 | `PeakHours` is derived from the same resampled series feeding seasonality detection. |
| REQ-UTL-005 | Coverage fraction reported | P0 | Every `core.Percentiles` summary reports what fraction of the expected window was actually observed. |
| REQ-UTL-006 | CloudWatch metric collection | P0 | `internal/adapters/aws/metrics/cloudwatch.go` collects the metric set each rule declares it needs. |
| REQ-UTL-007 | Metric collection failure isolation | P1 | A CloudWatch throttle/permission failure for one resource does not abort collection for the rest of the batch. |

## Validation (REQ-VAL-001 .. 008)

| ID | Title | Priority | Acceptance criteria |
|---|---|---|---|
| REQ-VAL-001 | Named validation checks | P0 | Every `ValidationCheck` has a `Name`, a `Metric`, a `Statistic`, and a `Comparison` against baseline. |
| REQ-VAL-002 | Critical vs. non-critical checks | P0 | `ValidationCheck.Critical` distinguishes a check whose failure must trigger rollback from one that only alerts. |
| REQ-VAL-003 | Baseline vs. observed window | P0 | `ValidationResult` carries both `BaselineWindow` and `ObservedWindow` explicitly, never an implicit "before" assumption. |
| REQ-VAL-004 | Four-verdict outcome | P0 | `Verdict` is exactly one of `success, partial_success, failure, inconclusive`. |
| REQ-VAL-005 | Inconclusive is a real outcome | P1 | Insufficient samples or ambiguous signal produces `VerdictInconclusive`, not a forced success or failure. |
| REQ-VAL-006 | Per-check outcome detail | P1 | `ValidationResult.Checks` reports each check's individual pass/fail, not only the aggregate verdict. |
| REQ-VAL-007 | Explanation accompanies every verdict | P1 | `ValidationResult.Explanation` states in plain language why the verdict landed where it did. |
| REQ-VAL-008 | Manual validation trigger | P2 | `POST /executions/{id}/validate` allows an operator to force an early validation pass. |
