package optimization

import (
	"fmt"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDRDSUnnecessaryReplica flags a read replica whose observed
// connection/read load is near zero over a full window: it is paying full
// instance price to offload traffic that never arrives.
//
// Traceability: REQ-OPT-005.
const RuleIDRDSUnnecessaryReplica optimize.RuleID = "rds-unnecessary-read-replica"

type ruleRDSUnnecessaryReplica struct{}

func NewRDSUnnecessaryReplicaRule() FullRule { return ruleRDSUnnecessaryReplica{} }

func (ruleRDSUnnecessaryReplica) ID() optimize.RuleID { return RuleIDRDSUnnecessaryReplica }

func (ruleRDSUnnecessaryReplica) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDRDSUnnecessaryReplica, Name: "Read replica with no read traffic",
		Category: optimize.CategoryWaste, Action: optimize.ActionRemoveRDSReplica,
		Description: "A read replica with near-zero observed connections over a full window is offloading nothing.",
		Kinds:       []cloud.Kind{cloud.KindRDSInstance}, Enabled: true,
	}
}

func (ruleRDSUnnecessaryReplica) Applies(r cloud.Resource) bool {
	return r.Kind == cloud.KindRDSInstance && r.State.Active() && r.AttrBool("is_read_replica", false)
}

func decideRDSUnnecessaryReplica(ctx EvalContext, r cloud.Resource) (m ports.ResourceMetrics, cost core.Money, ok bool) {
	m, found := MetricsFor(ctx, r.ID)
	minCoverage := ctx.Thresholds.Float(ctx.TenantID, RuleIDRDSUnnecessaryReplica, "min_coverage", 0.5)
	minWindow := ctx.Thresholds.Duration(ctx.TenantID, RuleIDRDSUnnecessaryReplica, "min_window_hours", time.Hour, 168*time.Hour)
	if !found || m.Connections == nil || !HasSufficientData(m, minCoverage, minWindow) {
		return
	}
	maxConn := ctx.Thresholds.Float(ctx.TenantID, RuleIDRDSUnnecessaryReplica, "max_connections_p99", 1)
	if m.Connections.P99 > maxConn {
		return
	}
	cost = CostFor(ctx, r)
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDRDSUnnecessaryReplica, "min_monthly_saving", 20)
	if !MeetsMinSaving(ctx.Spec, minSaving, cost) || ExcludedBySpec(ctx.Spec, r, optimize.ActionRemoveRDSReplica) {
		return ports.ResourceMetrics{}, core.Money{}, false
	}
	return m, cost, true
}

func (ruleRDSUnnecessaryReplica) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	m, cost, ok := decideRDSUnnecessaryReplica(ctx, r)
	if !ok {
		return nil, nil
	}
	evidence := []optimize.Evidence{
		MetricEvidence("connection count", m.Connections, m.Window, "cloudwatch"),
		ConfigEvidence("primary", r.Attr("primary_id", "")),
	}
	summary := fmt.Sprintf("Read replica %s has near-zero connections over %.0f days", r.DisplayName(), m.Window.Days())
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleRDSUnnecessaryReplica{}, Resource: r, Severity: core.SeverityMedium,
		Summary: summary, Detail: "No sustained read traffic reached this replica at any observed percentile.",
		Evidence: evidence, CurrentCost: cost, Saving: cost,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleRDSUnnecessaryReplica) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	return RuleAction{
		Type:          optimize.ActionRemoveRDSReplica,
		Parameters:    map[string]any{"db_instance_id": r.NativeID},
		ProposedState: optimize.StateSnapshot{MonthlyCost: core.ZeroUSD()},
		Reversibility: optimize.ReversibilityNone,
		Complexity:    optimize.ComplexityLow,
		Title:         fmt.Sprintf("Remove unused read replica %s", r.DisplayName()),
		Rationale:     f.Detail,
	}
}
