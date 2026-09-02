package govern

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// Checksum is the platform's content fingerprint, used for policy versions,
// spec versions and decision digests.
func Checksum(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:32]
}

// ApprovalState is the lifecycle of an approval request.
type ApprovalState string

const (
	ApprovalPending   ApprovalState = "pending"
	ApprovalApproved  ApprovalState = "approved"
	ApprovalRejected  ApprovalState = "rejected"
	ApprovalExpired   ApprovalState = "expired"
	ApprovalCancelled ApprovalState = "cancelled"
)

// SubjectKind names what is being approved. Approvals are not only for
// optimizations: the onboarding specification itself is approved through the
// same mechanism, so there is one audit trail for every consequential
// human decision in the platform.
type SubjectKind string

const (
	SubjectRecommendation SubjectKind = "recommendation"
	SubjectExecutionPlan  SubjectKind = "execution_plan"
	SubjectSpec           SubjectKind = "spec"
	SubjectPolicy         SubjectKind = "policy"
	SubjectAWSConnection  SubjectKind = "aws_connection"
	SubjectCommitment     SubjectKind = "commitment_purchase"
)

// Request is a pending human decision.
type Request struct {
	ID       core.ID       `json:"id"`
	TenantID core.TenantID `json:"tenant_id"`

	SubjectKind SubjectKind `json:"subject_kind"`
	SubjectID   core.ID     `json:"subject_id"`
	Title       string      `json:"title"`
	Summary     string      `json:"summary"`

	// Context is the decision packet a reviewer needs without leaving the
	// approval screen: the saving, the blast radius, the risk, the rollback
	// plan and the policy decision that demanded the approval.
	Context ApprovalContext `json:"context"`

	PolicyDecisionID        core.ID  `json:"policy_decision_id,omitempty"`
	RequiredRoles           []string `json:"required_roles,omitempty"`
	MinApprovals            int      `json:"min_approvals"`
	RequireDistinctApprover bool     `json:"require_distinct_approver"`

	State     ApprovalState `json:"state"`
	Responses []Response    `json:"responses,omitempty"`

	RequestedBy string     `json:"requested_by"`
	RequestedAt time.Time  `json:"requested_at"`
	ExpiresAt   time.Time  `json:"expires_at"`
	DecidedAt   *time.Time `json:"decided_at,omitempty"`
	// ExecuteAfter defers execution to a maintenance window even once
	// approved, so an approver can say yes at 2pm for a 2am change.
	ExecuteAfter *time.Time `json:"execute_after,omitempty"`
}

// ApprovalContext is the reviewer's decision packet.
type ApprovalContext struct {
	MonthlySaving     core.Money       `json:"monthly_saving,omitempty"`
	AnnualSaving      core.Money       `json:"annual_saving,omitempty"`
	MonthlyCostDelta  core.Money       `json:"monthly_cost_delta,omitempty"`
	Confidence        core.Confidence  `json:"confidence,omitempty"`
	RiskLevel         core.RiskLevel   `json:"risk_level,omitempty"`
	BlastSummary      string           `json:"blast_summary,omitempty"`
	Environment       core.Environment `json:"environment,omitempty"`
	AffectedResources []string         `json:"affected_resources,omitempty"`
	RollbackPlan      string           `json:"rollback_plan,omitempty"`
	ValidationPlan    string           `json:"validation_plan,omitempty"`
	PolicyReason      string           `json:"policy_reason,omitempty"`
	Diff              string           `json:"diff,omitempty"`
}

// Response is one approver's vote.
type Response struct {
	Principal string    `json:"principal"`
	Role      core.Role `json:"role"`
	Approved  bool      `json:"approved"`
	Comment   string    `json:"comment,omitempty"`
	At        time.Time `json:"at"`
	// IPAddress and UserAgent are captured for the audit trail because an
	// approval is the moment a human takes responsibility for a change.
	IPAddress string `json:"ip_address,omitempty"`
	UserAgent string `json:"user_agent,omitempty"`
}

// Decide records a vote and advances the request state.
//
// The rules encoded here are the segregation-of-duties controls: a principal
// votes at most once, a rejection is immediately final, and the requester
// cannot approve their own change when the policy demands a distinct
// approver.
func (r *Request) Decide(resp Response) error {
	if r.State != ApprovalPending {
		return core.Conflict("approval request %s is already %s", r.ID, r.State)
	}
	if time.Now().UTC().After(r.ExpiresAt) && !r.ExpiresAt.IsZero() {
		r.State = ApprovalExpired
		return core.NewError(core.ErrPreconditionOff, "approval_expired",
			"approval request %s expired at %s", r.ID, r.ExpiresAt.Format(time.RFC3339))
	}
	for _, existing := range r.Responses {
		if existing.Principal == resp.Principal {
			return core.Conflict("principal %s has already responded to %s", resp.Principal, r.ID)
		}
	}
	if r.RequireDistinctApprover && resp.Principal == r.RequestedBy && resp.Approved {
		return core.Forbidden("segregation of duties: %s requested this change and may not approve it", resp.Principal)
	}
	if resp.At.IsZero() {
		resp.At = time.Now().UTC()
	}
	r.Responses = append(r.Responses, resp)

	if !resp.Approved {
		// One rejection ends it. Requiring unanimity to reject would let a
		// change proceed over a reviewer's stated objection.
		r.State = ApprovalRejected
		now := resp.At
		r.DecidedAt = &now
		return nil
	}
	approvals := 0
	for _, x := range r.Responses {
		if x.Approved {
			approvals++
		}
	}
	min := r.MinApprovals
	if min <= 0 {
		min = 1
	}
	if approvals >= min {
		r.State = ApprovalApproved
		now := resp.At
		r.DecidedAt = &now
	}
	return nil
}

// Approved reports whether the request cleared.
func (r Request) Approved() bool { return r.State == ApprovalApproved }

// ApprovalCount returns how many approvals have been recorded.
func (r Request) ApprovalCount() int {
	n := 0
	for _, x := range r.Responses {
		if x.Approved {
			n++
		}
	}
	return n
}

// Describe renders the request for a notification.
func (r Request) Describe() string {
	return fmt.Sprintf("[%s] %s — %s (%d/%d approvals)",
		r.SubjectKind, r.Title, r.State, r.ApprovalCount(), r.MinApprovals)
}
