# AI Cost Copilot examples

Six example questions against the `shopfleet-prod` demo estate, each showing
the full path described in [`docs/ai-spec.md`](../../docs/ai-spec.md)
(SPEC-AI-001..005): the read-only tools the copilot calls, the structured
results those tools return, and the final answer only after it has passed
`GroundingVerifier` — every resource id, account id, and dollar figure in
the answer text is checked against what the tools actually returned in that
conversation.

| File | Shows |
|---|---|
| [`questions-and-answers.md`](questions-and-answers.md) | Five grounded questions across cost, recommendations, economics, and architecture-graph tools |
| [`grounding-failure-and-recovery.md`](grounding-failure-and-recovery.md) | The one case that goes differently: a first-draft answer that fails grounding, is regenerated, and is finally returned with a caveat rather than presented as settled fact |

All six run against `internal/adapters/llm/deterministic` (SPEC-AI-001) —
the same scripted, seeded, no-API-key provider used throughout the
onboarding transcripts in [`examples/onboarding/`](../onboarding/). Every
dollar figure quoted below is a real number from the demo estate, the same
figures used in [`examples/optimization-scenarios/`](../optimization-scenarios/)
and the root README, not invented for this file.

## The tools available to the copilot

| Tool | Package | Reads |
|---|---|---|
| `get_cost_summary` / `get_cost_breakdown` | `tool_cost.go` | `internal/domain/cost` |
| `explain_cost_change` | `tool_cost.go` | cost anomaly/delta explanation |
| `get_economic_footprint` / `get_unit_economics` / `get_efficiency_score` / `get_cost_slo_status` | `tool_economics.go` | `internal/domain/econ` |
| `list_recommendations` / `get_recommendation` / `get_blast_radius` | `tool_recommendations.go` | `internal/domain/optimize` |
| `list_resources` / `get_resource` | `tool_resources.go` | `internal/domain/cloud` |
| `query_architecture_graph` | `tool_architecture.go` | the Architecture Digital Twin |
| `run_counterfactual` | `tool_counterfactual.go` | the Counterfactual Engine |
| `get_savings_funnel` | `tool_savings.go` | `internal/domain/execute` savings ladder |
| `search_knowledge` | `tool_knowledge.go` | `internal/adapters/rag`, hybrid search over indexed documents |

Every one of these is read-only — none of them, individually or in
combination, can execute a change, approve a recommendation, or write to
anything but the copilot's own conversation state. See
`docs/ai-spec.md`, SPEC-AI-001 and SPEC-AI-004, for why this is a structural
property of the tool registry, not a convention the copilot happens to
follow.
