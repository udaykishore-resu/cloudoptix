package optimization

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cost"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/execute"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/spec"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// This file implements ports.OptimizationService: the use case that turns a
// discovered estate into a ranked, evidence-backed recommendation set. It is
// the only place in this package that talks to repositories — everything
// upstream (engine.go, rule.go, confidence.go, blast.go, risk.go, the rule_*
// files) is pure given an EvalContext, and stays that way so it can be
// tested without a database. Analyze is what assembles that EvalContext from
// live repositories and turns the deterministic Finding output into
// Recommendations a human reviews.
//
// Traceability: REQ-OPT-001, REQ-OPT-012, SPEC-OPT-006.

// Deps is every dependency Service needs, expressed as ports so this package
// never imports an adapter. Resources, Metrics, Costs, Recommendations,
// Specs, Pricing and Registry are required — NewService refuses to build
// without them rather than degrading silently at call time. Savings
// (calibration history and outcome corpus), Policies (policy-decision lookup
// for Explain) and Events (notifications) are optional: their absence
// narrows what a call returns, it never fails the call.
type Deps struct {
	Resources       ports.ResourceRepository
	Metrics         ports.MetricRepository
	Costs           ports.CostRepository
	Recommendations ports.RecommendationRepository
	Specs           ports.SpecRepository
	Savings         ports.SavingsRepository // optional
	Policies        ports.PolicyRepository  // optional
	Pricing         ports.PricingCatalog
	Registry        *Registry
	Events          ports.EventPublisher // optional
	Clock           core.Clock
	// Formula is the default PriorityFormula used when an AnalyzeRequest does
	// not supply one. The zero value is replaced with
	// optimize.DefaultPriorityFormula() by NewService.
	Formula optimize.PriorityFormula
	Logger  *slog.Logger
}

// Service implements ports.OptimizationService.
type Service struct {
	d Deps
}

var _ ports.OptimizationService = (*Service)(nil)

// NewService validates the required dependencies and fills in defaults for
// the optional ones (system clock, a default logger, the platform's default
// priority formula), so every other method can assume a complete Deps.
func NewService(d Deps) (*Service, error) {
	var missing []string
	if d.Resources == nil {
		missing = append(missing, "Resources")
	}
	if d.Metrics == nil {
		missing = append(missing, "Metrics")
	}
	if d.Costs == nil {
		missing = append(missing, "Costs")
	}
	if d.Recommendations == nil {
		missing = append(missing, "Recommendations")
	}
	if d.Specs == nil {
		missing = append(missing, "Specs")
	}
	if d.Pricing == nil {
		missing = append(missing, "Pricing")
	}
	if d.Registry == nil {
		missing = append(missing, "Registry")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("optimization: NewService missing required dependencies: %v", missing)
	}
	if d.Clock == nil {
		d.Clock = core.SystemClock{}
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	zeroFormula := optimize.PriorityFormula{}
	if d.Formula == zeroFormula {
		d.Formula = optimize.DefaultPriorityFormula()
	}
	return &Service{d: d}, nil
}

// Analyze loads the current estate, evaluates every enabled rule
// deterministically, shapes each surviving finding into a fully-scored
// Recommendation (confidence, blast radius, risk, priority), supersedes the
// prior generation of open recommendations when this is an unscoped full
// run, and persists the result.
//
// AnalyzeRequest.GenerateNarratives is accepted but not acted on: narrative
// generation is an LLM call this package deliberately has no port for — see
// optimize.Recommendation.Narrative's own doc comment, which says the field
// is decorative and never affects what is computed here. Wiring an LLM
// summarizer belongs to a caller that composes this Service with one, not to
// the deterministic engine itself. AnalyzeRequest.Async is accepted but
// ignored: this Service has no job-queue port, so every call runs
// synchronously; a caller wanting async execution wraps this call in its own
// goroutine or queue at a higher layer.
func (s *Service) Analyze(ctx context.Context, tenant core.TenantID, in ports.AnalyzeRequest) (ports.AnalyzeResult, error) {
	runID := core.NewID("run")
	now := s.d.Clock.Now()

	rf := ports.ResourceFilter{AccountIDs: in.AccountIDs, Environments: in.Environments}
	inv, err := s.d.Resources.LoadInventory(ctx, tenant, rf)
	if err != nil {
		return ports.AnalyzeResult{}, fmt.Errorf("optimization: loading inventory: %w", err)
	}
	topo, err := s.d.Resources.LoadTopology(ctx, tenant, rf)
	if err != nil {
		return ports.AnalyzeResult{}, fmt.Errorf("optimization: loading topology: %w", err)
	}

	ids := make([]core.ID, 0, inv.Len())
	for _, r := range inv.All() {
		ids = append(ids, r.ID)
	}
	metrics, err := s.d.Metrics.LoadSummaries(ctx, tenant, ids)
	if err != nil {
		// Telemetry is best-effort for an analysis run: every rule's own
		// insufficient-data guard already treats a missing entry as "no
		// signal", so a metrics-store outage degrades to config-only
		// findings rather than failing the whole run.
		s.d.Logger.Warn("optimization: loading metric summaries failed; continuing with no telemetry", "error", err)
		metrics = map[core.ID]ports.ResourceMetrics{}
	}

	costByResource, err := s.d.Costs.ByResource(ctx, tenant, ports.CostFilter{
		Period:       core.Period{Start: now.AddDate(0, -1, 0), End: now},
		AccountIDs:   in.AccountIDs,
		Environments: in.Environments,
		Basis:        cost.BasisAmortized,
	})
	if err != nil {
		s.d.Logger.Warn("optimization: loading attributed cost failed; falling back to each resource's own MonthlyCost", "error", err)
		costByResource = map[core.ID]core.Money{}
	}

	sp, err := s.loadActiveSpec(ctx, tenant)
	if err != nil {
		return ports.AnalyzeResult{}, fmt.Errorf("optimization: loading active spec: %w", err)
	}

	calib, err := s.loadCalibrations(ctx, tenant)
	if err != nil {
		s.d.Logger.Warn("optimization: loading rule calibrations failed; treating every rule as uncalibrated", "error", err)
		calib = nil
	}

	ec := EvalContext{
		TenantID:       tenant,
		Inventory:      inv,
		Topology:       topo,
		Metrics:        metrics,
		CostByResource: costByResource,
		Pricing:        s.d.Pricing,
		Spec:           sp,
		Calibrations:   calib,
		Thresholds:     s.d.Registry,
		Clock:          s.d.Clock,
	}

	findings, diag := s.d.Registry.Evaluate(ctx, ec)
	findings = filterFindings(findings, in)

	recs := make([]optimize.Recommendation, 0, len(findings))
	for _, f := range findings {
		res, ok := inv.ByID(f.ResourceID)
		if !ok {
			s.d.Logger.Warn("optimization: finding references a resource no longer in the inventory", "rule", f.RuleID, "resource", f.ResourceID)
			continue
		}
		rule, ok := s.d.Registry.Get(f.RuleID)
		if !ok {
			s.d.Logger.Warn("optimization: finding references an unregistered rule", "rule", f.RuleID)
			continue
		}
		recs = append(recs, s.buildRecommendation(ec, rule, res, f, calib))
	}

	formula := s.d.Formula
	if in.Formula != nil {
		formula = *in.Formula
	}
	recs = optimize.Rank(recs, formula)
	// Grouping runs after ranking, never before: the primary of a conflict
	// group is defined as the member the priority formula ranks highest, and
	// PriorityScore does not exist until Rank has computed it.
	recs = optimize.GroupConflicts(recs)

	superseded := 0
	if isUnscoped(in) {
		keepIDs := make([]core.ID, 0, len(recs))
		for _, r := range recs {
			keepIDs = append(keepIDs, r.ID)
		}
		n, err := s.d.Recommendations.SupersedeStale(ctx, tenant, now, keepIDs)
		if err != nil {
			s.d.Logger.Warn("optimization: superseding stale recommendations failed", "error", err)
		} else {
			superseded = n
		}
	}

	if len(recs) > 0 {
		if err := s.d.Recommendations.SaveBatch(ctx, tenant, recs); err != nil {
			return ports.AnalyzeResult{}, fmt.Errorf("optimization: saving recommendations: %w", err)
		}
	}
	s.publishCreated(ctx, tenant, runID, recs)

	total := optimize.TotalPotentialSaving(recs)
	autoExec := 0
	for _, r := range recs {
		if r.AutoExecutable {
			autoExec++
		}
	}

	return ports.AnalyzeResult{
		RunID:                         runID,
		ResourcesAnalyzed:             diag.ResourcesConsidered,
		RulesEvaluated:                diag.RulesEvaluated,
		FindingsProduced:              diag.FindingsProduced,
		RecommendationsCreated:        len(recs),
		Superseded:                    superseded,
		MutuallyExclusiveAlternatives: optimize.CountAlternatives(recs),
		TotalMonthlySaving:            total,
		TotalAnnualSaving:             total.Annualized(),
		AutoExecutable:                autoExec,
	}, nil
}

// buildRecommendation turns one validated Finding into a fully-scored
// Recommendation. It calls the same rule's BuildAction that produced the
// finding, so the action, evidence and finding can never disagree about what
// is being proposed.
func (s *Service) buildRecommendation(ec EvalContext, rule FullRule, res cloud.Resource, f optimize.Finding, calib map[optimize.RuleID]execute.RuleCalibration) optimize.Recommendation {
	action := rule.BuildAction(ec, res, f)
	blast := ComputeBlastRadius(ec, res)
	risk := ComputeRisk(ec, res, action.Type, action.Reversibility, blast)

	m, _ := MetricsFor(ec, res.ID)
	confidence, confInputs := ComputeConfidence(ec, res, f.RuleID, m, primaryPercentile(f), blast)

	// The saving multiplier is the learning loop's other lever alongside the
	// confidence multiplier (see confidence.go): it discounts a rule's
	// projected saving toward what it has actually delivered historically,
	// bounded to [0.3, 1.2] at calibration time for the same reason
	// over-confidence is bounded there — a rule that has chronically
	// overstated its savings should have that reflected in the number a
	// reviewer sees, not just in a separate confidence figure.
	savingMultiplier := 1.0
	if c, ok := calib[f.RuleID]; ok && c.Samples > 0 && c.SavingMultiplier > 0 {
		savingMultiplier = c.SavingMultiplier
	}
	saving := capSaving(f.EstimatedMonthlySaving.Scale(savingMultiplier), f.CurrentMonthlyCost)

	now := ec.Now()
	return optimize.Recommendation{
		ID:         core.NewID("rec"),
		TenantID:   ec.TenantID,
		Finding:    f,
		Title:      action.Title,
		Rationale:  action.Rationale,
		Action:     action.Type,
		Parameters: action.Parameters,
		// ConflictDomain is the rule's own statement of what this change
		// competes for; GroupConflicts (called once over the whole ranked
		// set, in Analyze) is what turns those statements into groups.
		ConflictDomain: action.resolvedConflictDomain(),
		CurrentState: optimize.StateSnapshot{
			InstanceType: res.InstanceType,
			VCPU:         res.Capacity.VCPU,
			MemoryGiB:    res.Capacity.MemoryGiB,
			MonthlyCost:  f.CurrentMonthlyCost,
		},
		ProposedState:          action.ProposedState,
		EstimatedMonthlySaving: saving,
		EstimatedAnnualSaving:  saving.Annualized(),
		Confidence:             confidence,
		ConfidenceBasis:        confInputs,
		Risk:                   risk,
		BlastRadius:            blast,
		Reversibility:          action.Reversibility,
		Complexity:             action.Complexity,
		Status:                 optimize.StatusOpen,
		// RequiresApproval, PolicyDecisionID and AutoExecutable are left at
		// their zero values deliberately: those are the policy engine's
		// call, not this rule engine's — see optimize.Recommendation's own
		// doc comment. A recommendation never self-assesses whether it may
		// run.
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// primaryPercentile returns the first metric-kind evidence's percentile
// summary, which is the same series the rule's own decision logic leaned on
// most — see ComputeConfidence's doc comment on why that series, not a
// self-reported score, is what confidence is computed from.
func primaryPercentile(f optimize.Finding) *core.Percentiles {
	for _, e := range f.Evidence {
		if e.Kind == "metric" && e.Percentiles != nil {
			return e.Percentiles
		}
	}
	return nil
}

// filterFindings applies the request's category/rule-ID narrowing.
// AccountIDs/Environments are already applied upstream via the resource
// filter that built the inventory, so they are not re-applied here.
func filterFindings(findings []optimize.Finding, in ports.AnalyzeRequest) []optimize.Finding {
	if len(in.Categories) == 0 && len(in.RuleIDs) == 0 {
		return findings
	}
	catSet := make(map[optimize.Category]bool, len(in.Categories))
	for _, c := range in.Categories {
		catSet[c] = true
	}
	ruleSet := make(map[optimize.RuleID]bool, len(in.RuleIDs))
	for _, r := range in.RuleIDs {
		ruleSet[r] = true
	}
	out := make([]optimize.Finding, 0, len(findings))
	for _, f := range findings {
		if len(catSet) > 0 && !catSet[f.Category] {
			continue
		}
		if len(ruleSet) > 0 && !ruleSet[f.RuleID] {
			continue
		}
		out = append(out, f)
	}
	return out
}

// isUnscoped reports whether an AnalyzeRequest carries no narrowing filter,
// i.e. is a full-estate run. Only a full run is allowed to supersede the
// tenant's entire prior open recommendation set — a scoped run (one account,
// one category) superseding globally would wrongly discard open
// recommendations the run never even looked at.
func isUnscoped(in ports.AnalyzeRequest) bool {
	return len(in.AccountIDs) == 0 && len(in.Environments) == 0 && len(in.Categories) == 0 && len(in.RuleIDs) == 0
}

func (s *Service) loadActiveSpec(ctx context.Context, tenant core.TenantID) (spec.Spec, error) {
	v, err := s.d.Specs.GetActive(ctx, tenant)
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			s.d.Logger.Warn("optimization: tenant has no active spec version; analyzing with an unrestricted default spec", "tenant", tenant)
			return spec.Spec{}, nil
		}
		return spec.Spec{}, err
	}
	return v.Spec, nil
}

func (s *Service) loadCalibrations(ctx context.Context, tenant core.TenantID) (map[optimize.RuleID]execute.RuleCalibration, error) {
	if s.d.Savings == nil {
		return nil, nil
	}
	return s.d.Savings.LoadCalibrations(ctx, tenant)
}

// publishCreated emits one EventRecommendationCreated per recommendation.
// Publishing is best-effort: a notification-bus outage must not fail an
// analysis run that has already persisted its results.
func (s *Service) publishCreated(ctx context.Context, tenant core.TenantID, runID core.ID, recs []optimize.Recommendation) {
	if s.d.Events == nil || len(recs) == 0 {
		return
	}
	events := make([]ports.Event, 0, len(recs))
	now := s.d.Clock.Now()
	for _, r := range recs {
		events = append(events, ports.Event{
			ID:            string(core.NewID("evt")),
			Type:          ports.EventRecommendationCreated,
			TenantID:      tenant,
			SubjectID:     r.ID,
			CorrelationID: string(runID),
			OccurredAt:    now,
			Payload: map[string]any{
				"rule_id":                  string(r.Finding.RuleID),
				"resource_id":              string(r.Finding.ResourceID),
				"estimated_monthly_saving": r.EstimatedMonthlySaving.Units(),
			},
		})
	}
	if err := s.d.Events.PublishBatch(ctx, events); err != nil {
		s.d.Logger.Warn("optimization: publishing recommendation-created events failed", "error", err)
	}
}

// List returns a page of recommendations matching the filter.
func (s *Service) List(ctx context.Context, tenant core.TenantID, f ports.RecommendationFilter, opts ports.ListOptions) (ports.Page[optimize.Recommendation], error) {
	return s.d.Recommendations.List(ctx, tenant, f, opts)
}

// Get returns one recommendation by ID.
func (s *Service) Get(ctx context.Context, tenant core.TenantID, id core.ID) (optimize.Recommendation, error) {
	return s.d.Recommendations.Get(ctx, tenant, id)
}

// Summary returns the dashboard roll-up.
func (s *Service) Summary(ctx context.Context, tenant core.TenantID) (ports.RecommendationSummary, error) {
	return s.d.Recommendations.Summary(ctx, tenant)
}

// Dismiss marks a recommendation dismissed with a reviewer-supplied reason.
func (s *Service) Dismiss(ctx context.Context, tenant core.TenantID, id core.ID, reason, actor string) error {
	return s.d.Recommendations.UpdateStatus(ctx, tenant, id, optimize.StatusDismissed, dismissReason(reason, actor), actor)
}

func dismissReason(reason, actor string) string {
	if actor == "" {
		return reason
	}
	return fmt.Sprintf("%s (by %s)", reason, actor)
}

// Snooze defers a recommendation until a future time. RecommendationRepository
// has no dedicated snooze method — UpdateStatus carries no "until" — so this
// reads the recommendation, sets its status and SnoozedUntil together, and
// writes it back with a single Update, keeping both fields consistent.
func (s *Service) Snooze(ctx context.Context, tenant core.TenantID, id core.ID, until time.Time, reason, actor string) error {
	rec, err := s.d.Recommendations.Get(ctx, tenant, id)
	if err != nil {
		return err
	}
	rec.Status = optimize.StatusSnoozed
	rec.StatusReason = dismissReason(reason, actor)
	rec.SnoozedUntil = &until
	rec.UpdatedAt = s.d.Clock.Now()
	return s.d.Recommendations.Update(ctx, rec)
}

// Explain returns the full reasoning packet for one recommendation.
func (s *Service) Explain(ctx context.Context, tenant core.TenantID, id core.ID) (ports.RecommendationExplanation, error) {
	rec, err := s.d.Recommendations.Get(ctx, tenant, id)
	if err != nil {
		return ports.RecommendationExplanation{}, err
	}
	explanation := ports.RecommendationExplanation{
		Recommendation:   rec,
		Evidence:         rec.Finding.Evidence,
		ConfidenceInputs: rec.ConfidenceBasis,
		RiskFactors:      rec.Risk.Factors,
		BlastRadius:      rec.BlastRadius,
		Alternatives:     s.loadAlternatives(ctx, tenant, rec),
		Narrative:        rec.Narrative,
	}

	if s.d.Policies != nil && !rec.PolicyDecisionID.IsZero() {
		if d, err := s.d.Policies.GetDecision(ctx, tenant, rec.PolicyDecisionID); err == nil {
			explanation.PolicyDecision = &d
		} else if !errors.Is(err, core.ErrNotFound) {
			s.d.Logger.Warn("optimization: loading policy decision for Explain failed", "error", err)
		}
	}

	if s.d.Savings != nil {
		if calib, ok := calibrationFor(ctx, s, tenant, rec.Finding.RuleID); ok {
			explanation.Calibration = &calib
		}
		if outcomes, err := s.d.Savings.ListOutcomes(ctx, tenant, rec.Finding.RuleID, 5); err == nil {
			explanation.SimilarOutcomes = outcomes
		}
	}

	return explanation, nil
}

// loadAlternatives resolves the other members of a recommendation's conflict
// group by id. A member that no longer loads — superseded by a later analysis
// run, dismissed by a reviewer — is skipped rather than failing the whole
// explanation: the alternatives are context for a decision, and a stale id is
// not a reason to refuse to explain the recommendation the caller asked
// about.
func (s *Service) loadAlternatives(ctx context.Context, tenant core.TenantID, rec optimize.Recommendation) []optimize.Recommendation {
	if len(rec.AlternativeIDs) == 0 {
		return nil
	}
	out := make([]optimize.Recommendation, 0, len(rec.AlternativeIDs))
	for _, id := range rec.AlternativeIDs {
		alt, err := s.d.Recommendations.Get(ctx, tenant, id)
		if err != nil {
			s.d.Logger.Warn("optimization: conflict-group alternative could not be loaded",
				"recommendation", rec.ID, "alternative", id, "error", err)
			continue
		}
		out = append(out, alt)
	}
	return out
}

func calibrationFor(ctx context.Context, s *Service, tenant core.TenantID, rule optimize.RuleID) (execute.RuleCalibration, bool) {
	all, err := s.d.Savings.LoadCalibrations(ctx, tenant)
	if err != nil {
		return execute.RuleCalibration{}, false
	}
	c, ok := all[rule]
	return c, ok
}

// ListRules exposes the rule catalog with each rule's current calibration.
func (s *Service) ListRules(ctx context.Context, tenant core.TenantID) ([]ports.RuleInfo, error) {
	calib, err := s.loadCalibrations(ctx, tenant)
	if err != nil {
		s.d.Logger.Warn("optimization: loading calibrations for ListRules failed", "error", err)
		calib = nil
	}
	return s.d.Registry.RuleInfo(tenant, calib), nil
}
