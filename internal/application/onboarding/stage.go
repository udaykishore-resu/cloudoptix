package onboarding

import (
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/spec"
)

// Stage names, in the fixed order onboarding progresses through. The order
// is what the UI's progress indicator renders and what decides which
// questions the agent asks next; it does not restrict what a turn may
// extract, only what it will ASK about if the user hasn't already said it.
const (
	StageOrganization = "organization"
	StageApplication  = "application"
	StageAWS          = "aws"
	StageWorkloads    = "workloads"
	StageBusiness     = "business"
	StageObjectives   = "objectives"
	StageGovernance   = "governance"
	StageReview       = "review"
)

// stageOrder lists every stage in progression order.
var stageOrder = []string{
	StageOrganization, StageApplication, StageAWS, StageWorkloads,
	StageBusiness, StageObjectives, StageGovernance, StageReview,
}

// nextStage returns the stage after s, or StageReview if s is the last one
// or unrecognised.
func nextStage(s string) string {
	for i, name := range stageOrder {
		if name == s && i+1 < len(stageOrder) {
			return stageOrder[i+1]
		}
	}
	return StageReview
}

// stageField is one question the agent may ask during a stage: which
// specification path it fills, the question text, whether an unanswered
// value blocks review, and how to tell the value is already known from the
// current draft.
type stageField struct {
	Path     string
	Label    string
	Question string
	Blocking bool
	Known    func(s spec.Spec) bool
}

// stageFields is the question bank, grouped by the stage each belongs to.
// Blocking marks the fields spec.Validate/AssessCompleteness treat as
// required — see spec.Spec.AssessCompleteness, which this list is kept
// aligned with so the review screen and the agent's own sense of "done"
// never disagree.
var stageFields = map[string][]stageField{
	StageOrganization: {
		{"organization.name", "organization name", "What's your company called?", true,
			func(s spec.Spec) bool { return s.Organization.Name != "" }},
		{"organization.industry", "industry", "What industry are you in?", false,
			func(s spec.Spec) bool { return s.Organization.Industry != "" }},
	},
	StageApplication: {
		{"application.name", "application name", "What's the name of the application or platform CloudOptix will be optimizing?", true,
			func(s spec.Spec) bool { return s.Application.Name != "" }},
		{"application.architecture.style", "architecture style", "Is it built as microservices, a monolith, or serverless?", false,
			func(s spec.Spec) bool { return s.Application.Architecture.Style != "" }},
		{"application.architecture.computePlatforms", "compute platforms", "What do you run it on — EKS, ECS, Fargate, Lambda, EC2?", false,
			func(s spec.Spec) bool { return len(s.Application.Architecture.ComputePlatforms) > 0 }},
	},
	StageAWS: {
		{"aws.accounts", "AWS accounts", "What AWS account ID(s) should CloudOptix analyse, and which regions?", true,
			func(s spec.Spec) bool { return len(s.AWS.Accounts) > 0 }},
		{"security.awsAccessMode", "AWS access mode", "CloudOptix connects by assuming a least-privilege IAM role in your account, never by storing access keys — is that acceptable?", true,
			func(s spec.Spec) bool { return s.Security.AWSAccessMode != "" || s.AWS.AccessMode != "" }},
	},
	StageWorkloads: {
		{"workloads", "workloads", "Are there specific workloads or services you especially want tracked?", false,
			func(s spec.Spec) bool { return len(s.Workloads) > 0 }},
	},
	StageBusiness: {
		{"business.transactions", "business transactions", "What business transactions matter for cost-per-unit — e.g. \"40,000 checkouts per month\"?", false,
			func(s spec.Spec) bool { return len(s.Business.Transactions) > 0 }},
	},
	StageObjectives: {
		{"objectives.costReductionTarget", "cost reduction target", "Do you have a cost reduction target, like \"cut spend by 25%\"?", false,
			func(s spec.Spec) bool { return s.Objectives.CostReductionTarget > 0 }},
		{"objectives.availabilityTarget", "availability target", "What availability target should CloudOptix protect, e.g. 99.9%?", false,
			func(s spec.Spec) bool { return s.Objectives.AvailabilityTarget > 0 }},
	},
	StageGovernance: {
		{"optimization.riskTolerance", "risk tolerance", "How would you describe your risk tolerance for optimization changes — low, medium or high?", true,
			func(s spec.Spec) bool { return s.Optimization.RiskTolerance != "" }},
		{"governance.productionChangesRequireApproval", "production approval policy", "Should production changes always require human approval?", false,
			func(s spec.Spec) bool {
				return s.Governance.ProductionChangesRequireApproval || !hasProductionAccount(s)
			}},
	},
}

func hasProductionAccount(s spec.Spec) bool {
	for _, a := range s.AWS.Accounts {
		if a.Production || a.Environment == "production" {
			return true
		}
	}
	return false
}

// outstandingInStage returns the fields for stage that are neither known in
// draft nor explicitly answered "I don't know" (core.ProvenanceUnknown), in
// declaration order. A field marked Unknown is resolved as far as the agent
// is concerned — it stops asking about it — even though the underlying spec
// value stays at its zero value.
func outstandingInStage(stage string, draft spec.Spec) []stageField {
	var out []stageField
	for _, f := range stageFields[stage] {
		if f.Known(draft) {
			continue
		}
		if draft.Provenance[f.Path] == core.ProvenanceUnknown {
			continue
		}
		out = append(out, f)
	}
	return out
}

// stageComplete reports whether every field on a stage is known — the
// signal used to advance to the next stage.
func stageComplete(stage string, draft spec.Spec) bool {
	return len(outstandingInStage(stage, draft)) == 0
}
