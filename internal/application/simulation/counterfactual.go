package simulation

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/simulate"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// scenarioOutcome is what one scenario handler produces. Counterfactual turns
// this into a full simulate.Counterfactual the same way patternResult is
// turned into a simulate.Candidate: the mechanical roll-up (cost delta,
// delta percentage, annualization, persistence) happens once, identically,
// for every scenario, so a handler only has to state the proposed state and
// its reasoning.
type scenarioOutcome struct {
	Question                                          string
	Proposed                                          simulate.StateProjection
	PerformanceDelta, ReliabilityDelta, SecurityDelta string
	Risk                                              core.RiskLevel
	Confidence                                        core.Confidence
	Assumptions                                       []simulate.Assumption
	Caveats                                           []string
	Narrative                                         string
}

// scenarioHandler answers one simulate.ScenarioType against the scoped
// inventory. Like patternBuilder, it is a pure function of its inputs — no
// I/O — so every scenario is unit-testable without a database.
type scenarioHandler func(inv *cloud.Inventory, pricing ports.PricingCatalog, region core.Region, sc simulate.Scenario) (scenarioOutcome, error)

// scenarioHandlers is the catalog of counterfactual questions CloudOptix can
// answer. ScenarioCustom is intentionally included with a handler that
// never fabricates a cost model for parameters it does not recognize — see
// handleCustom.
var scenarioHandlers = map[simulate.ScenarioType]scenarioHandler{
	simulate.ScenarioTrafficChange:      handleTrafficChange,
	simulate.ScenarioPlatformChange:     handlePlatformChange,
	simulate.ScenarioDatabaseChange:     handleDatabaseChange,
	simulate.ScenarioAddCache:           handleAddCache,
	simulate.ScenarioRemoveNAT:          handleRemoveNAT,
	simulate.ScenarioAddVPCEndpoint:     handleAddVPCEndpoint,
	simulate.ScenarioSpotAdoption:       handleSpotAdoption,
	simulate.ScenarioRegionChange:       handleRegionChange,
	simulate.ScenarioCommitmentPurchase: handleCommitmentPurchase,
	simulate.ScenarioStorageClass:       handleStorageClassChange,
	simulate.ScenarioReplicaChange:      handleReplicaChange,
	simulate.ScenarioCustom:             handleCustom,
}

// Counterfactual answers one what-if question by loading the scoped
// inventory, projecting its current state, dispatching to the scenario's
// handler for a proposed state, and computing the deltas between them.
func (s *Service) Counterfactual(ctx context.Context, tenant core.TenantID, sc simulate.Scenario) (simulate.Counterfactual, error) {
	inv, err := s.Resources.LoadInventory(ctx, tenant, counterfactualFilter(sc))
	if err != nil {
		return simulate.Counterfactual{}, err
	}
	region := dominantRegion(inv)
	current := stateProjection("current", inv)

	handler, ok := scenarioHandlers[sc.Type]
	if !ok {
		return simulate.Counterfactual{}, fmt.Errorf("simulation: unsupported scenario type %q", sc.Type)
	}
	outcome, err := handler(inv, s.Pricing, region, sc)
	if err != nil {
		return simulate.Counterfactual{}, err
	}

	costDelta := outcome.Proposed.MonthlyCost.MustSub(current.MonthlyCost)
	var costDeltaPct float64
	if !current.MonthlyCost.IsZero() {
		costDeltaPct = costDelta.Ratio(current.MonthlyCost) * 100
	}

	cf := simulate.Counterfactual{
		ID: core.NewID("cf"), TenantID: tenant, Scenario: sc, Question: outcome.Question,
		CurrentState: current, ProposedState: outcome.Proposed,
		CostDelta: costDelta, CostDeltaPct: costDeltaPct, AnnualCostDelta: costDelta.Annualized(),
		PerformanceDelta: outcome.PerformanceDelta, ReliabilityDelta: outcome.ReliabilityDelta, SecurityDelta: outcome.SecurityDelta,
		Risk: outcome.Risk, Confidence: outcome.Confidence, Assumptions: outcome.Assumptions,
		Caveats: outcome.Caveats, Narrative: outcome.Narrative, ComputedAt: s.now(),
	}
	if err := s.Store.SaveCounterfactual(ctx, cf); err != nil {
		return simulate.Counterfactual{}, err
	}
	return cf, nil
}

// counterfactualFilter scopes the inventory a scenario is asked against.
// Scenario, unlike MutationRequest, carries only a bare ScopeID with no
// scope-kind discriminator ("application" vs "workload" vs "account") — a
// port mismatch worked around the same way scopeFilter in mutation.go
// documents its own. ApplicationID is the natural default: a counterfactual
// question ("what if traffic doubles?") is normally asked about one
// application, not an arbitrary AWS account. An empty ScopeID scopes to the
// whole tenant.
func counterfactualFilter(sc simulate.Scenario) ports.ResourceFilter {
	if sc.ScopeID.IsZero() {
		return ports.ResourceFilter{}
	}
	return ports.ResourceFilter{ApplicationID: sc.ScopeID}
}

// stateProjection summarizes an inventory's current, unmodified cost as a
// StateProjection — the "current" side of every counterfactual.
func stateProjection(label string, inv *cloud.Inventory) simulate.StateProjection {
	byService := map[string]core.Money{}
	total := core.ZeroUSD()
	for _, r := range inv.All() {
		svc := r.Kind.Service()
		byService[svc] = byService[svc].MustAdd(r.MonthlyCost)
		total = total.MustAdd(r.MonthlyCost)
	}
	return simulate.StateProjection{Label: label, MonthlyCost: total, ByService: byService}
}

// addedComponent is a piece of infrastructure a scenario introduces that has
// no corresponding resource in the current inventory (a new cache cluster, a
// new VPC endpoint).
type addedComponent struct {
	name, kind, unit string
	quantity         float64
	monthlyCost      core.Money
}

// buildProposedState is the shared roll-up every scenario handler uses: it
// takes the current inventory, a set of per-resource cost overrides (the
// resource still exists but now costs something different) and a list of
// wholly new components, and produces the proposed StateProjection. Doing
// this once means every handler only has to say what changed, not
// re-implement the by-service and component bookkeeping.
func buildProposedState(label string, inv *cloud.Inventory, overrides map[core.ID]core.Money, added []addedComponent) simulate.StateProjection {
	byService := map[string]core.Money{}
	total := core.ZeroUSD()
	var components []simulate.ProjectedComponent
	for _, r := range inv.All() {
		cost := r.MonthlyCost
		if nc, ok := overrides[r.ID]; ok {
			cost = nc
			components = append(components, simulate.ProjectedComponent{
				Name: r.DisplayName(), Kind: string(r.Kind), Quantity: units(r), Unit: "resource", MonthlyCost: cost,
			})
		}
		svc := r.Kind.Service()
		byService[svc] = byService[svc].MustAdd(cost)
		total = total.MustAdd(cost)
	}
	for _, a := range added {
		byService[a.kind] = byService[a.kind].MustAdd(a.monthlyCost)
		total = total.MustAdd(a.monthlyCost)
		components = append(components, simulate.ProjectedComponent{
			Name: a.name, Kind: a.kind, Quantity: a.quantity, Unit: a.unit, MonthlyCost: a.monthlyCost,
		})
	}
	return simulate.StateProjection{Label: label, MonthlyCost: total, ByService: byService, Components: components}
}

// --- traffic-change scaling classification --------------------------------

// trafficScaling names how a resource kind's cost moves with request
// volume. The traffic-change scenario is the one the task specification
// calls out explicitly: "must not simply multiply the bill by N". Three
// different real behaviors exist in a cloud bill and conflating them is
// exactly the mistake a naive multiply makes:
//
//   - linear: usage-metered services (Lambda invocations, DynamoDB on-demand
//     RCU/WCU, API Gateway requests, CloudFront requests, SQS messages) are
//     billed per unit of traffic, so their currently-attributed cost — which
//     already reflects today's usage — scales proportionally with N.
//   - stepwise: autoscaled compute (EC2/ASG, EKS/ECS/Kubernetes workloads)
//     does not scale continuously; capacity is added in whole
//     instances/tasks, so cost moves in discrete jumps (a ceiling function),
//     not a smooth line, and can lag or overshoot the traffic multiplier.
//   - blended: NAT gateways and load balancers each carry a fixed hourly
//     fee that traffic does not move at all, plus a usage-metered
//     component (GB processed, LCU-hours) that does.
//   - fixed: everything else (VPC, security groups, KMS, secrets, log
//     storage, cluster control planes, Route53) costs the same regardless
//     of request volume.
type trafficScaling int

const (
	trafficFixed trafficScaling = iota
	trafficLinear
	trafficStepwise
	trafficBlended
)

func classifyTrafficScaling(k cloud.Kind) trafficScaling {
	switch k {
	case cloud.KindLambdaFunction, cloud.KindDynamoDBTable, cloud.KindAPIGateway,
		cloud.KindCloudFront, cloud.KindSQSQueue, cloud.KindKinesisStream, cloud.KindSNSTopic:
		return trafficLinear
	case cloud.KindEC2Instance, cloud.KindAutoScalingGroup, cloud.KindEKSNodeGroup,
		cloud.KindECSService, cloud.KindK8sWorkload, cloud.KindElastiCache:
		return trafficStepwise
	case cloud.KindNATGateway, cloud.KindALB, cloud.KindNLB:
		return trafficBlended
	default:
		return trafficFixed
	}
}

// stepwiseCost models autoscaled compute's cost under a traffic multiplier.
// It infers a per-unit cost by dividing the resource's currently-attributed
// cost by its current unit count (the pricing catalog gives per-instance-type
// rates, not per-workload rates, so this reuse of the already-attributed
// cost as the per-unit basis is the honest option available here), then
// rounds the scaled unit count UP to the next whole unit — the ceiling is
// what makes this genuinely non-linear rather than a disguised multiply: a
// 1.3x traffic increase on 3 units still needs 4 whole units, a 43% cost
// jump for a 30% traffic increase.
func stepwiseCost(r cloud.Resource, multiplier float64) core.Money {
	u := units(r)
	if u <= 0 {
		return r.MonthlyCost
	}
	perUnit := r.MonthlyCost.Div(u)
	newUnits := math.Ceil(u * multiplier)
	if newUnits < 1 {
		newUnits = 1
	}
	if r.Capacity.MaxCount > 0 && newUnits > float64(r.Capacity.MaxCount) {
		// Capped by the workload's own configured autoscaling ceiling: past
		// this point traffic keeps growing but capacity — and so cost —
		// cannot, which is itself a real signal (a capacity risk), not a
		// modelling gap.
		newUnits = float64(r.Capacity.MaxCount)
	}
	return perUnit.Scale(newUnits)
}

// blendedTrafficCost re-derives a NAT gateway's or load balancer's cost from
// the pricing catalog rather than scaling its attributed MonthlyCost,
// because the fixed hourly fee and the usage-metered component need to be
// split apart before only one of them is scaled — a split the attributed
// MonthlyCost figure does not carry. The baseline usage figures are stated,
// overridable Assumptions (see handleTrafficChange), not measurements.
func blendedTrafficCost(r cloud.Resource, pricing ports.PricingCatalog, region core.Region, multiplier, natGBBaseline, lbLCUBaseline float64) core.Money {
	switch r.Kind {
	case cloud.KindNATGateway:
		hourly, ok := pricing.ServicePrice(region, "nat_gateway", "hours")
		if !ok {
			return r.MonthlyCost
		}
		fixed := monthlyFromHourly(hourly)
		gbPrice, ok := pricing.ServicePrice(region, "nat_gateway", "gb_processed")
		if !ok {
			return fixed
		}
		return fixed.MustAdd(gbPrice.Scale(natGBBaseline * multiplier))
	case cloud.KindALB, cloud.KindNLB:
		svc := "alb"
		if r.Kind == cloud.KindNLB {
			svc = "nlb"
		}
		hourly, ok := pricing.ServicePrice(region, svc, "hours")
		if !ok {
			return r.MonthlyCost
		}
		fixed := monthlyFromHourly(hourly)
		lcuPrice, ok := pricing.ServicePrice(region, svc, "lcu_hour")
		if !ok {
			return fixed
		}
		return fixed.MustAdd(monthlyFromHourly(lcuPrice).Scale(lbLCUBaseline * multiplier))
	default:
		return r.MonthlyCost
	}
}

// handleTrafficChange is the traffic x N counterfactual. See trafficScaling
// for the classification this applies and why it is not a bill multiply.
func handleTrafficChange(inv *cloud.Inventory, pricing ports.PricingCatalog, region core.Region, sc simulate.Scenario) (scenarioOutcome, error) {
	multiplier := paramFloat(sc.Parameters, "multiplier", 2.0)
	if multiplier <= 0 {
		return scenarioOutcome{}, fmt.Errorf("simulation: traffic_change multiplier must be positive, got %g", multiplier)
	}
	natGBBaseline := paramFloat(sc.Parameters, "nat_gb_processed_baseline", 500)
	lbLCUBaseline := paramFloat(sc.Parameters, "lb_lcu_baseline", 10)

	overrides := map[core.ID]core.Money{}
	for _, r := range inv.All() {
		switch classifyTrafficScaling(r.Kind) {
		case trafficLinear:
			overrides[r.ID] = r.MonthlyCost.Scale(multiplier)
		case trafficStepwise:
			overrides[r.ID] = stepwiseCost(r, multiplier)
		case trafficBlended:
			overrides[r.ID] = blendedTrafficCost(r, pricing, region, multiplier, natGBBaseline, lbLCUBaseline)
		case trafficFixed:
			// left un-overridden: fixed costs do not move with traffic.
		}
	}

	proposed := buildProposedState(fmt.Sprintf("%.2gx traffic", multiplier), inv, overrides, nil)

	risk := core.RiskLow
	if multiplier >= 3 {
		risk = core.RiskMedium
	}
	if multiplier >= 8 {
		risk = core.RiskHigh
	}

	return scenarioOutcome{
		Question: fmt.Sprintf("What happens to cost, performance and reliability if traffic increases %.2gx?", multiplier),
		Proposed: proposed,
		PerformanceDelta: fmt.Sprintf(
			"stepwise-scaled compute (EC2/ASG/EKS/ECS/Kubernetes) adds whole instances or tasks to absorb %.2gx load and should hold latency roughly flat as long as autoscaling keeps pace; usage-metered services (Lambda, DynamoDB, API Gateway) have no headroom limit of their own but can surface cold-starts or throttling at the new volume", multiplier),
		ReliabilityDelta: "capacity that hits its configured max_count ceiling under this multiplier stops scaling and degrades instead — see caveats for which resources are affected",
		Risk:             risk,
		Confidence:       0.55,
		Assumptions: []simulate.Assumption{
			{Key: "traffic_multiplier", Label: "Traffic increase factor", Value: fmt.Sprintf("%g", multiplier), Unit: "x", Provenance: core.ProvenanceConfirmed, Note: "the scenario's own input parameter"},
			{Key: "nat_gb_processed_baseline", Label: "Baseline NAT GB processed per gateway per month", Value: fmt.Sprintf("%g", natGBBaseline), Unit: "GB/month", Provenance: core.ProvenanceInferred, Note: "not measured from VPC Flow Logs; override with an observed figure for a tighter estimate"},
			{Key: "lb_lcu_baseline", Label: "Baseline load balancer LCU-hours per month", Value: fmt.Sprintf("%g", lbLCUBaseline), Unit: "LCU-hours/month", Provenance: core.ProvenanceInferred, Note: "not measured from CloudWatch; override with an observed figure for a tighter estimate"},
		},
		Caveats: []string{
			"usage-metered services (Lambda, DynamoDB on-demand, API Gateway, CloudFront, SQS, Kinesis) scale their cost linearly with the multiplier because their attributed cost is already entirely usage-driven",
			"autoscaled compute (EC2/ASG, EKS/ECS/Kubernetes workloads) scales in whole-unit steps via a ceiling function, capped at each resource's configured max_count where one is set — traffic beyond that ceiling is a capacity risk, not just a cost one",
			"NAT gateways and load balancers keep their fixed hourly fee unchanged and scale only their usage-metered component (GB processed / LCU-hours) against the stated baseline assumptions",
			"everything else (networking primitives, KMS, secrets, log storage, cluster control planes) is treated as fixed and does not move with traffic",
		},
		Narrative: fmt.Sprintf("A %.2gx traffic increase does not multiply the bill by %.2g. Usage-metered services scale linearly with it, autoscaled compute scales in discrete steps that round up (so cost growth can outpace or lag the traffic multiplier depending on how full the current step is), fixed-fee network components barely move, and static infrastructure does not move at all.", multiplier, multiplier),
	}, nil
}

// --- scenario-parameter helpers -------------------------------------------

// paramFloat reads a numeric scenario parameter. Scenario.Parameters is
// map[string]any coming from a JSON-decoded API request in production and a
// literal Go map in tests, so it accepts every representation either path
// produces (float64, int, numeric string) rather than requiring a specific
// caller-side type.
func paramFloat(params map[string]any, key string, def float64) float64 {
	if params == nil {
		return def
	}
	v, ok := params[key]
	if !ok {
		return def
	}
	switch t := v.(type) {
	case float64:
		return t
	case float32:
		return float64(t)
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(t), 64); err == nil {
			return f
		}
	}
	return def
}

// paramString reads a string scenario parameter, falling back to def when
// absent or empty.
func paramString(params map[string]any, key, def string) string {
	if params == nil {
		return def
	}
	if s, ok := params[key].(string); ok && s != "" {
		return s
	}
	return def
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
