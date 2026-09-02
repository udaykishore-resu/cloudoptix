package optimization

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDEC2ScheduleOffHours flags a non-production instance with a real,
// detected diurnal pattern (core.Percentiles.Seasonal, derived from actual
// telemetry, never assumed) whose active hours cover a minority of the day —
// a candidate for an automatic start/stop schedule rather than running
// around the clock.
//
// Traceability: REQ-OPT-003.
const RuleIDEC2ScheduleOffHours optimize.RuleID = "ec2-schedule-off-hours"

type ruleEC2ScheduleOffHours struct{}

func NewEC2ScheduleOffHoursRule() FullRule { return ruleEC2ScheduleOffHours{} }

func (ruleEC2ScheduleOffHours) ID() optimize.RuleID { return RuleIDEC2ScheduleOffHours }

func (ruleEC2ScheduleOffHours) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDEC2ScheduleOffHours, Name: "Idle outside business hours — scheduling candidate",
		Category: optimize.CategoryScheduling, Action: optimize.ActionScheduleShutdown,
		Description: "A non-production instance with a detected diurnal usage pattern and " +
			"near-zero activity outside a recurring active window is a scheduling candidate.",
		Kinds: []cloud.Kind{cloud.KindEC2Instance}, Enabled: true,
	}
}

func (ruleEC2ScheduleOffHours) Applies(r cloud.Resource) bool {
	return r.Kind == cloud.KindEC2Instance && r.State.Active() && !r.Environment.IsProduction() && r.Environment != core.EnvUnknown
}

func decideEC2ScheduleOffHours(ctx EvalContext, r cloud.Resource) (m ports.ResourceMetrics, offHoursFraction float64, saving core.Money, ok bool) {
	m, found := MetricsFor(ctx, r.ID)
	minCoverage := ctx.Thresholds.Float(ctx.TenantID, RuleIDEC2ScheduleOffHours, "min_coverage", 0.5)
	if !found || m.CPU == nil || !HasSufficientData(m, minCoverage, 7*24*time.Hour) {
		return
	}
	if !m.CPU.Seasonal || len(m.CPU.PeakHours) == 0 {
		return // no real diurnal pattern detected: never invent a schedule
	}
	offCPUMax := ctx.Thresholds.Float(ctx.TenantID, RuleIDEC2ScheduleOffHours, "off_hours_cpu_max", 5)
	if m.CPU.P50 > offCPUMax {
		return // active more than half the time; not an off-hours candidate
	}
	offHoursFraction = 1 - float64(len(m.CPU.PeakHours))/24.0
	minFraction := ctx.Thresholds.Float(ctx.TenantID, RuleIDEC2ScheduleOffHours, "min_off_hours_fraction", 0.35)
	if offHoursFraction < minFraction {
		return
	}
	cost := CostFor(ctx, r)
	saving = cost.Scale(offHoursFraction)
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDEC2ScheduleOffHours, "min_monthly_saving", 10)
	if !MeetsMinSaving(ctx.Spec, minSaving, saving) || ExcludedBySpec(ctx.Spec, r, optimize.ActionScheduleShutdown) {
		return
	}
	ok = true
	return
}

func (ruleEC2ScheduleOffHours) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	m, frac, saving, ok := decideEC2ScheduleOffHours(ctx, r)
	if !ok {
		return nil, nil
	}
	evidence := []optimize.Evidence{
		MetricEvidence("CPU utilization", m.CPU, m.Window, "cloudwatch"),
		ConfigEvidence("detected active hours (UTC)", fmt.Sprintf("%v", m.CPU.PeakHours)),
	}
	summary := fmt.Sprintf("%s is idle roughly %.0f%% of each day with a detected diurnal pattern", r.DisplayName(), frac*100)
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleEC2ScheduleOffHours{}, Resource: r, Severity: core.SeverityLow,
		Summary: summary, Detail: "A recurring active window was detected from telemetry; an automatic start/stop schedule can cover the rest.",
		Evidence: evidence, CurrentCost: CostFor(ctx, r), Saving: saving,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

// ScheduleFromActiveHours renders a detected set of active UTC hours as the
// single "schedule" string the schedule_shutdown executor tags an instance
// with and compares against on its idempotency check.
//
// The executor stores this string verbatim as the cloudoptix:schedule tag,
// so its shape has to be stable and self-describing rather than pretty: the
// tag is what a human reads on the instance months later, and what
// isApplied compares to decide whether the change already landed. Detected
// hours are contiguous in practice but not guaranteed to be, so an
// non-contiguous set renders as the hours it actually covers rather than as
// a range that would quietly widen the window.
//
// The rule used to emit active_hours_utc ([]int) and off_hours_fraction
// (float) instead. Neither key is read by any executor: the plan reached the
// mutate step, found no "schedule" parameter, and silently applied the
// executor's generic weekday default — so the schedule derived from this
// instance's own telemetry never reached the instance.
func ScheduleFromActiveHours(hours []int) string {
	if len(hours) == 0 {
		return ""
	}
	sorted := append([]int(nil), hours...)
	sort.Ints(sorted)
	contiguous := true
	for i := 1; i < len(sorted); i++ {
		if sorted[i] != sorted[i-1]+1 {
			contiguous = false
			break
		}
	}
	if contiguous {
		// The window runs to the end of the last active hour, so a peak set
		// of {8..17} means "running 08:00-18:00", not "08:00-17:00".
		return fmt.Sprintf("run %02d:00-%02d:00 UTC daily; stopped otherwise", sorted[0], (sorted[len(sorted)-1]+1)%24)
	}
	parts := make([]string, 0, len(sorted))
	for _, h := range sorted {
		parts = append(parts, fmt.Sprintf("%02d", h))
	}
	return "run during UTC hours " + strings.Join(parts, ",") + "; stopped otherwise"
}

func (ruleEC2ScheduleOffHours) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	m, frac, saving, ok := decideEC2ScheduleOffHours(ctx, r)
	if !ok {
		return RuleAction{Type: optimize.ActionAdvisoryOnly}
	}
	return RuleAction{
		Type: optimize.ActionScheduleShutdown,
		Parameters: map[string]any{
			// schedule is the executor's only input for this action; the
			// fraction rides along as the reviewer-facing arithmetic behind
			// the saving, not as something an executor reads.
			"schedule":           ScheduleFromActiveHours(m.CPU.PeakHours),
			"off_hours_fraction": frac,
		},
		ProposedState: optimize.StateSnapshot{MonthlyCost: CostFor(ctx, r).MustSub(saving)},
		Reversibility: optimize.ReversibilityInstant,
		Complexity:    optimize.ComplexityLow,
		Title:         fmt.Sprintf("Schedule %s to stop outside its active hours", r.DisplayName()),
		Rationale:     f.Detail,
	}
}
