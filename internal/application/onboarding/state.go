package onboarding

import (
	"fmt"
	"strings"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/spec"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// pathLabels gives every tracked provenance path a short, human label for
// the review UI. It is built from stageFields (which already carry a label
// for the question-bank paths) plus a handful of paths this package sets
// provenance for but never asks a direct question about (inferred-only
// fields).
var pathLabels = func() map[string]string {
	m := map[string]string{
		"application.domain":            "business domain",
		"application.criticality":       "application criticality",
		"objectives.costSlos":           "cost SLOs",
		"security.complianceFrameworks": "compliance frameworks",
	}
	for _, fields := range stageFields {
		for _, f := range fields {
			m[f.Path] = f.Label
		}
	}
	return m
}()

// renderFieldValue renders the current spec value at path as display text.
// Only paths this package actually sets provenance for need an entry; a
// path with no entry renders as "" and is skipped by buildFieldStates,
// which is the mechanism informational-only paths like
// "aws.accounts.environment" (set for provenance bookkeeping, not shown as
// its own row — the account list itself covers it) use to stay out of the
// UI.
func renderFieldValue(path string, draft spec.Spec) string {
	switch path {
	case "organization.name":
		return draft.Organization.Name
	case "organization.industry":
		return draft.Organization.Industry
	case "application.name":
		return draft.Application.Name
	case "application.domain":
		return draft.Application.Domain
	case "application.criticality":
		return draft.Application.Criticality
	case "application.architecture.style":
		return draft.Application.Architecture.Style
	case "application.architecture.computePlatforms":
		return strings.Join(draft.Application.Architecture.ComputePlatforms, ", ")
	case "application.architecture.databases":
		return strings.Join(draft.Application.Architecture.Databases, ", ")
	case "aws.accounts":
		ids := make([]string, len(draft.AWS.Accounts))
		for i, a := range draft.AWS.Accounts {
			ids[i] = fmt.Sprintf("%s (%s)", a.ID, a.Environment)
		}
		return strings.Join(ids, ", ")
	case "security.awsAccessMode":
		if draft.Security.AWSAccessMode != "" {
			return draft.Security.AWSAccessMode
		}
		return draft.AWS.AccessMode
	case "business.transactions":
		names := make([]string, len(draft.Business.Transactions))
		for i, t := range draft.Business.Transactions {
			names[i] = fmt.Sprintf("%s (%.0f/mo)", t.Name, t.MonthlyVolume)
		}
		return strings.Join(names, ", ")
	case "objectives.costReductionTarget":
		return fmt.Sprintf("%.0f%%", draft.Objectives.CostReductionTarget*100)
	case "objectives.monthlyBudget":
		return fmt.Sprintf("$%.0f", draft.Objectives.MonthlyBudget)
	case "objectives.availabilityTarget":
		return fmt.Sprintf("%.4f%%", draft.Objectives.AvailabilityTarget*100)
	case "objectives.maxLatencyMs":
		return fmt.Sprintf("%.0fms", draft.Objectives.MaxLatencyMS)
	case "objectives.costSlos":
		names := make([]string, len(draft.Objectives.CostSLOs))
		for i, c := range draft.Objectives.CostSLOs {
			names[i] = c.Name
		}
		return strings.Join(names, ", ")
	case "optimization.riskTolerance":
		return draft.Optimization.RiskTolerance
	case "governance.productionChangesRequireApproval":
		return fmt.Sprintf("%v", draft.Governance.ProductionChangesRequireApproval)
	case "security.complianceFrameworks":
		return strings.Join(draft.Security.ComplianceFrameworks, ", ")
	default:
		return ""
	}
}

// buildFieldStates splits every tracked provenance path in draft into the
// four buckets the onboarding UI renders: Collected (user-confirmed),
// Inferred, Unknown (asked, no answer) and NeedsConfirmation (a proposed
// value awaiting sign-off — this package does not currently produce any,
// but the bucket exists in ports.OnboardingState so a future confirmation
// flow has somewhere to put them).
func buildFieldStates(draft spec.Spec) (collected, inferred, unknown, needsConfirm []ports.FieldState) {
	for _, path := range sortedProvenancePaths(draft) {
		label, hasLabel := pathLabels[path]
		if !hasLabel {
			continue
		}
		prov := draft.Provenance[path]
		fs := ports.FieldState{
			Path: path, Label: label, Provenance: prov,
			Value: renderFieldValue(path, draft),
		}
		if r, ok := rationaleFor(draft, path); ok {
			fs.Rationale = r
		}
		switch prov {
		case core.ProvenanceConfirmed:
			collected = append(collected, fs)
		case core.ProvenanceInferred:
			inferred = append(inferred, fs)
		case core.ProvenanceUnknown:
			unknown = append(unknown, fs)
		case core.ProvenanceRequiresConfirmation:
			needsConfirm = append(needsConfirm, fs)
		}
	}
	return
}

// rationaleFor looks up the one-sentence explanation runInference attached
// via upsertInferenceNote, falling back to a generic note for inferred
// fields it did not annotate individually.
func rationaleFor(draft spec.Spec, path string) (string, bool) {
	for _, q := range draft.OpenQuestions {
		if q.Path == path && q.Why != "" {
			return q.Why, true
		}
	}
	if draft.Provenance[path] == core.ProvenanceInferred {
		return "derived from other answers in this conversation", true
	}
	return "", false
}

// buildOnboardingState assembles the full ports.OnboardingState the chat UI
// renders after every turn.
func buildOnboardingState(conv ports.Conversation, draft spec.Spec, reply, stage string, degraded bool, suggestions []string) ports.OnboardingState {
	completeness := draft.AssessCompleteness()
	validation := draft.Validate()
	collected, inferred, unknown, needsConfirm := buildFieldStates(draft)

	var openQuestions []spec.OpenQuestion
	for _, q := range draft.OpenQuestions {
		if !isPathResolved(draft, q.Path) {
			openQuestions = append(openQuestions, q)
		}
	}

	return ports.OnboardingState{
		ConversationID:    conv.ID,
		Reply:             reply,
		Stage:             stage,
		Draft:             draft,
		Completeness:      completeness,
		Collected:         collected,
		Inferred:          inferred,
		Unknown:           unknown,
		NeedsConfirmation: needsConfirm,
		OpenQuestions:     openQuestions,
		Validation:        validation,
		// ReadyForReview mirrors exactly what Approve will accept — the
		// specification's own blocking-gap analysis — rather than waiting
		// for the conversational stage machine to reach StageReview. The
		// two can legitimately disagree: the stage machine keeps asking a
		// few optional questions (architecture details, workloads) even
		// after every REQUIRED field is already known, and a user should
		// be able to approve the moment they could, not only once they
		// have humoured every optional question too.
		ReadyForReview: completeness.ReadyForReview,
		Suggestions:    suggestions,
		Degraded:       degraded,
	}
}

// newDraftSpec builds the empty specification a new onboarding conversation
// starts from.
func newDraftSpec() spec.Spec {
	return spec.Spec{
		APIVersion: spec.CurrentAPIVersion,
		Kind:       spec.KindSpec,
		Provenance: map[string]core.Provenance{},
		Security:   spec.Security{EncryptionRequired: true},
	}
}
