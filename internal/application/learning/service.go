package learning

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/application/automation"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/execute"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// minSamples is the minimum-sample guard described in doc.go, applied by
// execute.Calibrate. It is package-level (not per-call configurable) so
// every caller gets the same evidentiary bar rather than one weakened by an
// impatient caller passing a lower number.
const minSamples = 5

// Deps is every dependency Service needs. Savings is required: it is both
// where outcomes are read from and where calibrations are written back to.
// Knowledge is optional — without it, Recalibrate still computes and
// persists calibrations; it simply has nothing to feed the RAG corpus with,
// which narrows what the copilot can cite, not what the loop can compute.
type Deps struct {
	Savings   ports.SavingsRepository
	Knowledge ports.KnowledgeStore // optional
	Clock     core.Clock
	Logger    *slog.Logger
}

// Service is the calibration and outcome-feedback loop. It structurally
// satisfies automation.Learner (Recalibrate) without either package
// importing the other's types — see automation/service.go's Learner
// interface doc comment for why that seam is deliberate.
type Service struct{ d Deps }

// This compile-time assertion is safe only because the dependency runs one
// way: automation declares the Learner interface but never imports this
// package (see automation/service.go), so learning importing automation
// here to check itself against that interface introduces no cycle.
var _ automation.Learner = (*Service)(nil)

// NewService validates the required dependency and fills in defaults for
// the optional ones.
func NewService(d Deps) (*Service, error) {
	if d.Savings == nil {
		return nil, fmt.Errorf("learning: NewService requires Savings")
	}
	if d.Clock == nil {
		d.Clock = core.SystemClock{}
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Service{d: d}, nil
}

// RecordOutcome persists one observed outcome. automation.Service already
// calls ports.SavingsRepository.SaveOutcome directly for every judged
// validation (see automation/validate.go's recordOutcome) because it is the
// call already holding both the prediction and the measurement; this method
// exists for any other caller — a backfill job, a test, a future ingestion
// path — that has an execute.Outcome in hand without going through
// automation's Validate.
func (s *Service) RecordOutcome(ctx context.Context, o execute.Outcome) error {
	if o.ID.IsZero() {
		o.ID = core.NewID("otc")
	}
	if o.ObservedAt.IsZero() {
		o.ObservedAt = s.d.Clock.Now()
	}
	return s.d.Savings.SaveOutcome(ctx, o)
}

// Recalibrate recomputes execute.RuleCalibration for every rule with at
// least one recorded outcome, persists each one, and — when a KnowledgeStore
// is configured — writes a summary document per calibrated rule into the RAG
// corpus under source "outcomes", so a copilot query like "should we trust
// the EC2 rightsizing rule in production" retrieves this tenant's own
// track record rather than only the rule's static description.
func (s *Service) Recalibrate(ctx context.Context, tenant core.TenantID) (ports.LearningResult, error) {
	all, err := s.d.Savings.ListOutcomes(ctx, tenant, "", 0)
	if err != nil {
		return ports.LearningResult{}, fmt.Errorf("learning: loading outcomes: %w", err)
	}
	byRule := map[optimize.RuleID][]execute.Outcome{}
	for _, o := range all {
		byRule[o.RuleID] = append(byRule[o.RuleID], o)
	}

	now := s.d.Clock.Now()
	result := ports.LearningResult{OutcomesConsidered: len(all), Calibrations: map[optimize.RuleID]execute.RuleCalibration{}}
	var accuracySum float64
	accuracyCount := 0
	var docs []ports.Document

	for ruleID, outcomes := range byRule {
		calib := execute.Calibrate(ruleID, tenant, outcomes, minSamples)
		if err := s.d.Savings.SaveCalibration(ctx, calib); err != nil {
			s.d.Logger.Warn("learning: saving calibration failed", "rule", ruleID, "error", err)
			continue
		}
		result.Calibrations[ruleID] = calib
		result.RulesCalibrated++
		if calib.Samples >= minSamples {
			accuracySum += calib.MeanSavingRatio
			accuracyCount++
		}
		docs = append(docs, calibrationDocument(tenant, calib, outcomes, now))
	}
	if accuracyCount > 0 {
		result.MeanAccuracy = accuracySum / float64(accuracyCount)
	}

	if s.d.Knowledge != nil && len(docs) > 0 {
		if err := s.d.Knowledge.Index(ctx, docs); err != nil {
			// The calibrations themselves are already saved; a RAG-indexing
			// failure narrows what the copilot can cite, it does not undo
			// the calibration pass, so it is logged rather than returned.
			s.d.Logger.Warn("learning: indexing calibration outcomes into the knowledge store failed", "tenant", tenant, "error", err)
		}
	}

	return result, nil
}

// calibrationDocument renders one rule's calibration and its most recent
// outcomes into a short, human-readable RAG document. The id is stable per
// tenant and rule (not per calibration run), so a re-index simply replaces
// the previous summary for that rule rather than accumulating a new
// document every time Recalibrate runs.
func calibrationDocument(tenant core.TenantID, calib execute.RuleCalibration, outcomes []execute.Outcome, now time.Time) ports.Document {
	successes, rollbacks := 0, 0
	for _, o := range outcomes {
		if o.Verdict == execute.VerdictSuccess {
			successes++
		}
		if o.RolledBack {
			rollbacks++
		}
	}
	content := fmt.Sprintf(
		"Rule %s has %d recorded outcome(s) for this tenant: %d succeeded, %d were rolled back. "+
			"Success rate %.0f%%, rollback rate %.0f%%. Mean saving ratio (actual/predicted) %.2f. "+
			"Confidence multiplier %.2f, saving multiplier %.2f applied to future recommendations from this rule.",
		calib.RuleID, calib.Samples, successes, rollbacks,
		calib.SuccessRate*100, calib.RollbackRate*100, calib.MeanSavingRatio,
		calib.ConfidenceMultiplier, calib.SavingMultiplier,
	)
	return ports.Document{
		ID:       fmt.Sprintf("outcome-calibration:%s:%s", tenant, calib.RuleID),
		TenantID: tenant, Source: "outcomes",
		Title:   fmt.Sprintf("Track record: %s", calib.RuleID),
		Content: content,
		Metadata: map[string]string{
			"rule_id": string(calib.RuleID), "samples": fmt.Sprintf("%d", calib.Samples),
		},
		UpdatedAt: now,
	}
}
