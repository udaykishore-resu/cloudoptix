package optimization

import (
	"fmt"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDRDSIdle flags a primary database with near-zero P99 connection count
// over a full, well-covered observation window: nothing is talking to it.
//
// Traceability: REQ-OPT-005.
const RuleIDRDSIdle optimize.RuleID = "rds-idle-database"

type ruleRDSIdle struct{}

func NewRDSIdleRule() FullRule { return ruleRDSIdle{} }

func (ruleRDSIdle) ID() optimize.RuleID { return RuleIDRDSIdle }

func (ruleRDSIdle) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDRDSIdle, Name: "Idle database — no connections", Category: optimize.CategoryWaste,
		Action:      optimize.ActionStopRDS,
		Description: "A database with near-zero P99 connections over a full observation window is not serving traffic.",
		Kinds:       []cloud.Kind{cloud.KindRDSInstance}, Enabled: true,
	}
}

func (ruleRDSIdle) Applies(r cloud.Resource) bool {
	return r.Kind == cloud.KindRDSInstance && r.State.Active() && !r.AttrBool("is_read_replica", false)
}

func decideRDSIdle(ctx EvalContext, r cloud.Resource) (m ports.ResourceMetrics, cost core.Money, ok bool) {
	m, found := MetricsFor(ctx, r.ID)
	minCoverage := ctx.Thresholds.Float(ctx.TenantID, RuleIDRDSIdle, "min_coverage", 0.5)
	minWindow := ctx.Thresholds.Duration(ctx.TenantID, RuleIDRDSIdle, "min_window_hours", time.Hour, 168*time.Hour)
	if !found || m.Connections == nil || !HasSufficientData(m, minCoverage, minWindow) {
		return
	}
	maxConn := ctx.Thresholds.Float(ctx.TenantID, RuleIDRDSIdle, "max_connections_p99", 1)
	if m.Connections.P99 > maxConn {
		return
	}
	cost = CostFor(ctx, r)
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDRDSIdle, "min_monthly_saving", 15)
	if !MeetsMinSaving(ctx.Spec, minSaving, cost) || ExcludedBySpec(ctx.Spec, r, optimize.ActionStopRDS) {
		return ports.ResourceMetrics{}, core.Money{}, false
	}
	return m, cost, true
}

func (ruleRDSIdle) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	m, cost, ok := decideRDSIdle(ctx, r)
	if !ok {
		return nil, nil
	}
	evidence := []optimize.Evidence{MetricEvidence("connection count", m.Connections, m.Window, "cloudwatch")}
	summary := fmt.Sprintf("%s has near-zero connections (P99 %.1f) over %.0f days", r.DisplayName(), m.Connections.P99, m.Window.Days())
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleRDSIdle{}, Resource: r, Severity: core.SeverityMedium,
		Summary: summary, Detail: "No sustained client connections were observed at any percentile.",
		Evidence: evidence, CurrentCost: cost, Saving: cost,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleRDSIdle) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	return RuleAction{
		Type:          optimize.ActionStopRDS,
		Parameters:    map[string]any{"db_instance_id": r.NativeID},
		ProposedState: optimize.StateSnapshot{MonthlyCost: core.ZeroUSD()},
		Reversibility: optimize.ReversibilityFast,
		Complexity:    optimize.ComplexityLow,
		Title:         fmt.Sprintf("Stop idle database %s", r.DisplayName()),
		Rationale:     f.Detail,
	}
}
