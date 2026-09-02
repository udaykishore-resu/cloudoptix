// Package audit provides the tamper-evident record of everything
// consequential the platform does.
//
// The design choice worth explaining: records are hash-chained. Each record
// stores the hash of its predecessor for the same tenant, so removing or
// editing a record breaks the chain at every subsequent entry. This does not
// make the log immutable — nothing stored in a mutable database is — but it
// makes tampering detectable with a single verification pass, which is the
// property an auditor actually needs. Records are also written to object
// storage with a retention lock in production deployments, where true
// immutability is available.
//
// Traceability: REQ-AUD-001..009, SPEC-SEC-005.
package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// Action names an auditable operation. The set is closed so that a query for
// "every production execution last quarter" is exact rather than a text search.
type Action string

const (
	ActionTenantCreated   Action = "tenant.created"
	ActionTenantUpdated   Action = "tenant.updated"
	ActionUserInvited     Action = "user.invited"
	ActionUserRoleChanged Action = "user.role_changed"
	ActionLogin           Action = "auth.login"
	ActionLoginFailed     Action = "auth.login_failed"

	ActionSpecDrafted   Action = "spec.drafted"
	ActionSpecUpdated   Action = "spec.updated"
	ActionSpecValidated Action = "spec.validated"
	ActionSpecApproved  Action = "spec.approved"
	ActionSpecRejected  Action = "spec.rejected"

	ActionAWSAccountRegistered Action = "aws_account.registered"
	ActionAWSAccountVerified   Action = "aws_account.verified"
	ActionAWSAssumeRole        Action = "aws.assume_role"
	ActionAWSAPICall           Action = "aws.api_call"

	ActionDiscoveryStarted   Action = "discovery.started"
	ActionDiscoveryCompleted Action = "discovery.completed"
	ActionDiscoveryFailed    Action = "discovery.failed"

	ActionCostIngested    Action = "cost.ingested"
	ActionAnomalyDetected Action = "cost.anomaly_detected"
	ActionSLOBreached     Action = "slo.breached"
	ActionBudgetExhausted Action = "budget.exhausted"

	ActionRecommendationCreated   Action = "recommendation.created"
	ActionRecommendationDismissed Action = "recommendation.dismissed"
	ActionPolicyEvaluated         Action = "policy.evaluated"
	ActionPolicyCreated           Action = "policy.created"
	ActionPolicyActivated         Action = "policy.activated"

	ActionApprovalRequested Action = "approval.requested"
	ActionApprovalGranted   Action = "approval.granted"
	ActionApprovalRejected  Action = "approval.rejected"
	ActionApprovalExpired   Action = "approval.expired"

	ActionPlanCreated        Action = "execution.plan_created"
	ActionExecutionStarted   Action = "execution.started"
	ActionExecutionStep      Action = "execution.step"
	ActionExecutionSucceeded Action = "execution.succeeded"
	ActionExecutionFailed    Action = "execution.failed"
	ActionValidationStarted  Action = "validation.started"
	ActionValidationResult   Action = "validation.result"
	ActionRollbackStarted    Action = "rollback.started"
	ActionRollbackCompleted  Action = "rollback.completed"
	ActionRollbackFailed     Action = "rollback.failed"

	ActionSimulationRun Action = "simulation.run"
	ActionCostCompiled  Action = "cost_compiler.run"
	ActionRegressionRun Action = "cost_regression.run"

	ActionCopilotQuery      Action = "ai.copilot_query"
	ActionLLMCall           Action = "ai.llm_call"
	ActionGroundingRejected Action = "ai.grounding_rejected"

	ActionCrossTenantAccess   Action = "security.cross_tenant_access"
	ActionAuthorizationDenied Action = "security.authorization_denied"
	ActionDataExported        Action = "data.exported"
)

// Sensitive reports whether an action must be retained for the full
// compliance period regardless of tenant retention settings.
func (a Action) Sensitive() bool {
	switch a {
	case ActionSpecApproved, ActionApprovalGranted, ActionExecutionStarted,
		ActionExecutionSucceeded, ActionExecutionFailed, ActionRollbackStarted,
		ActionRollbackCompleted, ActionRollbackFailed, ActionPolicyActivated,
		ActionCrossTenantAccess, ActionAWSAssumeRole, ActionUserRoleChanged,
		ActionDataExported:
		return true
	}
	return false
}

// Outcome is the result of an audited operation.
type Outcome string

const (
	OutcomeSuccess Outcome = "success"
	OutcomeFailure Outcome = "failure"
	OutcomeDenied  Outcome = "denied"
	OutcomePartial Outcome = "partial"
)

// Record is one immutable audit entry.
type Record struct {
	ID       core.ID       `json:"id"`
	TenantID core.TenantID `json:"tenant_id"`
	Sequence int64         `json:"sequence"`

	Action  Action  `json:"action"`
	Outcome Outcome `json:"outcome"`

	// Actor identifies who did it. Machine actors are labelled as such so a
	// query can separate human decisions from automation.
	Actor        string      `json:"actor"`
	ActorRoles   []core.Role `json:"actor_roles,omitempty"`
	ActorMachine bool        `json:"actor_machine"`
	IPAddress    string      `json:"ip_address,omitempty"`
	UserAgent    string      `json:"user_agent,omitempty"`
	RequestID    string      `json:"request_id,omitempty"`
	TraceID      string      `json:"trace_id,omitempty"`

	// Subject is what was acted on.
	SubjectKind string  `json:"subject_kind"`
	SubjectID   core.ID `json:"subject_id,omitempty"`
	SubjectName string  `json:"subject_name,omitempty"`

	// Before and After carry the state transition. For AWS mutations these
	// are the resource attributes; for approvals, the request state; for spec
	// changes, the diff. They are what make the log reconstructive rather
	// than merely descriptive.
	Before map[string]any `json:"before,omitempty"`
	After  map[string]any `json:"after,omitempty"`

	// AWSOperation records the exact API call for mutations, so a customer's
	// CloudTrail can be reconciled with CloudOptix's own record.
	AWSOperation string         `json:"aws_operation,omitempty"`
	AWSAccountID core.AccountID `json:"aws_account_id,omitempty"`
	AWSRegion    core.Region    `json:"aws_region,omitempty"`
	AWSRequestID string         `json:"aws_request_id,omitempty"`

	// Linked identifiers let one query assemble the whole story of a change.
	RecommendationID core.ID `json:"recommendation_id,omitempty"`
	PlanID           core.ID `json:"plan_id,omitempty"`
	ApprovalID       core.ID `json:"approval_id,omitempty"`
	PolicyDecisionID core.ID `json:"policy_decision_id,omitempty"`
	SpecVersionID    core.ID `json:"spec_version_id,omitempty"`

	Message  string         `json:"message"`
	Metadata map[string]any `json:"metadata,omitempty"`
	Error    string         `json:"error,omitempty"`

	At time.Time `json:"at"`

	// PrevHash and Hash form the tamper-evident chain.
	PrevHash string `json:"prev_hash"`
	Hash     string `json:"hash"`
}

// ComputeHash produces the record's content hash over its immutable fields.
// The Hash field itself is excluded, and fields are serialised in a fixed
// order so the hash is reproducible across processes and Go versions.
func (r Record) ComputeHash() string {
	payload := struct {
		Sequence  int64          `json:"sequence"`
		TenantID  core.TenantID  `json:"tenant_id"`
		Action    Action         `json:"action"`
		Outcome   Outcome        `json:"outcome"`
		Actor     string         `json:"actor"`
		Subject   string         `json:"subject"`
		SubjectID core.ID        `json:"subject_id"`
		Before    map[string]any `json:"before"`
		After     map[string]any `json:"after"`
		AWSOp     string         `json:"aws_op"`
		Message   string         `json:"message"`
		At        string         `json:"at"`
		PrevHash  string         `json:"prev_hash"`
	}{
		Sequence: r.Sequence, TenantID: r.TenantID, Action: r.Action, Outcome: r.Outcome,
		Actor: r.Actor, Subject: r.SubjectKind, SubjectID: r.SubjectID,
		Before: r.Before, After: r.After, AWSOp: r.AWSOperation,
		Message: r.Message, At: r.At.UTC().Format(time.RFC3339Nano), PrevHash: r.PrevHash,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		// A record that cannot be serialised cannot be chained; hashing the
		// error text still produces a stable, distinct value that will fail
		// verification loudly rather than silently matching.
		raw = []byte("unserializable:" + err.Error())
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Seal finalises a record by linking it to its predecessor and computing its
// hash. A record is only valid once sealed.
func (r *Record) Seal(prevHash string, sequence int64) {
	if r.At.IsZero() {
		r.At = time.Now().UTC()
	}
	if r.ID.IsZero() {
		r.ID = core.NewID("aud")
	}
	r.Sequence = sequence
	r.PrevHash = prevHash
	r.Hash = r.ComputeHash()
}

// ChainVerification is the result of verifying a tenant's audit chain.
type ChainVerification struct {
	TenantID       core.TenantID `json:"tenant_id"`
	RecordsChecked int           `json:"records_checked"`
	Valid          bool          `json:"valid"`
	FirstBreakAt   *int64        `json:"first_break_at,omitempty"`
	BreakDetail    string        `json:"break_detail,omitempty"`
	VerifiedAt     time.Time     `json:"verified_at"`
}

// VerifyChain walks an ordered record slice and reports the first break.
func VerifyChain(tenant core.TenantID, records []Record) ChainVerification {
	v := ChainVerification{TenantID: tenant, Valid: true, VerifiedAt: time.Now().UTC()}
	sorted := make([]Record, len(records))
	copy(sorted, records)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Sequence < sorted[j].Sequence })

	prev := ""
	for i, r := range sorted {
		v.RecordsChecked++
		if r.PrevHash != prev {
			v.Valid = false
			seq := r.Sequence
			v.FirstBreakAt = &seq
			v.BreakDetail = fmt.Sprintf("record %d expected prev_hash %q but stored %q — a preceding record was altered or removed",
				r.Sequence, prev, r.PrevHash)
			return v
		}
		if got := r.ComputeHash(); got != r.Hash {
			v.Valid = false
			seq := r.Sequence
			v.FirstBreakAt = &seq
			v.BreakDetail = fmt.Sprintf("record %d content hash mismatch: stored %q, recomputed %q — the record was altered",
				r.Sequence, r.Hash, got)
			return v
		}
		prev = r.Hash
		_ = i
	}
	return v
}

// Query is the audit search filter.
type Query struct {
	TenantID    core.TenantID
	Actions     []Action
	Actors      []string
	SubjectID   core.ID
	Outcomes    []Outcome
	From, To    time.Time
	OnlyMachine *bool
	Limit       int
	Cursor      string
}
