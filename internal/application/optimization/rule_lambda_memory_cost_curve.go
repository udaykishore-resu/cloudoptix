package optimization

import (
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDLambdaMemoryCostCurve walks the Lambda memory ladder pricing the true
// GB-second cost at each step, rather than assuming "less memory is always
// cheaper" (wrong whenever the function is CPU-bound enough that duration
// rises faster than memory falls) or "more memory is always cheaper" (wrong
// once the function stops being CPU-bound and extra memory only adds cost
// with no offsetting duration drop).
//
// Lambda telemetry gives one observed (memoryMB, durationMs) point, not a
// full duration-vs-memory curve, so predicting duration at a candidate
// memory level requires a model. LambdaDurationAtMemory splits the observed
// duration into a memory-elastic portion (assumed to scale inversely with
// memory, modelling CPU-bound work that speeds up with more vCPU share) and
// a fixed floor portion (modelling I/O waits, cold-start overhead and other
// work that does not get faster with more memory). This is an explicit,
// documented engineering assumption about workload behaviour — not a
// fabricated price; every dollar figure built on top of it still comes from
// ports.PricingCatalog.
//
// Traceability: REQ-OPT-008.
const RuleIDLambdaMemoryCostCurve optimize.RuleID = "lambda-memory-cost-curve"

// lambdaMinMemoryMB and lambdaMaxMemoryMB bound the AWS Lambda memory range.
const (
	lambdaMinMemoryMB = 128
	lambdaMaxMemoryMB = 10240
)

// LambdaDurationAtMemory predicts a function's average duration at
// candidateMB given it was observed to run at currentDurationMs when
// configured with currentMB.
//
// nonParallelFloorFraction is the fraction of the observed duration assumed
// to be non-parallelizable (I/O waits, fixed overhead); the remainder is
// assumed to scale as currentMB/candidateMB, matching how Lambda allocates
// vCPU share proportional to configured memory. The floor is a lower bound
// on predicted duration at any memory level: duration never (in this model)
// falls below it, however much memory is added, which is what lets more
// memory eventually stop paying for itself.
func LambdaDurationAtMemory(currentMB int, currentDurationMs float64, candidateMB int, nonParallelFloorFraction float64) float64 {
	if currentMB <= 0 || candidateMB <= 0 || currentDurationMs <= 0 {
		return currentDurationMs
	}
	nonParallelFloorFraction = clamp01(nonParallelFloorFraction)
	floorMs := currentDurationMs * nonParallelFloorFraction
	parallelMs := currentDurationMs - floorMs
	return floorMs + parallelMs*(float64(currentMB)/float64(candidateMB))
}

// LambdaMonthlyCost prices one memory configuration exactly from the
// catalog: durationMs (from LambdaDurationAtMemory or an observed value),
// the configured memory, the invocation volume, and the catalog's
// GB-second and per-request prices are all that go into it.
func LambdaMonthlyCost(memoryMB int, durationMs float64, invocationsPerMonth float64, gbSecondPrice, requestPrice core.Money) core.Money {
	gbSeconds := (float64(memoryMB) / 1024.0) * (durationMs / 1000.0)
	computeCost := gbSecondPrice.Scale(gbSeconds * invocationsPerMonth)
	// requestPrice is quoted per 1,000 requests (see pricing.ServicePrice's
	// doc comment on the lambda/request dimension).
	requestCost := requestPrice.Scale(invocationsPerMonth / 1000.0)
	return computeCost.MustAdd(requestCost)
}

// LambdaOptimalMemory walks the memory ladder from lambdaMinMemoryMB to
// lambdaMaxMemoryMB in stepMB increments and returns the cheapest
// configuration found, using LambdaDurationAtMemory to predict duration at
// each candidate. It always includes currentMB itself as a candidate, so the
// result is never worse than doing nothing.
func LambdaOptimalMemory(currentMB int, currentDurationMs, nonParallelFloorFraction, invocationsPerMonth float64, gbSecondPrice, requestPrice core.Money, stepMB int) (bestMB int, bestCost core.Money, bestDurationMs float64) {
	if stepMB <= 0 {
		stepMB = 64
	}
	bestMB = currentMB
	bestDurationMs = currentDurationMs
	bestCost = LambdaMonthlyCost(currentMB, currentDurationMs, invocationsPerMonth, gbSecondPrice, requestPrice)
	for mb := lambdaMinMemoryMB; mb <= lambdaMaxMemoryMB; mb += stepMB {
		if mb == currentMB {
			continue
		}
		d := LambdaDurationAtMemory(currentMB, currentDurationMs, mb, nonParallelFloorFraction)
		c := LambdaMonthlyCost(mb, d, invocationsPerMonth, gbSecondPrice, requestPrice)
		if c.LessThan(bestCost) {
			bestMB, bestCost, bestDurationMs = mb, c, d
		}
	}
	return bestMB, bestCost, bestDurationMs
}

type ruleLambdaMemoryCostCurve struct{}

func NewLambdaMemoryCostCurveRule() FullRule { return ruleLambdaMemoryCostCurve{} }

func (ruleLambdaMemoryCostCurve) ID() optimize.RuleID { return RuleIDLambdaMemoryCostCurve }

func (ruleLambdaMemoryCostCurve) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDLambdaMemoryCostCurve, Name: "Lambda memory over/under-provisioning (cost-curve optimal)",
		Category: optimize.CategoryRightsizing, Action: optimize.ActionResizeLambdaMemory,
		Description: "Walks the Lambda memory ladder pricing true GB-second cost at each step; never recommends a " +
			"memory change whose predicted duration curve would raise total cost.",
		Kinds: []cloud.Kind{cloud.KindLambdaFunction}, Enabled: true,
	}
}

func (ruleLambdaMemoryCostCurve) Applies(r cloud.Resource) bool {
	return r.Kind == cloud.KindLambdaFunction && r.State.Active() && r.Capacity.MemoryMB > 0
}

func lambdaGBSecondPrice(ctx EvalContext, r cloud.Resource) (core.Money, bool) {
	dimension := "gb_second"
	if r.Attr("architecture", "") == "arm64" {
		dimension = "arm_gb_second"
	}
	return ctx.Pricing.ServicePrice(r.Region, "lambda", dimension)
}

func decideLambdaMemoryCostCurve(ctx EvalContext, r cloud.Resource) (bestMB int, currentCost, bestCost, saving core.Money, ok bool) {
	durationMs := parseFloatAttr(r.Attr("avg_duration_ms", ""), -1)
	invocations := parseFloatAttr(r.Attr("invocations_per_month", ""), -1)
	if durationMs <= 0 || invocations < 0 {
		return
	}
	gbSecondPrice, ok1 := lambdaGBSecondPrice(ctx, r)
	requestPrice, ok2 := ctx.Pricing.ServicePrice(r.Region, "lambda", "request")
	if !ok1 || !ok2 {
		return
	}
	currentMB := r.Capacity.MemoryMB
	currentCost = LambdaMonthlyCost(currentMB, durationMs, invocations, gbSecondPrice, requestPrice)
	floorFraction := ctx.Thresholds.Float(ctx.TenantID, RuleIDLambdaMemoryCostCurve, "nonparallel_floor_fraction", 0.15)
	stepMB := ctx.Thresholds.Int(ctx.TenantID, RuleIDLambdaMemoryCostCurve, "memory_step_mb", 64)
	bestMB, bestCost, _ = LambdaOptimalMemory(currentMB, durationMs, floorFraction, invocations, gbSecondPrice, requestPrice, stepMB)
	if bestMB == currentMB || !bestCost.LessThan(currentCost) {
		return 0, currentCost, core.Money{}, core.Money{}, false
	}
	saving = currentCost.MustSub(bestCost)
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDLambdaMemoryCostCurve, "min_monthly_saving", 1)
	if !MeetsMinSaving(ctx.Spec, minSaving, saving) || ExcludedBySpec(ctx.Spec, r, optimize.ActionResizeLambdaMemory) {
		return 0, currentCost, core.Money{}, core.Money{}, false
	}
	return bestMB, currentCost, bestCost, saving, true
}

func (ruleLambdaMemoryCostCurve) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	bestMB, currentCost, bestCost, saving, ok := decideLambdaMemoryCostCurve(ctx, r)
	if !ok {
		return nil, nil
	}
	direction := "increasing"
	if bestMB < r.Capacity.MemoryMB {
		direction = "decreasing"
	}
	evidence := []optimize.Evidence{
		ConfigEvidence("current memory", fmt.Sprintf("%d MB", r.Capacity.MemoryMB)),
		ConfigEvidence("observed avg duration", r.Attr("avg_duration_ms", "")+"ms"),
		CostEvidence("current vs cost-curve-optimal monthly cost", fmt.Sprintf("%s vs %s", currentCost.Format(), bestCost.Format()), "pricing_catalog"),
	}
	summary := fmt.Sprintf("%s: %s memory to %d MB minimizes GB-second cost", r.DisplayName(), direction, bestMB)
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleLambdaMemoryCostCurve{}, Resource: r, Severity: core.SeverityInfo,
		Summary: summary, Detail: "Duration at the candidate memory is predicted from an elastic-plus-floor model of the observed duration, not assumed constant.",
		Evidence: evidence, CurrentCost: currentCost, Saving: saving,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleLambdaMemoryCostCurve) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	bestMB, _, bestCost, _, ok := decideLambdaMemoryCostCurve(ctx, r)
	if !ok {
		return RuleAction{Type: optimize.ActionAdvisoryOnly}
	}
	return RuleAction{
		Type:          optimize.ActionResizeLambdaMemory,
		Parameters:    map[string]any{"function_id": r.NativeID, "memory_mb": bestMB},
		ProposedState: optimize.StateSnapshot{MemoryMB: bestMB, MonthlyCost: bestCost},
		Reversibility: optimize.ReversibilityInstant,
		Complexity:    optimize.ComplexityTrivial,
		Title:         fmt.Sprintf("Set %s's memory to %d MB", r.DisplayName(), bestMB),
		Rationale:     f.Detail,
	}
}
