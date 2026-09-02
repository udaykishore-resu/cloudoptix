package automation

import (
	"context"
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/execute"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// minCalibrationSamples is the minimum-sample guard: a rule with fewer
// observed outcomes than this keeps its neutral 1.0 multipliers regardless
// of how good or bad those few outcomes looked. See execute.Calibrate's own
// doc comment — shrinking a rule's future estimates on two data points is
// noise amplification, not learning.
const minCalibrationSamples = 5

// Learn recomputes rule calibrations from every outcome recorded since the
// last run. When a Learner is wired in (internal/application/learning,
// structurally satisfying the Learner interface declared in service.go) it
// delegates to it, which additionally feeds outcomes back into the RAG
// corpus. Without one, it runs the same domain calibration
// (execute.Calibrate) directly against ports.SavingsRepository, which is
// already a required dependency of this service — Learn always does
// something real, never a stub that returns an empty result because an
// optional collaborator was not configured.
func (s *Service) Learn(ctx context.Context, tenant core.TenantID) (ports.LearningResult, error) {
	if s.d.Learner != nil {
		result, err := s.d.Learner.Recalibrate(ctx, tenant)
		if err == nil {
			return result, nil
		}
		s.d.Logger.Warn("automation: learner recalibration failed, falling back to direct calibration", "tenant", tenant, "error", err)
	}
	return s.recalibrateDirect(ctx, tenant)
}

func (s *Service) recalibrateDirect(ctx context.Context, tenant core.TenantID) (ports.LearningResult, error) {
	all, err := s.d.Savings.ListOutcomes(ctx, tenant, "", 0)
	if err != nil {
		return ports.LearningResult{}, fmt.Errorf("automation: loading outcomes for calibration: %w", err)
	}
	byRule := map[optimize.RuleID][]execute.Outcome{}
	for _, o := range all {
		byRule[o.RuleID] = append(byRule[o.RuleID], o)
	}

	result := ports.LearningResult{OutcomesConsidered: len(all), Calibrations: map[optimize.RuleID]execute.RuleCalibration{}}
	var accuracySum float64
	accuracyCount := 0
	for ruleID, outcomes := range byRule {
		calib := execute.Calibrate(ruleID, tenant, outcomes, minCalibrationSamples)
		if err := s.d.Savings.SaveCalibration(ctx, calib); err != nil {
			s.d.Logger.Warn("automation: saving calibration failed", "rule", ruleID, "error", err)
			continue
		}
		result.Calibrations[ruleID] = calib
		result.RulesCalibrated++
		if calib.Samples >= minCalibrationSamples {
			accuracySum += calib.MeanSavingRatio
			accuracyCount++
		}
	}
	if accuracyCount > 0 {
		result.MeanAccuracy = accuracySum / float64(accuracyCount)
	}
	return result, nil
}
