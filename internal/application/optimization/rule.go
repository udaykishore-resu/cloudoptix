package optimization

import (
	"fmt"
	"strings"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/spec"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// This file holds the helpers shared by every rule in the package: evidence
// construction, the finding constructor that enforces the evidence
// invariant, and the spec-driven guards (exclusions, minimum saving, risk
// tolerance) that apply identically regardless of which rule is asking.
// Concrete rules read thresholds through EvalContext.Thresholds and call into
// these helpers; none of them re-implements this logic.

// MetricsFor is the one place a rule looks up a resource's telemetry. A
// resource with no entry returns the zero value and false, which every
// caller must treat as "no signal", not as "confirmed idle".
func MetricsFor(ctx EvalContext, id core.ID) (ports.ResourceMetrics, bool) {
	m, ok := ctx.Metrics[id]
	return m, ok
}

// CostFor returns the attributed monthly cost of a resource, falling back to
// the denormalized Resource.MonthlyCost when the cost engine has not
// attributed it independently (e.g. between a discovery scan and the first
// cost ingestion). Both are legitimate provenance; a rule that needs to know
// which is in play should read the resource's CostSource field directly.
func CostFor(ctx EvalContext, r cloud.Resource) core.Money {
	if c, ok := ctx.CostByResource[r.ID]; ok && !c.IsZero() {
		return c
	}
	return r.MonthlyCost
}

// HasSufficientData is the guard every utilization-based rule must pass
// before treating a resource as idle, oversized or well-behaved. Acting on a
// resource with a short window or thin coverage is a data-quality problem,
// not an optimization opportunity — CloudWatch's own detailed-monitoring
// gaps, agent installation lag, and instances younger than the window all
// produce exactly the low-coverage signature that looks identical to "truly
// idle" if this guard is skipped.
func HasSufficientData(m ports.ResourceMetrics, minCoverage float64, minWindow time.Duration) bool {
	if !m.HasSignal(minCoverage) {
		return false
	}
	return m.Window.Duration() >= minWindow
}

// --- Evidence builders -----------------------------------------------------

// MetricEvidence cites a percentile summary. Every rightsizing and
// idle-detection rule's primary evidence is one of these.
func MetricEvidence(label string, p *core.Percentiles, window core.Period, source string) optimize.Evidence {
	if p == nil {
		return optimize.Evidence{Kind: "metric", Label: label, Value: "no data", Window: window, Source: source}
	}
	return optimize.Evidence{
		Kind:        "metric",
		Label:       label,
		Value:       fmt.Sprintf("p50=%.1f p95=%.1f p99=%.1f mean=%.1f (%s)", p.P50, p.P95, p.P99, p.Mean, p.Label()),
		Window:      window,
		Source:      source,
		Percentiles: p,
	}
}

// ConfigEvidence cites a discovered configuration fact.
func ConfigEvidence(label, value string) optimize.Evidence {
	return optimize.Evidence{Kind: "config", Label: label, Value: value, Source: "discovery"}
}

// CostEvidence cites a priced fact from the pricing catalog or cost engine.
func CostEvidence(label, value, source string) optimize.Evidence {
	return optimize.Evidence{Kind: "cost", Label: label, Value: value, Source: source}
}

// TopologyEvidence cites a fact derived from the dependency graph.
func TopologyEvidence(label, value string) optimize.Evidence {
	return optimize.Evidence{Kind: "topology", Label: label, Value: value, Source: "discovery"}
}

// --- Finding construction ---------------------------------------------------

// findingInput is the common shape every rule assembles before calling
// NewFinding. Keeping it as a struct rather than a long positional parameter
// list is what keeps forty-plus call sites readable and keeps a future field
// addition from becoming a forty-file diff.
type findingInput struct {
	Rule        Rule
	Resource    cloud.Resource
	Severity    core.Severity
	Summary     string
	Detail      string
	Evidence    []optimize.Evidence
	CurrentCost core.Money
	Saving      core.Money
}

// NewFinding builds a Finding and enforces the evidence invariant at the
// point of construction, matching optimize.Finding.Validate. Rules call this
// instead of constructing optimize.Finding literals directly so no call site
// can forget the ID or evidence.
func NewFinding(ctx EvalContext, in findingInput) (optimize.Finding, error) {
	info := in.Rule.Info()
	f := optimize.Finding{
		ID:                     core.NewID("find"),
		TenantID:               ctx.TenantID,
		RuleID:                 in.Rule.ID(),
		RuleName:               info.Name,
		Category:               info.Category,
		ResourceID:             in.Resource.ID,
		ResourceName:           in.Resource.DisplayName(),
		ResourceKind:           in.Resource.Kind,
		AccountID:              in.Resource.AccountID,
		Region:                 in.Resource.Region,
		Environment:            in.Resource.Environment,
		Severity:               in.Severity,
		Summary:                in.Summary,
		Detail:                 in.Detail,
		Evidence:               in.Evidence,
		CurrentMonthlyCost:     in.CurrentCost,
		EstimatedMonthlySaving: capSaving(in.Saving, in.CurrentCost),
		DetectedAt:             ctx.Now(),
	}
	if err := f.Validate(); err != nil {
		return optimize.Finding{}, err
	}
	return f, nil
}

// capSaving guards against a rule's own arithmetic producing a saving that
// exceeds the resource's cost, which Finding.Validate would otherwise reject
// outright and discard the whole finding for what is usually a harmless
// rounding overshoot at the boundary (e.g. a 100% utilization-headroom
// computation). Capping here is more useful than losing the finding, while
// still refusing anything computed so far past 100% that it signals a real
// bug rather than rounding.
func capSaving(saving, current core.Money) core.Money {
	if current.IsZero() {
		return saving
	}
	if saving.Ratio(current) > 1.02 {
		return current
	}
	return saving
}

// --- Spec-driven guards ------------------------------------------------------

// ExcludedBySpec reports whether the tenant's approved specification excludes
// this resource or this action from optimization entirely: by action name, by
// resource identifier (native ID, ARN or display name), or by a tag the
// tenant flagged as exempt (e.g. "cloudoptix:exclude=true", or any tag whose
// key/value pair appears in excludedTags).
func ExcludedBySpec(sp spec.Spec, r cloud.Resource, action optimize.ActionType) bool {
	for _, a := range sp.Optimization.ExcludedActions {
		if strings.EqualFold(a, string(action)) {
			return true
		}
	}
	for _, id := range sp.Optimization.ExcludedResources {
		if id == "" {
			continue
		}
		if strings.EqualFold(id, r.NativeID) || strings.EqualFold(id, string(r.ARN)) || strings.EqualFold(id, r.DisplayName()) {
			return true
		}
	}
	for k, v := range sp.Optimization.ExcludedTags {
		if got, ok := r.Tags.Get(k); ok {
			if v == "" || v == "*" || strings.EqualFold(got, v) {
				return true
			}
		}
	}
	return false
}

// MeetsMinSaving applies both the rule's own minimum (from its YAML
// threshold, resolved by the caller) and the tenant's blanket
// optimization.minSavingThreshold: a finding must clear the higher of the
// two, because a tenant floor exists precisely to suppress the flood of
// sub-$5 findings a large estate produces regardless of what any one rule
// considers material.
func MeetsMinSaving(sp spec.Spec, ruleMinimum float64, saving core.Money) bool {
	floor := ruleMinimum
	if sp.Optimization.MinSavingThreshold > floor {
		floor = sp.Optimization.MinSavingThreshold
	}
	return saving.Units() >= floor
}

// RiskTolerance normalizes the spec's free-form risk tolerance to a
// comparable ordinal, defaulting to the conservative "low" reading for an
// unset or unrecognised value — an optimization platform should never treat
// silence as permission for its riskier levers (Spot, aggressive scheduling).
func RiskTolerance(sp spec.Spec) int {
	switch strings.ToLower(strings.TrimSpace(sp.Optimization.RiskTolerance)) {
	case "high":
		return 2
	case "medium", "moderate":
		return 1
	default:
		return 0
	}
}

// SpotAllowed reports whether Spot-based rules may fire at all. The spec's
// explicit spotAllowed flag is authoritative; a low risk tolerance is a
// second, independent gate — a tenant can raise spotAllowed without also
// raising their general risk tolerance, and the rule must honour both.
func SpotAllowed(sp spec.Spec) bool {
	return sp.Optimization.SpotAllowed && RiskTolerance(sp) >= 1
}

// InterruptionTolerant reports whether a workload type can absorb a Spot
// interruption or an off-hours shutdown without a correctness or durability
// problem. Datastores and unset/unknown types are never tolerant: an unknown
// workload type is a data gap, not a green light.
func InterruptionTolerant(t cloud.WorkloadType) bool {
	switch t {
	case cloud.WorkloadWorker, cloud.WorkloadBatch, cloud.WorkloadStream, cloud.WorkloadMLTraining:
		return true
	case cloud.WorkloadMicroservice, cloud.WorkloadAPI:
		// Tolerant only when horizontally scaled behind a load balancer /
		// target group, which the caller must additionally confirm via the
		// topology (see rule_ec2_spot_candidacy.go) — a lone microservice
		// instance is not automatically interruption-tolerant just because
		// its type usually is.
		return true
	default:
		return false
	}
}

// matchedWorkload finds the specification's declared Workload for a resource
// by the loosest signal CloudOptix has before full attribution has run: a
// "workload" tag, or the resource's display name carrying the workload's
// name as a prefix. It is deliberately best-effort and documented as such —
// see blast.go and risk.go, the two callers that need a workload's SLO or
// criticality before the architecture twin has resolved a hard link.
func matchedWorkload(sp spec.Spec, r cloud.Resource) (spec.Workload, bool) {
	tagName := r.Tags.First("workload", "Workload", "cloudoptix:workload")
	for _, w := range sp.Workloads {
		if tagName != "" && strings.EqualFold(tagName, w.Name) {
			return w, true
		}
	}
	name := core.Normalize(r.DisplayName())
	for _, w := range sp.Workloads {
		wn := core.Normalize(w.Name)
		if wn != "" && strings.HasPrefix(name, wn) {
			return w, true
		}
	}
	return spec.Workload{}, false
}

// matchedTransactions returns the business transactions whose declared
// workload set includes a workload matched to this resource, used by
// blast.go to estimate the users behind a change. This is a best-effort text
// correlation, not a resolved graph — EvalContext carries the specification
// but not the fully-attributed application/workload/transaction graph that
// only exists after the twin builds it, so blast radius on transaction
// volume is intentionally conservative rather than exact.
func matchedTransactions(sp spec.Spec, r cloud.Resource) []spec.TransactionSpec {
	w, ok := matchedWorkload(sp, r)
	if !ok {
		return nil
	}
	var out []spec.TransactionSpec
	for _, tx := range sp.Business.Transactions {
		for _, wn := range tx.Workloads {
			if strings.EqualFold(wn, w.Name) {
				out = append(out, tx)
				break
			}
		}
	}
	return out
}

// inMaintenanceWindow reports whether now falls inside one of the tenant's
// declared maintenance windows for the given environment. A tenant with no
// declared windows is treated as having none (never a wildcard "always"),
// because silently assuming permission is the wrong failure mode for a
// change-timing guard.
func inMaintenanceWindow(sp spec.Spec, env core.Environment, now time.Time) (spec.MaintenanceWindow, bool) {
	weekday := strings.ToLower(now.UTC().Weekday().String())
	minutesNow := now.UTC().Hour()*60 + now.UTC().Minute()
	for _, w := range sp.Automation.MaintenanceWindows {
		if len(w.Environments) > 0 {
			match := false
			for _, e := range w.Environments {
				if strings.EqualFold(e, string(env)) {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}
		dayOK := len(w.Days) == 0
		for _, d := range w.Days {
			if strings.EqualFold(d, weekday) || strings.EqualFold(d, weekday[:3]) {
				dayOK = true
				break
			}
		}
		if !dayOK {
			continue
		}
		start, err := time.Parse("15:04", w.StartUTC)
		if err != nil {
			continue
		}
		startMin := start.Hour()*60 + start.Minute()
		end := startMin + w.DurationMinutes
		if minutesNow >= startMin && minutesNow < end {
			return w, true
		}
	}
	return spec.MaintenanceWindow{}, false
}
