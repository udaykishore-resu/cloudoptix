# Traceability matrix

This document is built from two sources of ground truth, both re-derived directly from the repository rather than asserted: every `// Traceability: ...` comment actually present in the Go source (125 markers, grepped with `grep -rn "Traceability:" --include=*.go .` and reconciled below verbatim), and the live HTTP route table (`internal/transport/http/routes.go`, 111 operations). Nothing in this matrix references an ID that does not either appear in a source marker or is explicitly called out in [Flagged IDs](#flagged-ids) as introduced by this documentation effort.

## How to read this document

- **Table A** is the source of truth: every file's `Traceability:` comment, unedited in substance.
- **Table B** maps every `SPEC-*` prefix to the specification document that covers it in depth.
- **Table C** maps every HTTP operation to the `REQ-API-*` requirement covering it (see [`docs/requirements.md`](requirements.md) for acceptance criteria).
- **Table D** maps every `REQ-*` prefix to its owning Go package(s) and the test files that exercise it.
- **Flagged IDs** lists every requirement or spec ID this documentation set had to introduce with no directly-citing source marker, and every discrepancy found between a source comment and the code it describes.

## Table A — source `Traceability:` markers, verbatim

| Source file | REQ IDs cited | SPEC IDs cited |
|---|---|---|
| `internal/adapters/aws/awserr/translate.go` | REQ-DSC-002, REQ-SEC-001 | SPEC-DSC-001 |
| `internal/adapters/aws/costing/costexplorer.go` | REQ-COST-001..008 | SPEC-COST-001 |
| `internal/adapters/aws/discovery/common.go` | REQ-DSC-001..010 | SPEC-DSC-001 |
| `internal/adapters/aws/executor/common.go` | REQ-EXE-001..014 | SPEC-AUTO-001 |
| `internal/adapters/aws/metrics/cloudwatch.go` | REQ-UTL-001..004 | SPEC-UTL-002 |
| `internal/adapters/aws/sts/doc.go` | REQ-SEC-001 | SPEC-SEC-001 |
| `internal/adapters/awssim/doc.go` | REQ-SIM-001..009 | SPEC-DEMO-001 |
| `internal/adapters/events/doc.go` | REQ-EVT-001..008 | SPEC-ARCH-004 |
| `internal/adapters/llm/anthropic/provider.go` | REQ-AI-001, REQ-AI-006 | SPEC-AI-001 |
| `internal/adapters/llm/bedrock/provider.go` | REQ-AI-002, REQ-AI-006 | SPEC-AI-001 |
| `internal/adapters/llm/deterministic/doc.go` | REQ-AI-003, REQ-AI-007, REQ-ONB-002 | SPEC-AI-001 |
| `internal/adapters/llm/doc.go` | REQ-AI-001..009 | SPEC-AI-001..004 |
| `internal/adapters/llm/fallback/provider.go` | REQ-AI-007 | SPEC-AI-002 |
| `internal/adapters/llm/middleware/chain.go` | REQ-AI-004, REQ-AI-005, REQ-AI-007 | SPEC-AI-002 |
| `internal/adapters/llm/middleware/doc.go` | REQ-AI-005, REQ-AI-009 | SPEC-AI-002, SPEC-SEC-004 |
| `internal/adapters/memstore/doc.go` | REQ-TEST-002 | SPEC-ARCH-003, SPEC-SEC-003 |
| `internal/adapters/notify/doc.go` | REQ-NOT-001..010 | SPEC-ARCH-005 |
| `internal/adapters/postgres/money.go` | — | SPEC-ARCH-003, SPEC-COST-001 |
| `internal/adapters/pricing/pricing.go` | REQ-COST-002, REQ-OPT-003 | SPEC-COST-003 |
| `internal/adapters/rag/doc.go` | REQ-AI-010..013 | SPEC-AI-005, SPEC-SEC-003 |
| `internal/application/automation/doc.go` | REQ-EXE-001..014, REQ-VAL-001..008, REQ-AUTO-001..009 | SPEC-AUTO-001..006 |
| `internal/application/compiler/doc.go` | REQ-CC-001..008 | SPEC-SIM-001 |
| `internal/application/copilot/doc.go` | REQ-AI-006..010, REQ-COP-001..008 | SPEC-AI-002, SPEC-AI-003 |
| `internal/application/copilot/service.go` | REQ-AI-006..010, REQ-COP-001..008 | SPEC-AI-002, SPEC-AI-003 |
| `internal/application/costing/doc.go` | REQ-COST-001..008 | SPEC-COST-001..004 |
| `internal/application/discovery/doc.go` | REQ-DSC-001..014 | SPEC-DSC-001..003 |
| `internal/application/economics/doc.go` | REQ-ECON-001..012 | SPEC-ECON-001..005 |
| `internal/application/governance/doc.go` | REQ-GOV-001..011 | SPEC-GOV-001, SPEC-AI-004 |
| `internal/application/learning/doc.go` | REQ-LRN-001..006 | SPEC-OPT-008 |
| `internal/application/onboarding/doc.go` | REQ-ONB-001..012, REQ-AI-001..003 | SPEC-ONB-001..006 |
| `internal/application/optimization/blast.go` | REQ-OPT-008 | SPEC-OPT-005 |
| `internal/application/optimization/confidence.go` | REQ-OPT-006 | SPEC-OPT-004 |
| `internal/application/optimization/doc.go` | REQ-OPT-001..014 | SPEC-OPT-001..008 |
| `internal/application/optimization/registry_init.go` | REQ-OPT-001 | — |
| `internal/application/optimization/risk.go` | REQ-OPT-007 | SPEC-OPT-006 |
| `internal/application/optimization/rule_cloudfront_egress.go` | REQ-OPT-007 | — |
| `internal/application/optimization/rule_cloudwatch_high_cardinality.go` | REQ-OPT-010 | — |
| `internal/application/optimization/rule_cloudwatch_log_retention.go` | REQ-OPT-010 | — |
| `internal/application/optimization/rule_cross_az_chatter.go` | REQ-OPT-007 | — |
| `internal/application/optimization/rule_dynamodb_billing_mode.go` | REQ-OPT-006 | — |
| `internal/application/optimization/rule_ebs_gp2_gp3.go` | REQ-OPT-004 | SPEC-OPT-002 |
| `internal/application/optimization/rule_ebs_orphaned_snapshot.go` | REQ-OPT-004 | — |
| `internal/application/optimization/rule_ebs_overprovisioned.go` | REQ-OPT-004 | — |
| `internal/application/optimization/rule_ebs_snapshot_retention.go` | REQ-OPT-004 | — |
| `internal/application/optimization/rule_ebs_unattached.go` | REQ-OPT-004 | — |
| `internal/application/optimization/rule_ebs_unused_ami.go` | REQ-OPT-004 | — |
| `internal/application/optimization/rule_ec2_burst_credit.go` | REQ-OPT-003 | — |
| `internal/application/optimization/rule_ec2_commitment_gap.go` | REQ-OPT-003 | SPEC-OPT-002 |
| `internal/application/optimization/rule_ec2_never_used.go` | REQ-OPT-003 | — |
| `internal/application/optimization/rule_ec2_oversized_declared.go` | REQ-OPT-003 | SPEC-OPT-002 |
| `internal/application/optimization/rule_ec2_prev_generation.go` | REQ-OPT-003 | — |
| `internal/application/optimization/rule_ec2_rightsize.go` | REQ-OPT-003 | SPEC-OPT-002 |
| `internal/application/optimization/rule_ec2_schedule_offhours.go` | REQ-OPT-003 | — |
| `internal/application/optimization/rule_ec2_spot_candidacy.go` | REQ-OPT-003 | SPEC-OPT-002 |
| `internal/application/optimization/rule_ec2_stopped_storage.go` | REQ-OPT-003 | — |
| `internal/application/optimization/rule_ecs_task_count.go` | REQ-OPT-009 | — |
| `internal/application/optimization/rule_eip_unattached.go` | REQ-OPT-007 | — |
| `internal/application/optimization/rule_eks_consolidation.go` | REQ-OPT-009 | — |
| `internal/application/optimization/rule_eks_nodegroup_no_spot.go` | REQ-OPT-009 | — |
| `internal/application/optimization/rule_eks_nodegroup_overprovisioned.go` | REQ-OPT-009 | — |
| `internal/application/optimization/rule_fargate_vs_ec2.go` | REQ-OPT-009 | — |
| `internal/application/optimization/rule_k8s_pod_requests_oversized.go` | REQ-OPT-009 | — |
| `internal/application/optimization/rule_kms_secrets_unused.go` | REQ-OPT-010 | — |
| `internal/application/optimization/rule_lambda_excessive_timeout.go` | REQ-OPT-008 | — |
| `internal/application/optimization/rule_lambda_graviton.go` | REQ-OPT-008 | — |
| `internal/application/optimization/rule_lambda_memory_cost_curve.go` | REQ-OPT-008 | — |
| `internal/application/optimization/rule_lambda_provisioned_concurrency.go` | REQ-OPT-008 | — |
| `internal/application/optimization/rule_lb_idle.go` | REQ-OPT-007 | — |
| `internal/application/optimization/rule_nat_redundant.go` | REQ-OPT-007 | — |
| `internal/application/optimization/rule_nat_vpc_endpoint.go` | REQ-OPT-007 | — |
| `internal/application/optimization/rule_rds_aurora_candidacy.go` | REQ-OPT-005 | — |
| `internal/application/optimization/rule_rds_backup_retention.go` | REQ-OPT-005 | — |
| `internal/application/optimization/rule_rds_gp2_gp3.go` | REQ-OPT-005 | — |
| `internal/application/optimization/rule_rds_idle.go` | REQ-OPT-005 | — |
| `internal/application/optimization/rule_rds_multiaz_nonprod.go` | REQ-OPT-005 | — |
| `internal/application/optimization/rule_rds_overprovisioned_storage.go` | REQ-OPT-005 | — |
| `internal/application/optimization/rule_rds_oversized.go` | REQ-OPT-005 | — |
| `internal/application/optimization/rule_rds_unnecessary_replica.go` | REQ-OPT-005 | — |
| `internal/application/optimization/rule_s3_incomplete_multipart.go` | REQ-OPT-005 | — |
| `internal/application/optimization/rule_s3_intelligent_tiering.go` | REQ-OPT-005 | SPEC-OPT-002 |
| `internal/application/optimization/rule_s3_no_lifecycle.go` | REQ-OPT-005 | — |
| `internal/application/optimization/rule_s3_noncurrent_versions.go` | REQ-OPT-005 | — |
| `internal/application/optimization/rule_s3_wrong_storage_class.go` | REQ-OPT-005 | — |
| `internal/application/optimization/service.go` | REQ-OPT-001, REQ-OPT-012 | SPEC-OPT-006 |
| `internal/application/simulation/doc.go` | REQ-SIM-001..010 | SPEC-SIM-001 |
| `internal/application/twin/doc.go` | REQ-TWIN-001..009 | SPEC-TWIN-001..003 |
| `internal/application/utilization/doc.go` | REQ-UTL-001..007 | SPEC-UTL-002 |
| `internal/domain/audit/audit.go` | REQ-AUD-001..009 | SPEC-SEC-005 |
| `internal/domain/cloud/estate.go` | REQ-SEC-001 | SPEC-SEC-001 |
| `internal/domain/cloud/resource.go` | REQ-DSC-002 | SPEC-DSC-001 |
| `internal/domain/core/assessment.go` (:17) | REQ-ONB-004 | SPEC-ONB-002 |
| `internal/domain/core/assessment.go` (:109) | REQ-OPT-006 | SPEC-OPT-004 |
| `internal/domain/core/assessment.go` (:258) | REQ-UTL-003 | SPEC-UTL-002 |
| `internal/domain/core/identity.go` | REQ-SEC-003 | SPEC-SEC-003 |
| `internal/domain/core/money.go` | — | SPEC-ARCH-002, SPEC-COST-001 |
| `internal/domain/core/principal.go` | REQ-SEC-005 | SPEC-SEC-002 |
| `internal/domain/cost/cost.go` | REQ-COST-001..008 | SPEC-COST-001 |
| `internal/domain/econ/footprint.go` | REQ-ECON-001..012 | SPEC-ECON-001 |
| `internal/domain/econ/slo.go` (:52) | REQ-SLO-001..006 | SPEC-ECON-003 |
| `internal/domain/econ/slo.go` (:154) | REQ-SLO-004 | SPEC-ECON-004 |
| `internal/domain/econ/slo.go` (:345) | REQ-ECON-010 | SPEC-ECON-005 |
| `internal/domain/execute/plan.go` | REQ-EXE-001..014, REQ-VAL-001..008 | SPEC-AUTO-001 |
| `internal/domain/execute/savings.go` (:21) | REQ-SAV-001..007 | SPEC-AUTO-005 |
| `internal/domain/execute/savings.go` (:274) | REQ-LRN-001..006 | SPEC-OPT-008 |
| `internal/domain/govern/policy.go` | REQ-GOV-001..011 | SPEC-GOV-001, SPEC-AI-004 |
| `internal/domain/optimize/recommendation.go` (:12) | REQ-OPT-001..014 | SPEC-OPT-001 |
| `internal/domain/optimize/recommendation.go` (:356) | REQ-OPT-008 | SPEC-OPT-005 |
| `internal/domain/optimize/recommendation.go` (:403) | REQ-OPT-012 | SPEC-OPT-006 |
| `internal/domain/optimize/conflict.go` | REQ-OPT-015 | SPEC-OPT-009 |
| `internal/domain/optimize/action_contract.go` | REQ-OPT-016 | SPEC-OPT-010 |
| `internal/domain/simulate/simulate.go` | REQ-SIM-001..010, REQ-CC-001..008 | SPEC-SIM-001 |
| `internal/domain/spec/diff.go` | REQ-SPEC-011 | SPEC-ONB-005 |
| `internal/domain/spec/spec.go` | REQ-SPEC-001..015 | SPEC-ONB-001 |
| `internal/domain/spec/validate.go` | REQ-SPEC-008 | SPEC-ONB-004 |
| `internal/domain/tenancy/tenant.go` | REQ-TEN-001..008 | SPEC-SEC-003 |
| `internal/infrastructure/auth/doc.go` | REQ-SEC-002, REQ-SEC-003 | SPEC-SEC-004 |
| `internal/infrastructure/config/config.go` | REQ-OPS-002 | SPEC-OPS-001 |
| `internal/infrastructure/resilience/doc.go` | REQ-API-041 | SPEC-API-014 |
| `internal/infrastructure/server/doc.go` | REQ-OPS-001 | SPEC-OPS-003 |
| `internal/infrastructure/telemetry/doc.go` | REQ-OPS-004 | SPEC-OPS-002 |
| `internal/ports/ai.go` (:46) | REQ-AI-006 | SPEC-AI-002 |
| `internal/ports/ai.go` (:245) | REQ-AI-008 | SPEC-AI-003 |
| `internal/ports/repositories.go` | — | SPEC-ARCH-003, SPEC-SEC-003 |
| `internal/ports/services.go` | REQ-SEC-001 | SPEC-SEC-001 |
| `internal/ports/usecases.go` | — | SPEC-ARCH-003, SPEC-API-001 |
| `internal/transport/http/doc.go` | REQ-API-001..050 | SPEC-API-001..020, SPEC-SEC-004, SPEC-SEC-005 |
| `rules/rules.go` | REQ-OPT-002 | SPEC-OPT-002 |

125 markers total across 108 distinct files (some files carry more than one, e.g. `internal/domain/econ/slo.go` and `internal/domain/optimize/recommendation.go` each have three).

**Note on REQ-API-041 in `internal/infrastructure/resilience/doc.go`.** This marker cites `REQ-API-041 (resilient outbound calls)` for a package that provides retry/circuit-breaker/rate-limiter *primitives*, not an HTTP endpoint. `docs/requirements.md` defines `REQ-API-041` as "Regression suite management" (an HTTP operation), following the route-table grouping used to allocate all fifty `REQ-API-*` numbers evenly across the 111 operations. The resilience package's use of the same ID is almost certainly a copy/paste of the wrong number from the same `REQ-API-001..050` block by whoever wrote that doc comment, rather than a deliberate second meaning — `resilience`'s primitives are used by `internal/adapters/aws/*` and `internal/adapters/llm/*`, not by anything in the HTTP route table. This is flagged rather than silently resolved: the two uses of `REQ-API-041` in this repository refer to different things, and this documentation set did not have authority to renumber either one.

## Table B — `SPEC-*` prefix → owning specification document

| SPEC prefix | Covers | Document |
|---|---|---|
| SPEC-AI-001..005 | Provider abstraction, middleware chain, copilot grounding, governance/AI boundary, RAG retrieval | [`docs/ai-spec.md`](ai-spec.md) |
| SPEC-API-001..020 | HTTP contract: RBAC route table, pagination, idempotency, error format, streaming | [`docs/architecture.md`](architecture.md) |
| SPEC-ARCH-001..005 | System context, domain layering, hexagonal ports, event-driven integration, notification dispatch | [`docs/architecture.md`](architecture.md) |
| SPEC-AUTO-001..006 | Four-phase execution discipline, rollback construction, idempotent retry, autonomous loop caps, savings lifecycle | [`docs/automation-spec.md`](automation-spec.md) |
| SPEC-COST-001..004 | Monetary precision, CUR/CE ingestion, pricing catalog, anomaly detection | [`docs/cost-engine-spec.md`](cost-engine-spec.md) |
| SPEC-DEMO-001 | Demo tenant / simulator fidelity | [`docs/testing-spec.md`](testing-spec.md) |
| SPEC-DSC-001..003 | Resource model, discovery orchestration, attribution | [`docs/aws-discovery-spec.md`](aws-discovery-spec.md) |
| SPEC-ECON-001..005 | Footprint model, unit economics, cost SLOs, error budget, efficiency score | [`docs/architecture-economics-spec.md`](architecture-economics-spec.md) |
| SPEC-GOV-001 | Policy-as-code evaluation engine | [`docs/automation-spec.md`](automation-spec.md) |
| SPEC-ONB-001..006 | Specification artefact, provenance, stage machine, deterministic validation, diffing, resumability | [`docs/onboarding-spec.md`](onboarding-spec.md) |
| SPEC-OPS-001..003 | Configuration layering, observability wiring, graceful lifecycle | [`docs/observability-spec.md`](observability-spec.md) |
| SPEC-OPT-001..008 | Rule/finding/recommendation model, rule pack economics, confidence, blast radius, risk/registry, learning | [`docs/optimization-spec.md`](optimization-spec.md) |
| SPEC-SEC-001..005 | AssumeRole access model, RBAC, tenant isolation, auth/AI sanitization boundary, audit | [`docs/security-spec.md`](security-spec.md) |
| SPEC-SIM-001 | Mutation engine, counterfactual engine, cost compiler | [`docs/optimization-spec.md`](optimization-spec.md) (§ Simulation and the Cost Compiler) |
| SPEC-TWIN-001..003 | Graph/view model, cost-flow conservation, dependents traversal | [`docs/architecture-economics-spec.md`](architecture-economics-spec.md) (§ Architecture Digital Twin) |
| SPEC-UTL-001..002 | Percentile statistics, trend/seasonality | [`docs/optimization-spec.md`](optimization-spec.md) (§ Utilization statistics) |

## Table C — API operation → `REQ-API-*`

All 111 operations from `internal/transport/http/routes.go` (8 public onboarding + 103 authenticated), grouped as in `docs/requirements.md`'s API section.

| Operation | Method + path | Permission | REQ ID |
|---|---|---|---|
| onboarding.start | POST /onboarding | *(public)* | REQ-API-001 |
| onboarding.send | POST /onboarding/{id}/messages | *(public)* | REQ-API-001 |
| onboarding.send_stream | POST /onboarding/{id}/messages/stream | *(public)* | REQ-API-001 |
| onboarding.state | GET /onboarding/{id} | *(public)* | REQ-API-001 |
| onboarding.summarize | GET /onboarding/{id}/summary | *(public)* | REQ-API-002 |
| onboarding.apply_edit | PATCH /onboarding/{id} | *(public)* | REQ-API-002 |
| onboarding.approve | POST /onboarding/{id}/approve | *(public)* | REQ-API-002 |
| onboarding.cancel | POST /onboarding/{id}/cancel | *(public)* | REQ-API-002 |
| specs.get_active, specs.diff, specs.list_versions | GET /specs/active, /specs/diff, /specs | spec:read | REQ-API-003 |
| specs.propose_revision, specs.import_yaml | POST /specs/revisions, /specs/import | spec:write | REQ-API-004 |
| specs.get, specs.export_yaml | GET /specs/{id}, /specs/{id}/export | spec:read | REQ-API-005 |
| specs.approve, specs.reject | POST /specs/{id}/approve, /reject | spec:approve | REQ-API-006 |
| tenants.get, tenants.update | GET/PATCH /tenant | tenant:administer | REQ-API-007 |
| users.list, users.invite | GET/POST /tenant/users | tenant:administer | REQ-API-008 |
| users.update_roles, users.remove | PATCH/DELETE /tenant/users/{id} | tenant:administer | REQ-API-009 |
| aws_accounts.register, aws_accounts.list | POST/GET /aws-accounts | aws:connect, resource:read | REQ-API-010 |
| aws_accounts.get, aws_accounts.verify | GET /aws-accounts/{id}, POST .../verify | resource:read, aws:connect | REQ-API-011 |
| aws_accounts.suspend, aws_accounts.remove | POST .../suspend, DELETE /aws-accounts/{id} | aws:connect | REQ-API-012 |
| aws_accounts.instructions | GET .../instructions | aws:connect | REQ-API-013 |
| discovery.run, discovery.list_runs | POST/GET /discovery/runs | discovery:run, resource:read | REQ-API-014 |
| discovery.status, discovery.get_stream | GET /discovery/status, .../stream | resource:read | REQ-API-015 |
| discovery.get | GET /discovery/runs/{id} | resource:read | REQ-API-016 |
| architecture.graph, architecture.cost_flow | GET /architecture/graph, /cost-flow | resource:read, cost:read | REQ-API-017 |
| architecture.rebuild | POST /architecture/rebuild | discovery:run | REQ-API-018 |
| architecture.node, architecture.dependents | GET /architecture/nodes/{id}(/dependents) | resource:read | REQ-API-019 |
| resources.list, resources.get | GET /resources(/{id}) | resource:read | REQ-API-020 |
| costs.ingest | POST /costs/ingest | cost:read | REQ-API-021 |
| costs.summary, costs.series | GET /costs/summary, /series | cost:read | REQ-API-022 |
| costs.breakdown, costs.forecast | GET /costs/breakdown, /forecast | cost:read | REQ-API-023 |
| costs.explain | GET /costs/explain | cost:read | REQ-API-024 |
| costs.detect_anomalies, costs.list_anomalies | POST/GET /costs/anomalies(/detect) | cost:read | REQ-API-025 |
| economics.compute | POST /economics/compute | economics:read | REQ-API-026 |
| economics.list_footprints, economics.footprint | GET /economics/footprints(/{id}) | economics:read | REQ-API-027 |
| economics.list_transactions, .unit_economics(_history) | GET /economics/transactions/... | economics:read | REQ-API-028 |
| economics.efficiency_score, .executive_summary | GET /economics/efficiency-score, /executive-summary | economics:read | REQ-API-029 |
| cost_slos.upsert, cost_slos.delete | POST /cost-slos, DELETE /cost-slos/{id} | slo:write | REQ-API-030 |
| cost_slos.list, .evaluate, .budget_states | GET/POST /cost-slos, /evaluate, /budget-states | economics:read | REQ-API-031 |
| recommendations.analyze | POST /recommendations/analyze | recommendation:run | REQ-API-032 |
| recommendations.list, .summary, .list_rules | GET /recommendations, /summary, /rules | recommendation:read | REQ-API-033 |
| recommendations.get, .explain | GET /recommendations/{id}(/explain) | recommendation:read | REQ-API-034 |
| recommendations.dismiss, .snooze | POST /recommendations/{id}/dismiss, /snooze | recommendation:run | REQ-API-035 |
| recommendations.plan_execution, .policy_decision | POST .../execution-plan, GET .../policy-decision | execution:start, policy:read | REQ-API-036 |
| simulations.mutate, .counterfactual | POST /simulations/mutate, /counterfactual | simulation:run | REQ-API-037 |
| simulations.list, .get | GET /simulations(/{id}) | simulation:run | REQ-API-038 |
| compiler.compile | POST /compiler/compile | compiler:run | REQ-API-039 |
| compiler.get_compilation, regression.run | GET /compiler/compilations/{id}, POST .../regression | compiler:run | REQ-API-040 |
| regression.list_suites, .upsert_suite | GET/POST /regression/suites | compiler:run | REQ-API-041 |
| policies.get_active, .list_versions | GET /policies/active, /versions | policy:read | REQ-API-042 |
| policies.save, .validate, .simulate, .activate | PUT /policies, POST /validate, /simulate, /{id}/activate | policy:write, policy:read | REQ-API-043 |
| approvals.list, .request, .get | GET/POST /approvals, GET /approvals/{id} | approval:read | REQ-API-044 |
| automation.process, .learn | POST /automation/process, /learn | automation:write | REQ-API-045 |
| executions.list, .get, .stream | GET /executions(/{id})(/stream) | execution:read | REQ-API-046 |
| executions.execute, .cancel | POST /executions/{id}/execute, /cancel | execution:start, execution:cancel | REQ-API-047 |
| executions.validate, .rollback | POST /executions/{id}/validate, /rollback | execution:read, rollback:start | REQ-API-048 |
| savings.funnel, audit.query, .verify, .timeline | GET /savings/funnel, /audit, /verify, /timeline | execution:read, audit:read | REQ-API-049 |
| copilot.ask(_stream), .list_conversations, .get_conversation, .suggestions | POST/GET /copilot/... | copilot:use | REQ-API-050 |
| auth.whoami | GET /auth/whoami | *(none — self-identification)* | *(not covered by REQ-API-001..050; a diagnostic endpoint)* |

## Table D — `REQ-*` prefix → owning package(s) and representative tests

| Prefix | Primary package(s) | Representative test file(s) |
|---|---|---|
| REQ-AI | `internal/adapters/llm/*`, `internal/ports/ai.go` | `llm/anthropic/provider_test.go`, `llm/bedrock/provider_test.go`, `llm/deterministic/provider_test.go`, `llm/fallback/provider_test.go`, `llm/middleware/*_test.go` (7 files) |
| REQ-API | `internal/transport/http` | `routing_test.go`, `rbac_test.go`, `auth_test.go`, `pagination_test.go`, `problem_test.go`, `sse_test.go`, `tenant_isolation_test.go` |
| REQ-AUD | `internal/domain/audit`, `internal/adapters/memstore` | `memstore/audit_test.go` — **no direct `internal/domain/audit` unit test file exists**; the hash-chain logic is exercised only indirectly through the memstore adapter test (see Flagged IDs) |
| REQ-AUTO | `internal/application/automation` | `autonomous_test.go`, `execute_test.go`, `funnel_test.go`, `learn_test.go`, `plan_test.go`, `rollback_test.go`, `validate_test.go` |
| REQ-CC | `internal/application/compiler` | `compiler_test.go`, `terraform_plan_test.go`, `terraform_hcl_test.go`, `cloudformation_test.go`, `kubernetes_test.go`, `regression_test.go`, `prcomment_test.go` |
| REQ-COP | `internal/application/copilot` | `service_test.go`, `registry_test.go`, `tools_test.go`, `grounding_test.go` |
| REQ-COST | `internal/application/costing`, `internal/adapters/aws/costing` | `costing/service_test.go`, `costing/anomaly_test.go`, `costing/forecast_test.go`, `costing/explain_test.go`, `aws/costing/costexplorer_test.go`, `aws/costing/cur_test.go` |
| REQ-DSC | `internal/application/discovery`, `internal/adapters/aws/discovery` | `discovery/service_test.go`; per-service adapter tests: `ec2_test.go`, `rds_test.go`, `s3_test.go`, `lambda_test.go`, `eks_test.go`, `ecs_test.go` and 11 more |
| REQ-ECON | `internal/application/economics` | `attribution_test.go`, `efficiency_test.go`, `slo_test.go`, `summary_test.go`, `unitecon_test.go` |
| REQ-EVT | `internal/adapters/events` | `inprocess_test.go`, `eventbridge_test.go`, `sqs_test.go` |
| REQ-EXE | `internal/domain/execute`, `internal/application/automation`, `internal/adapters/aws/executor` | `automation/execute_test.go`, `automation/plan_test.go`, `aws/executor/{ec2,eks,lambda,rds,s3}_test.go` |
| REQ-GOV | `internal/domain/govern`, `internal/application/governance` | `governance/evaluate_test.go`, `governance/approval_test.go`, `governance/maintenance_test.go` — **no direct `internal/domain/govern` unit test file**; `Evaluate`'s own correctness is exercised through the application-layer tests |
| REQ-LRN | `internal/application/learning` | `service_test.go` |
| REQ-NOT | `internal/adapters/notify` | `dispatcher_test.go`, `render_test.go`, `smtp_test.go`, `ses_test.go`, `slack_test.go`, `webhook_test.go` |
| REQ-ONB | `internal/application/onboarding` | `service_test.go`, `extraction_test.go` |
| REQ-OPS | `internal/infrastructure/{config,server,telemetry}` | `config/config_test.go`, `server/server_test.go`, `server/health_test.go`, `telemetry/logging_test.go`, `telemetry/metrics_test.go` |
| REQ-OPT | `internal/application/optimization`, `rules/` | `blast_test.go`, `confidence_test.go`, `priority_test.go`, `registry_init_test.go`, plus five rule-specific tests (`rule_ec2_rightsize_test.go`, `rule_ebs_gp2_gp3_test.go`, `rule_nat_vpc_endpoint_test.go`, `rule_k8s_pod_requests_oversized_test.go`, `rule_lambda_memory_cost_curve_test.go`) — **the other 41 rule files have no individual `_test.go` companion** (see Flagged IDs) |
| REQ-SAV | `internal/domain/execute` (savings.go) | `automation/funnel_test.go` — **no direct `internal/domain/execute` unit test file** |
| REQ-SEC | `internal/adapters/aws/sts`, `internal/infrastructure/auth`, `internal/domain/core` | `sts/broker_test.go`, `sts/verify_test.go`, `auth/jwt_test.go`, `auth/apikey_test.go`, `auth/devtoken_test.go`, `auth/service_token_test.go`, `auth/tenant_test.go` |
| REQ-SIM | `internal/application/simulation`, `internal/domain/simulate` | `simulation_test.go`, `fakes_test.go` |
| REQ-SLO | `internal/domain/econ` (slo.go), `internal/application/economics` | `economics/slo_test.go` |
| REQ-SPEC | `internal/domain/spec` | **no direct test file exists for `internal/domain/spec`** — validated only indirectly through `onboarding/service_test.go` and `onboarding/extraction_test.go` (see Flagged IDs) |
| REQ-TEN | `internal/domain/tenancy` | **no direct test file** — see Flagged IDs |
| REQ-TEST | `internal/adapters/memstore`, `internal/adapters/awssim`, `internal/adapters/llm/deterministic`, `internal/adapters/events` | `memstore/store_test.go`, `awssim/demo_test.go` |
| REQ-TWIN | `internal/application/twin` | `twin_test.go`, `costflow_test.go` |
| REQ-UTL | `internal/application/utilization`, `internal/adapters/aws/metrics` | `collect_test.go`, `stats_test.go`, `aws/metrics/cloudwatch_test.go` |
| REQ-VAL | `internal/domain/execute` (plan.go), `internal/application/automation` | `automation/validate_test.go` |

## Flagged IDs

Every ID in this list is either (a) introduced by this documentation set with no source `Traceability:` marker citing it directly, or (b) a genuine discrepancy found between a source comment and the behaviour of the code it describes. Both kinds are listed together because both represent a place where a reader should trust the code over a document — including this one — rather than assume documentation and implementation are in sync by default.

| ID / claim | Kind | Detail |
|---|---|---|
| `REQ-SEC-004` | Introduced | `SPEC-SEC-004` is cited by three files (`middleware/doc.go`, `auth/doc.go`, `transport/http/doc.go`) but no source marker anywhere cites a `REQ-SEC-004`. This document introduces it ("AI input/output sanitization") to give that SPEC a requirement to trace to. |
| `REQ-OPS-003` | Introduced | `SPEC-OPS-003` is cited (`server/doc.go`, paired there with `REQ-OPS-001`) but no source marker cites `REQ-OPS-003` directly. Introduced as "secret-shaped config fields reject literal values," matching `config.go`'s own documented behaviour, to fill the numbering gap between `REQ-OPS-002` and `REQ-OPS-004`. |
| `REQ-TEST-001` | Introduced | Only `REQ-TEST-002` is cited in source (`memstore/doc.go`). `REQ-TEST-001` is introduced as the natural pairing requirement ("deterministic reproducibility across the AI-dependent surface") — no source marker cites it. |
| `SPEC-ARCH-001` | Introduced | `SPEC-ARCH-002` through `005` are all cited in source; `001` is not. Introduced in `docs/architecture.md` as the system-context overview section, to keep the SPEC-ARCH numbering contiguous. |
| `SPEC-UTL-001` | Introduced | Only `SPEC-UTL-002` is cited in source (three separate files, consistently). `SPEC-UTL-001` is introduced in `docs/optimization-spec.md` as the percentile-statistics section (`core.SummarizeSamples`), which has no source marker of its own but is the natural `001` counterpart to `UTL-002`'s trend/seasonality content. |
| `REQ-API-041` used for two different things | Discrepancy | See the note under Table A: `internal/infrastructure/resilience/doc.go` cites `REQ-API-041` for retry/circuit-breaker primitives, while the HTTP route-table allocation in this document assigns `REQ-API-041` to `regression.list_suites`/`regression.upsert_suite`. Almost certainly an off-by-reference in the resilience package's own comment (copied from the shared `REQ-API-001..050` block without picking a number actually describing an HTTP operation) — resilience is used by outbound AWS/LLM calls, not the route table. Not resolved by renumbering either source; flagged instead. |
| `policies/README.md`'s "Known limitation: `auto_execute` cannot currently be selected" | Discrepancy (stale documentation, not a traceability marker) | Verified false against the current code. `internal/domain/govern/policy.go`'s `Evaluate` already tracks the most-restrictive *matching* rule's effect independently of the seeded default (see the comment at policy.go:333 beginning "Deny-bias operates *among the matching rules*"). A direct test against the shipped `balanced.yaml`, constructed to satisfy every guard on `balanced.waste.non-production.auto`, resolves to `Effect: auto_execute`. See [The AI safety model](../README.md#the-ai-safety-model) in the root README for the full write-up. Listed here because it is exactly the class of drift this traceability effort exists to catch, even though it is not a `REQ-*`/`SPEC-*` marker mismatch. |
| Domain packages with no direct unit-test file | Coverage gap, not an ID mismatch | `internal/domain/{audit,govern,execute,spec,tenancy,optimize,cost,econ,simulate,cloud}` have **no `_test.go` file of their own** (confirmed: `find internal/domain -name '*_test.go'` returns only `internal/domain/core/money_yaml_test.go`). Every one of these packages is exercised — sometimes thoroughly — through the application-layer and adapter test suites listed in Table D, but that means a defect isolated to pure domain logic (e.g. `govern.Evaluate`'s fold, `audit.ComputeHash`, `spec.Diff`) is only caught if an application-layer test happens to exercise that exact path. This is called out in [`docs/testing-spec.md`](testing-spec.md) as a specific, addressable gap. |
| `REQ-DSC-001..010` vs. `REQ-DSC-001..014` | Not a discrepancy, noted for completeness | `internal/adapters/aws/discovery/common.go` cites the narrower range; `internal/application/discovery/doc.go` cites the fuller one. Read as a subset relationship (the AWS adapter package implements discovery mechanics 001–010; the application-layer orchestrator additionally owns 011–014, e.g. multi-account/multi-region scope and run history) rather than a conflict. |
| `REQ-SIM-001..009` vs. `REQ-SIM-001..010` | Not a discrepancy, noted for completeness | Same pattern: `internal/adapters/awssim/doc.go` (the simulator) implements the narrower range; `internal/application/simulation/doc.go` and `internal/domain/simulate/simulate.go` (the mutation/counterfactual/compiler engines) additionally own `REQ-SIM-010` (simulation listing/retrieval, an application-layer concern the simulator itself has no stake in). |
