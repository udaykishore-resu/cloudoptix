/**
 * Precise domain types drawn directly from the Go structs in
 * internal/domain/**\/*.go and internal/ports/usecases.go.
 *
 * Why this file exists alongside api.ts: several nested schemas in
 * api/openapi.yaml are declared as opaque `{[key:string]: unknown}` objects
 * (Finding, RiskAssessment, BlastRadius, ConfidenceInput, StateSnapshot,
 * Step, RollbackPlan, ValidationPlan, PlanValidationResult, Driver, Weights,
 * Match, Candidate, StateProjection, PricedChange, RegressionCheck,
 * RuleCalibration, Turn) even though the Go types behind them are fully
 * structured — and TwinEdge's documented `source`/`target` fields don't
 * match the `from`/`to` json tags the Go struct actually serialises. Rather
 * than lose type safety on those (large) parts of the UI, this file types
 * them from the authoritative Go source per the task's own instruction to
 * prefer internal/ports/usecases.go for exact field names and shapes. See
 * the frontend README's "API contract notes" section for the full list —
 * report it upstream so openapi.yaml can be tightened to match.
 */
import type { Confidence, Criticality, Money, Period, Percentiles, Provenance, RiskLevel, Severity, Tags } from "./api";

// ---------------------------------------------------------------------------
// cloud (resource model)
// ---------------------------------------------------------------------------
export type ResourceKind = string; // "aws.ec2.instance", "aws.rds.instance", ... (open set, see mock/kinds.ts)
export type ResourceCategory =
  | "compute"
  | "database"
  | "storage"
  | "network"
  | "messaging"
  | "observability"
  | "security"
  | "other";
export type ResourceState =
  | "running"
  | "stopped"
  | "available"
  | "in-use"
  | "idle"
  | "terminated"
  | "pending"
  | "modifying"
  | "failed"
  | "unknown";
export type PurchaseModel = "on_demand" | "spot" | "reserved" | "savings_plan" | "serverless" | "unknown";

export interface Capacity {
  vcpu?: number;
  memory_gib?: number;
  storage_gib?: number;
  provisioned_iops?: number;
  throughput_mibps?: number;
  network_gbps?: number;
  instance_count?: number;
  desired_count?: number;
  min_count?: number;
  max_count?: number;
  read_replicas?: number;
  shard_count?: number;
  memory_mb?: number;
  timeout_s?: number;
  concurrency?: number;
  object_count?: number;
  retention_days?: number;
  write_capacity?: number;
  read_capacity?: number;
}

export interface Resource {
  id: string;
  tenant_id: string;
  account_id: string;
  region: string;
  availability_zone?: string;
  kind: ResourceKind;
  arn?: string;
  native_id: string;
  name?: string;
  state: ResourceState;
  instance_type?: string;
  engine?: string;
  engine_version?: string;
  capacity: Capacity;
  purchase_model: PurchaseModel;
  tags?: Tags;
  environment: string;
  environment_source: Provenance;
  application_id?: string;
  application?: string;
  workload_id?: string;
  workload?: string;
  owner?: string;
  cost_center?: string;
  criticality: Criticality;
  attributes?: Record<string, string>;
  created_at?: string;
  first_seen_at: string;
  last_seen_at: string;
  discovered_by: string;
  deleted: boolean;
  monthly_cost: Money;
  cost_source: Provenance;
  // Enriched fields the resource explorer needs and the twin computes.
  cpu?: Percentiles;
  memory?: Percentiles;
  finding_count?: number;
  potential_saving?: Money;
  dependencies?: string[];
  dependents?: string[];
}

// ---------------------------------------------------------------------------
// twin (architecture digital twin)
// ---------------------------------------------------------------------------
export type TwinView = "architecture" | "cost" | "performance" | "reliability" | "security" | "economics";

export interface TwinNode {
  id: string;
  label: string;
  kind: ResourceKind;
  category: ResourceCategory;
  service: string;
  account_id: string;
  region: string;
  availability_zone?: string;
  environment: string;
  state: ResourceState;
  monthly_cost: Money;
  economic_footprint: Money;
  cost_share: number;
  cpu?: Percentiles;
  memory?: Percentiles;
  latency_p99_ms?: number;
  error_rate?: number;
  availability?: number;
  risk: RiskLevel;
  criticality: Criticality;
  owner?: string;
  application?: string;
  workload?: string;
  tags?: Tags;
  finding_count: number;
  potential_saving: Money;
  group?: boolean;
  group_count?: number;
}

export type RelationKind =
  | "depends_on"
  | "routes_to"
  | "reads_from"
  | "writes_to"
  | "invokes"
  | "member_of"
  | "attached_to"
  | "peers_with";

export interface TwinEdge {
  from: string;
  to: string;
  kind: RelationKind | string;
  label?: string;
  weight: number;
  confidence: Confidence;
  cost_flow?: Money;
}

export interface TwinStats {
  node_count: number;
  edge_count: number;
  total_cost: Money;
  environments: number;
  accounts: number;
  regions: number;
  applications: number;
  orphan_count: number;
  completeness: number;
  built_at: string;
}

export interface TwinGraph {
  nodes: TwinNode[];
  edges: TwinEdge[];
  stats: TwinStats;
  view: TwinView;
  legend?: Record<string, string>;
  truncated: boolean;
}

export interface CostFlowNode {
  id: string;
  label: string;
  kind: string;
  amount: Money;
  share: number;
}
export interface CostFlowLevel {
  depth: number;
  nodes: CostFlowNode[];
}
export interface CostFlowLink {
  from: string;
  to: string;
  amount: Money;
  basis: string;
}
export interface CostFlowGraph {
  levels: CostFlowLevel[];
  links: CostFlowLink[];
  total: Money;
  unattributed: Money;
  period: Period;
}

// ---------------------------------------------------------------------------
// optimize (findings + recommendations)
// ---------------------------------------------------------------------------
export type OptimizeCategory =
  | "rightsizing"
  | "waste"
  | "storage"
  | "commitment"
  | "network"
  | "architecture"
  | "scheduling"
  | "licensing"
  | "data_lifecycle"
  | "observability_cost";

export type ActionType =
  | "resize_instance"
  | "stop_instance"
  | "terminate_instance"
  | "delete_volume"
  | "resize_volume"
  | "modify_volume_type"
  | "delete_snapshot"
  | "deregister_ami"
  | "release_elastic_ip"
  | "resize_rds_instance"
  | "modify_rds_storage"
  | "remove_rds_replica"
  | "stop_rds_instance"
  | "apply_s3_lifecycle"
  | "abort_multipart_uploads"
  | "set_log_retention"
  | "resize_lambda_memory"
  | "remove_provisioned_concurrency"
  | "switch_lambda_architecture"
  | "resize_node_group"
  | "adjust_pod_resources"
  | "enable_spot_capacity"
  | "create_vpc_endpoint"
  | "remove_nat_gateway"
  | "schedule_shutdown"
  | "purchase_commitment"
  | "switch_dynamodb_billing_mode"
  | "advisory_only";

export type Reversibility = "instant" | "fast" | "slow" | "none";
export type Complexity = "trivial" | "low" | "medium" | "high" | "project";
export type RecommendationStatus =
  | "open"
  | "under_review"
  | "approved"
  | "rejected"
  | "scheduled"
  | "executing"
  | "executed"
  | "validating"
  | "validated"
  | "failed"
  | "rolled_back"
  | "superseded"
  | "snoozed"
  | "dismissed";

export interface Evidence {
  kind: "metric" | "config" | "cost" | "topology" | "history";
  label: string;
  value: string;
  window?: Period;
  source: "cloudwatch" | "discovery" | "cost_explorer" | string;
  percentiles?: Percentiles;
}

export interface Finding {
  id: string;
  tenant_id: string;
  rule_id: string;
  rule_name: string;
  category: OptimizeCategory;
  resource_id: string;
  resource_name: string;
  resource_kind: ResourceKind;
  account_id: string;
  region: string;
  environment: string;
  severity: Severity;
  summary: string;
  detail?: string;
  evidence: Evidence[];
  current_monthly_cost: Money;
  estimated_monthly_saving: Money;
  detected_at: string;
}

export interface StateSnapshot {
  instance_type?: string;
  volume_type?: string;
  size_gib?: number;
  iops?: number;
  memory_mb?: number;
  count?: number;
  vcpu?: number;
  memory_gib?: number;
  monthly_cost: Money;
  attributes?: Record<string, string>;
}

export interface ConfidenceInput {
  name: string;
  value: number;
  weight: number;
  explanation: string;
}

export interface RiskFactor {
  name: string;
  contribution: number;
  explanation: string;
}

export interface RiskAssessment {
  score: number;
  level: RiskLevel;
  availability_risk: RiskLevel;
  performance_risk: RiskLevel;
  security_risk: RiskLevel;
  data_loss_risk: RiskLevel;
  factors: RiskFactor[];
  mitigations?: string[];
}

export interface BlastRadius {
  resources_affected: number;
  services_affected: number;
  critical_services: number;
  workloads_affected?: string[];
  apis_affected: number;
  transactions_affected?: string[];
  estimated_users: number;
  monthly_revenue_at_risk?: Money;
  environments_affected?: string[];
  cross_account: boolean;
  score: number;
  level: RiskLevel;
  completeness: number;
  explanation: string;
}

export interface Recommendation {
  id: string;
  tenant_id: string;
  finding: Finding;
  title: string;
  rationale: string;
  action: ActionType;
  parameters: Record<string, unknown>;
  current_state: StateSnapshot;
  proposed_state: StateSnapshot;
  estimated_monthly_saving: Money;
  estimated_annual_saving: Money;
  implementation_cost?: Money;
  payback_days?: number;
  confidence: Confidence;
  confidence_basis?: ConfidenceInput[];
  risk: RiskAssessment;
  blast_radius: BlastRadius;
  reversibility: Reversibility;
  complexity: Complexity;
  priority_score: number;
  rank?: number;
  status: RecommendationStatus;
  status_reason?: string;
  snoozed_until?: string;
  requires_approval: boolean;
  policy_decision_id?: string;
  auto_executable: boolean;
  narrative?: string;
  maintenance_window?: string;
  supersedes_id?: string;
  created_at: string;
  updated_at: string;
}

export interface RuleInfo {
  id: string;
  name: string;
  category: OptimizeCategory;
  action: ActionType;
  description: string;
  kinds: ResourceKind[];
  enabled: boolean;
  thresholds?: Record<string, unknown>;
  calibration?: RuleCalibration;
}

export interface RecommendationSummary {
  open: number;
  total_monthly_saving: Money;
  by_category: Record<string, number>;
  saving_by_category: Record<string, Money>;
  by_risk: Record<string, number>;
  auto_executable: number;
  awaiting_approval: number;
}

export interface RecommendationExplanation {
  recommendation: Recommendation;
  evidence: Evidence[];
  confidence_inputs: ConfidenceInput[];
  risk_factors: RiskFactor[];
  blast_radius: BlastRadius;
  affected_nodes?: TwinNode[];
  policy_decision?: PolicyDecision;
  calibration?: RuleCalibration;
  rollback_summary?: string;
  narrative?: string;
  similar_outcomes?: Outcome[];
}

// ---------------------------------------------------------------------------
// execute (plans, savings, learning)
// ---------------------------------------------------------------------------
export type StepKind = "precondition" | "snapshot" | "mutate" | "wait" | "verify";
export type StepState = "pending" | "running" | "succeeded" | "failed" | "skipped" | "rolled_back";

export interface Step {
  id: string;
  ordinal: number;
  kind: StepKind;
  name: string;
  describe: string;
  aws_action?: string;
  target?: string;
  parameters?: Record<string, unknown>;
  idempotency_key: string;
  state: StepState;
  attempts: number;
  max_retries: number;
  started_at?: string;
  finished_at?: string;
  error?: string;
  output?: Record<string, unknown>;
  abort_on_failure: boolean;
}

export interface RollbackPlan {
  id: string;
  tenant_id: string;
  plan_id: string;
  steps: Step[];
  feasible: boolean;
  infeasible_reason?: string;
  estimated_duration: number; // nanoseconds, per Go time.Duration JSON encoding
  data_loss_risk: RiskLevel;
  summary: string;
  created_at: string;
}

export interface ValidationCheckDef {
  name: string;
  metric: string;
  statistic: string;
  comparison: string;
  threshold: number;
  critical: boolean;
  reason: string;
}

export interface ValidationPlan {
  observation_window: number;
  baseline_window: number;
  checks: ValidationCheckDef[];
  auto_rollback_on?: string[];
  min_samples: number;
}

export type PlanState =
  | "draft"
  | "awaiting_approval"
  | "approved"
  | "scheduled"
  | "preflight"
  | "executing"
  | "executed"
  | "validating"
  | "validated"
  | "failed"
  | "rolling_back"
  | "rolled_back"
  | "rollback_failed"
  | "cancelled";

export interface ExecutionPlan {
  id: string;
  tenant_id: string;
  recommendation_id: string;
  action: ActionType;
  title: string;
  account_id: string;
  region: string;
  environment: string;
  resource_ids: string[];
  steps: Step[];
  snapshots?: unknown[];
  rollback?: RollbackPlan;
  validation: ValidationPlan;
  expected_monthly_saving: Money;
  baseline_monthly_cost: Money;
  state: PlanState;
  state_reason?: string;
  approval_id?: string;
  policy_decision_id?: string;
  scheduled_for?: string;
  dry_run: boolean;
  requested_by: string;
  created_at: string;
  started_at?: string;
  finished_at?: string;
}

export type Verdict = "success" | "partial_success" | "failure" | "inconclusive";

export interface CheckOutcome {
  name: string;
  metric: string;
  baseline: number;
  observed: number;
  threshold: number;
  change_pct: number;
  passed: boolean;
  critical: boolean;
  samples: number;
  detail: string;
}

export interface PlanValidationResult {
  id: string;
  tenant_id: string;
  plan_id: string;
  verdict: Verdict;
  explanation: string;
  baseline_window: Period;
  observed_window: Period;
  checks: CheckOutcome[];
  predicted_monthly_saving: Money;
  observed_monthly_saving: Money;
  saving_accuracy: number;
  rollback_triggered: boolean;
  rollback_reason?: string;
  evaluated_at: string;
}

export type SavingsStage = "potential" | "approved" | "planned" | "executed" | "validated" | "realized";

export interface LeakagePoint {
  from: SavingsStage;
  to: SavingsStage;
  amount: Money;
  count: number;
  conversion_rate: number;
  top_reasons?: string[];
}

export interface SavingsFunnel {
  tenant_id: string;
  period: Period;
  potential_monthly: Money;
  approved_monthly: Money;
  planned_monthly: Money;
  executed_monthly: Money;
  validated_monthly: Money;
  realized_monthly: Money;
  realized_annual: Money;
  counts: Record<SavingsStage, number>;
  leakage: LeakagePoint[];
  prediction_accuracy: number;
  computed_at: string;
}

export interface Outcome {
  id: string;
  tenant_id: string;
  rule_id: string;
  action: ActionType;
  resource_kind: string;
  environment: string;
  predicted_monthly_saving: Money;
  actual_monthly_saving: Money;
  predicted_confidence: Confidence;
  predicted_risk: RiskLevel;
  verdict: Verdict;
  rolled_back: boolean;
  performance_impact_pct: number;
  availability_impact_pct: number;
  saving_ratio: number;
  observed_at: string;
}

export interface RuleCalibration {
  rule_id: string;
  tenant_id: string;
  samples: number;
  success_rate: number;
  rollback_rate: number;
  mean_saving_ratio: number;
  median_saving_ratio: number;
  confidence_multiplier: number;
  saving_multiplier: number;
  updated_at: string;
}

export interface AutonomousRunResult {
  considered: number;
  planned: number;
  executed: number;
  skipped: number;
  failed: number;
  rolled_back: number;
  monthly_saving: Money;
  skip_reasons?: Record<string, number>;
  duration_ms: number;
}

export interface LearningResult {
  outcomes_considered: number;
  rules_calibrated: number;
  calibrations?: Record<string, RuleCalibration>;
  mean_accuracy: number;
}

// ---------------------------------------------------------------------------
// govern (policy + approvals)
// ---------------------------------------------------------------------------
export type PolicyEffect = "auto_execute" | "require_approval" | "prohibit" | "advisory_only";

export interface PolicyMatch {
  actions?: ActionType[];
  categories?: OptimizeCategory[];
  rule_ids?: string[];
  environments?: string[];
  account_ids?: string[];
  regions?: string[];
  resource_kinds?: string[];
  application_ids?: string[];
  tag_selector?: Record<string, string>;
  min_confidence?: number;
  max_risk_level?: RiskLevel;
  max_blast_score?: number;
  max_critical_services?: number;
  max_monthly_saving_impact?: Money;
  min_reversibility?: Reversibility;
  require_maintenance_window?: boolean;
}

export interface PolicyRule {
  id: string;
  description: string;
  match: PolicyMatch;
  effect: PolicyEffect;
  approvers?: string[];
  min_approvals?: number;
  require_distinct_approver?: boolean;
  maintenance_windows?: string[];
  reason?: string;
}

export interface Policy {
  id: string;
  tenant_id: string;
  name: string;
  description?: string;
  version: number;
  rules: PolicyRule[];
  default_effect: PolicyEffect;
  enabled: boolean;
  created_by: string;
  created_at: string;
  activated_at?: string;
  checksum: string;
}

export interface PolicyDecision {
  id: string;
  tenant_id: string;
  recommendation_id: string;
  policy_id: string;
  policy_version: number;
  policy_checksum: string;
  effect: PolicyEffect;
  matched_rules: string[];
  deciding_rule: string;
  reason: string;
  explanation: string[];
  requires_approval: boolean;
  approvers?: string[];
  min_approvals: number;
  require_distinct_approver: boolean;
  maintenance_windows?: string[];
  input_digest: string;
  decided_at: string;
}

export interface PolicySimulationChange {
  recommendation_id: string;
  title: string;
  from: PolicyEffect;
  to: PolicyEffect;
  monthly_saving: Money;
}

export interface PolicySimulation {
  policy_name: string;
  evaluated: number;
  auto_execute: number;
  require_approval: number;
  prohibited: number;
  advisory: number;
  changes?: PolicySimulationChange[];
  auto_executable_saving: Money;
  warnings?: string[];
}

export type ApprovalState = "pending" | "approved" | "rejected" | "expired" | "withdrawn" | "cancelled";
export type SubjectKind =
  | "recommendation"
  | "execution_plan"
  | "spec"
  | "policy"
  | "aws_connection"
  | "commitment_purchase";

export interface ApprovalContext {
  monthly_saving?: Money;
  annual_saving?: Money;
  monthly_cost_delta?: Money;
  confidence?: Confidence;
  risk_level?: RiskLevel;
  blast_summary?: string;
  environment?: string;
  affected_resources?: string[];
  rollback_plan?: string;
  validation_plan?: string;
  policy_reason?: string;
  diff?: string;
}

export interface ApprovalResponse {
  principal: string;
  role: string;
  approved: boolean;
  comment?: string;
  at: string;
  ip_address?: string;
  user_agent?: string;
}

export interface ApprovalRequest {
  id: string;
  tenant_id: string;
  subject_kind: SubjectKind;
  subject_id: string;
  title: string;
  summary: string;
  context: ApprovalContext;
  policy_decision_id?: string;
  required_roles?: string[];
  min_approvals: number;
  require_distinct_approver: boolean;
  state: ApprovalState;
  responses?: ApprovalResponse[];
  requested_by: string;
  requested_at: string;
  expires_at: string;
  decided_at?: string;
  execute_after?: string;
}

// ---------------------------------------------------------------------------
// simulate (mutation, counterfactual, compiler, regression)
// ---------------------------------------------------------------------------
export interface Assumption {
  key: string;
  label: string;
  value: string;
  unit?: string;
  provenance: Provenance;
  sensitivity?: number;
  note?: string;
}

export type Dimension =
  | "cost"
  | "performance"
  | "reliability"
  | "scalability"
  | "security"
  | "operational_complexity"
  | "migration_complexity"
  | "risk";

export interface DimensionScore {
  dimension: Dimension;
  score: number;
  delta: number;
  rationale: string;
  confidence: Confidence;
}

export interface ComponentChange {
  action: "replace" | "add" | "remove" | "resize" | "keep";
  from?: string;
  to?: string;
  component: string;
  monthly_delta: Money;
  rationale: string;
  effort?: string;
}

export interface Candidate {
  id: string;
  tenant_id: string;
  simulation_id: string;
  name: string;
  summary: string;
  pattern: string;
  changes: ComponentChange[];
  current_monthly_cost: Money;
  projected_monthly_cost: Money;
  monthly_delta: Money;
  annual_delta: Money;
  savings_pct: number;
  scores: DimensionScore[];
  composite_score: number;
  assumptions: Assumption[];
  risks?: string[];
  blockers?: string[];
  migration_steps?: string[];
  confidence: Confidence;
  recommended: boolean;
}

export type Weights = Partial<Record<Dimension, number>>;

export interface Simulation {
  id: string;
  tenant_id: string;
  name: string;
  scope: "application" | "workload" | "account";
  scope_id: string;
  kind: "architecture_mutation" | "counterfactual" | "cost_compiler";
  baseline_cost: Money;
  candidates: Candidate[];
  weights: Weights;
  assumptions: Assumption[];
  requested_by: string;
  created_at: string;
  completed_at?: string;
  status: string;
  error?: string;
}

export type ScenarioType =
  | "traffic_change"
  | "platform_change"
  | "database_change"
  | "add_cache"
  | "remove_nat"
  | "add_vpc_endpoint"
  | "spot_adoption"
  | "region_change"
  | "commitment_purchase"
  | "storage_class_change"
  | "replica_change"
  | "custom";

export interface Scenario {
  type: ScenarioType;
  label?: string;
  parameters?: Record<string, unknown>;
  scope_id?: string;
}

export interface ProjectedComponent {
  name: string;
  kind: string;
  quantity: number;
  unit: string;
  monthly_cost: Money;
}

export interface StateProjection {
  label: string;
  monthly_cost: Money;
  by_service?: Record<string, Money>;
  components?: ProjectedComponent[];
  p95_latency_ms?: number;
  availability?: number;
  notes?: string[];
}

export interface Counterfactual {
  id: string;
  tenant_id: string;
  scenario: Scenario;
  question: string;
  current_state: StateProjection;
  proposed_state: StateProjection;
  cost_delta: Money;
  cost_delta_pct: number;
  annual_cost_delta: Money;
  performance_delta: string;
  reliability_delta: string;
  security_delta?: string;
  risk: RiskLevel;
  confidence: Confidence;
  assumptions: Assumption[];
  caveats?: string[];
  narrative?: string;
  computed_at: string;
}

export type SourceKind =
  | "terraform_plan"
  | "terraform_hcl"
  | "cloudformation"
  | "kubernetes_manifest"
  | "helm_release"
  | "live_topology";

export type ChangeAction = "create" | "update" | "replace" | "delete" | "no-op";

export interface PriceComponent {
  name: string;
  unit: string;
  quantity: number;
  unit_price: Money;
  monthly: Money;
  price_basis: string;
}

export interface PricedChange {
  address: string;
  resource_type: string;
  kind: string;
  action: ChangeAction;
  region?: string;
  before_monthly: Money;
  after_monthly: Money;
  monthly_delta: Money;
  usage_dependent: boolean;
  assumptions?: Assumption[];
  unpriced: boolean;
  unpriced_reason?: string;
  price_components?: PriceComponent[];
  warnings?: string[];
}

export interface CostRisk {
  code: string;
  severity: Severity;
  address?: string;
  summary: string;
  detail?: string;
  monthly_impact?: Money;
  remediation?: string;
}

export interface Opportunity {
  address: string;
  summary: string;
  monthly_saving: Money;
  change: string;
}

export interface CompilationResult {
  id: string;
  tenant_id: string;
  source: SourceKind;
  label: string;
  changes: PricedChange[];
  baseline_monthly: Money;
  projected_monthly: Money;
  monthly_delta: Money;
  annual_delta: Money;
  delta_pct: number;
  created_count: number;
  updated_count: number;
  deleted_count: number;
  unpriced_count: number;
  coverage: number;
  confidence: Confidence;
  assumptions: Assumption[];
  risks?: CostRisk[];
  opportunities?: Opportunity[];
  pricing_date: string;
  compiled_at: string;
  duration_ms: number;
}

export type RegressionCheckKind =
  | "max_monthly_increase_pct"
  | "max_monthly_increase_abs"
  | "max_cost_per_transaction"
  | "forbidden_resource"
  | "require_tags"
  | "max_unpriced_ratio"
  | "budget_headroom";

export type RegressionVerdict = "PASS" | "WARNING" | "FAIL";

export interface RegressionCheck {
  name: string;
  kind: RegressionCheckKind;
  threshold?: number;
  amount?: Money;
  environments?: string[];
  resource_types?: string[];
  required_tags?: string[];
  transaction_name?: string;
  on_violation: RegressionVerdict;
  message?: string;
}

export interface RegressionSuite {
  id: string;
  tenant_id: string;
  name: string;
  version: number;
  checks: RegressionCheck[];
  enabled: boolean;
  created_at: string;
}

export interface CheckResult {
  name: string;
  kind: RegressionCheckKind;
  verdict: RegressionVerdict;
  expected: string;
  actual: string;
  message: string;
  offenders?: string[];
}

export interface RegressionReport {
  id: string;
  tenant_id: string;
  compilation_id: string;
  suite_name: string;
  verdict: RegressionVerdict;
  results: CheckResult[];
  monthly_delta: Money;
  annual_delta: Money;
  summary: string;
  required_action?: string;
  evaluated_at: string;
}

// ---------------------------------------------------------------------------
// Onboarding (Turn — thin in openapi)
// ---------------------------------------------------------------------------
// ---------------------------------------------------------------------------
// econ (error budgets) — openapi.yaml's EconomicErrorBudget schema omits the
// evaluated-state fields (state, burn projection, triggered actions,
// explanation) that econ/slo.go's EvaluateBudget actually populates and that
// the SLO and overview pages need to render burn-down status at all.
// ---------------------------------------------------------------------------
export type BudgetState = "healthy" | "watch" | "at_risk" | "exhausted" | "breached" | "unknown";

// BreachAction is a plain Go string enum (econ/slo.go) — openapi.yaml
// mistakenly declares it as an opaque object schema instead of a string.
export type BreachAction = "notify" | "require_approval" | "freeze_cost_increases" | "generate_recommendations" | "open_investigation" | string;

export interface CostSLO {
  id: string;
  tenant_id: string;
  name: string;
  description?: string;
  kind: string;
  direction: "at_most" | "at_least" | string;
  scope: string;
  scope_id?: string;
  transaction_id?: string;
  target?: Money;
  target_ratio?: number;
  window: "calendar_month" | "rolling_30d" | "rolling_7d";
  error_budget_pct: number;
  breach_actions: BreachAction[];
  owner?: string;
  enabled: boolean;
  created_at: string;
  updated_at: string;
}

export interface EconomicErrorBudget {
  id: string;
  tenant_id: string;
  slo_id: string;
  slo_name: string;
  kind: string;
  period: Period;
  target?: Money;
  budget_amount?: Money;
  actual?: Money;
  consumed?: Money;
  remaining?: Money;
  consumed_ratio: number;
  burn_rate: number;
  projected_end_of_window?: Money;
  projected_overage?: Money;
  exhaustion_date?: string;
  state: BudgetState;
  triggered_actions?: string[];
  explanation: string;
  evaluated_at: string;
}

// ---------------------------------------------------------------------------
// Driver / UnitEconomics — openapi.yaml declares Driver as a fully opaque
// {[key:string]: unknown} schema (referenced by both UnitEconomics.drivers
// and econ/footprint.go's DecomposeChange), so both are hand-typed here.
// ---------------------------------------------------------------------------
export interface Driver {
  kind: string;
  label: string;
  impact: Money;
  impact_share: number;
  explanation: string;
}

export interface UnitEconomics {
  id: string;
  tenant_id: string;
  transaction_id: string;
  name: string;
  period: Period;
  volume: number;
  total_cost: Money;
  cost_per_unit: Money;
  direct_per_unit?: Money;
  shared_per_unit?: Money;
  prior_cost_per_unit?: Money;
  change_pct: number;
  drivers: Driver[];
  confidence: Confidence;
  volume_provenance: Provenance;
  computed_at: string;
}

// ---------------------------------------------------------------------------
// EfficiencyScore / EfficiencyFactor — openapi.yaml declares EfficiencyFactor
// as a fully opaque {[key:string]: unknown} schema, so it is hand-typed here
// from econ/slo.go's EfficiencyFactor/EfficiencyScore structs.
// ---------------------------------------------------------------------------
export interface EfficiencyFactor {
  name: string;
  score: number;
  weight: number;
  detail: string;
  opportunity?: Money;
}

export interface EfficiencyScore {
  id: string;
  tenant_id: string;
  scope: string;
  scope_id?: string;
  label: string;
  period: Period;
  score: number;
  grade: string;
  factors: EfficiencyFactor[];
  prior_score?: number;
  delta?: number;
  waste_ratio: number;
  total_spend: Money;
  identified_waste: Money;
  computed_at: string;
}

// ---------------------------------------------------------------------------
// ExecutiveSummary — openapi.yaml's version nests the generated (thin)
// Recommendation/EconomicErrorBudget/SavingsFunnel schemas, all-optional;
// the overview page needs the richer domain versions of those and the
// guarantee (true in every real response) that the top-level fields exist.
// ---------------------------------------------------------------------------
export interface ExecutiveSummary {
  period: Period;
  monthly_spend: Money;
  forecast_month_end: Money;
  prior_month_spend: Money;
  spend_change_pct: number;
  potential_savings: Money;
  realized_savings: Money;
  realized_annualized: Money;
  waste_pct: number;
  efficiency_score: number;
  efficiency_grade: string;
  cost_slos_healthy: number;
  cost_slos_at_risk: number;
  cost_slos_breached: number;
  budget_states: EconomicErrorBudget[];
  top_opportunities: Recommendation[];
  top_transactions: UnitEconomics[];
  savings_funnel: SavingsFunnel;
  generated_at: string;
}

// ---------------------------------------------------------------------------
// AuditEntry — openapi.yaml marks every field optional; the audit trail
// itself (audit.go's Append) always populates id/sequence/hash/action/at/etc.
// ---------------------------------------------------------------------------
export interface AuditEntry {
  id: string;
  sequence: number;
  action: string;
  outcome: "success" | "failure";
  actor: string;
  machine: boolean;
  subject: string;
  subject_id?: string;
  message: string;
  before?: Record<string, unknown>;
  after?: Record<string, unknown>;
  metadata?: Record<string, unknown>;
  at: string;
  hash: string;
}

export interface Turn {
  id?: string;
  role: "user" | "assistant" | "system";
  text: string;
  at: string;
  suggestions?: string[];
}

// ---------------------------------------------------------------------------
// Resource explorer saved views (frontend-only concept, persisted locally)
// ---------------------------------------------------------------------------
export interface SavedView {
  id: string;
  name: string;
  filters: Record<string, unknown>;
  sort?: { id: string; desc: boolean }[];
  createdAt: string;
}
