package optimization

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/execute"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/spec"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
	rulepack "github.com/udaykishore-resu/cloudoptix/rules"
)

// EvalContext is everything a Rule may read. It is built once per analysis
// run and passed by value to every rule/resource pair, which is what makes
// evaluation embarrassingly parallel-safe even though the current Registry
// runs it sequentially for determinism.
//
// Deliberately absent: anything mutable, anything that performs I/O, and
// anything derived from an LLM. A rule that needs a fact not on this struct
// is asking the wrong layer for it.
type EvalContext struct {
	TenantID core.TenantID

	Inventory *cloud.Inventory
	Topology  *cloud.Topology
	// Metrics is keyed by resource ID. A resource absent from this map has no
	// telemetry at all, which every rule that reasons about utilization must
	// treat identically to zero coverage — see HasSufficientData.
	Metrics map[core.ID]ports.ResourceMetrics
	// CostByResource is the attributed monthly run-rate per resource, from the
	// cost engine. A resource absent here has an unknown cost, not a zero
	// cost; rules must not treat a missing entry as "free".
	CostByResource map[core.ID]core.Money

	Pricing ports.PricingCatalog
	Spec    spec.Spec

	// Calibrations is the tenant's historical accuracy record per rule.
	// confidence.go reads it; a rule ID absent here is treated as an
	// uncalibrated rule (multiplier 1.0), not as a distrusted one.
	Calibrations map[optimize.RuleID]execute.RuleCalibration

	Thresholds ThresholdSource
	Clock      core.Clock
}

// Now is a convenience over Clock.Now(), used pervasively enough that
// spelling it out at every call site would hurt readability.
func (c EvalContext) Now() time.Time { return c.Clock.Now() }

// ThresholdSource resolves a rule's configuration, applying the tenant
// override -> YAML default -> caller-supplied fallback precedence documented
// in rules/README.md. Rule code never reads a Go constant for a tunable
// value; it always goes through this so the YAML pack is the single source
// of truth.
type ThresholdSource interface {
	Float(tenant core.TenantID, rule optimize.RuleID, key string, fallback float64) float64
	Int(tenant core.TenantID, rule optimize.RuleID, key string, fallback int) int
	Bool(tenant core.TenantID, rule optimize.RuleID, key string, fallback bool) bool
	Duration(tenant core.TenantID, rule optimize.RuleID, key string, unit time.Duration, fallback time.Duration) time.Duration
	Enabled(tenant core.TenantID, rule optimize.RuleID, shippedDefault bool) bool
}

// Rule is a deterministic detector: given the evaluation context and one
// resource, it states what is true about that resource, or nothing. It never
// decides what to do about what it found — that split is what lets Evaluate
// be tested with zero knowledge of pricing, actions or risk, and what lets
// the recommendation-shaping logic in confidence.go/blast.go/risk.go be
// shared identically across every rule.
type Rule interface {
	ID() optimize.RuleID
	Info() ports.RuleInfo
	// Applies is a cheap pre-filter (kind, purchase model, category) run
	// before Evaluate, so the registry never calls Evaluate — which may read
	// metrics and pricing — on a resource the rule could never fire on.
	Applies(r cloud.Resource) bool
	Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error)
}

// RuleAction is the executable shape of one of a rule's findings: the verb,
// its typed parameters, the before/after state a human reviews, and how hard
// the change is to undo. A Finding states a fact; RuleAction is the one place
// that fact turns into a proposed change, and only the rule that produced the
// finding is qualified to make that call.
type RuleAction struct {
	Type          optimize.ActionType
	Parameters    map[string]any
	ProposedState optimize.StateSnapshot
	Reversibility optimize.Reversibility
	Complexity    optimize.Complexity
	Title         string
	Rationale     string
	// ConflictDomain declares which dimension of the resource's spend this
	// change competes for, so the engine can tell two rules that are both
	// right about the same money apart from two rules that compose. Leaving
	// it unset resolves it from the action type
	// (optimize.DefaultConflictDomain), which is correct for every rule whose
	// action already says what it contends for. A rule must set it explicitly
	// in exactly one case: when its action is advisory_only — advice carries
	// no verb to derive a domain from — and its saving nonetheless claims the
	// same dollars as another rule's executable change. The EKS node-group
	// instance-type advisory is the worked example: it cannot be executed,
	// but it is still one of three answers to "this node group is too big".
	ConflictDomain optimize.ConflictDomain
}

// resolvedConflictDomain is the domain the engine records on the
// recommendation: the rule's declaration when it made one, the action's
// default otherwise.
func (a RuleAction) resolvedConflictDomain() optimize.ConflictDomain {
	if a.ConflictDomain != optimize.ConflictDomainNone {
		return a.ConflictDomain
	}
	return optimize.DefaultConflictDomain(a.Type)
}

// ActionBuilder is implemented by every rule in this package alongside Rule.
// It is a separate interface, not folded into Rule, so that Evaluate — the
// function every rule's tests exercise most — stays a pure "what is true"
// function with no action-shaping concerns leaking into it.
type ActionBuilder interface {
	BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction
}

// FullRule is the combination every concrete rule in this package satisfies.
// It exists so registration and lookup have one type to hold, rather than
// carrying two interface values per rule everywhere.
type FullRule interface {
	Rule
	ActionBuilder
}

// tenantOverride is one tenant's deviation from the shipped rule pack.
type tenantOverride struct {
	enabled    *bool
	thresholds map[string]float64
}

// Registry owns the rule catalogue: which rules exist, their YAML-declared
// defaults, and any per-tenant overrides layered on top. It is the engine's
// single mutable object; everything else in this package is a pure function
// of an EvalContext.
type Registry struct {
	mu    sync.RWMutex
	rules map[optimize.RuleID]FullRule
	defs  map[string]rulepack.RuleDef

	overridesMu sync.RWMutex
	overrides   map[core.TenantID]map[optimize.RuleID]tenantOverride

	logger *slog.Logger
}

var _ ThresholdSource = (*Registry)(nil)

// NewRegistry builds an empty registry over a parsed rule pack. Use
// NewDefaultRegistry (registry_init.go) to get one with every shipped rule
// already registered.
func NewRegistry(pack rulepack.Pack, logger *slog.Logger) *Registry {
	if logger == nil {
		logger = slog.Default()
	}
	return &Registry{
		rules:     map[optimize.RuleID]FullRule{},
		defs:      pack.Defs,
		overrides: map[core.TenantID]map[optimize.RuleID]tenantOverride{},
		logger:    logger,
	}
}

// Register adds a rule to the catalogue. It panics on a duplicate ID or on a
// rule whose ID has no matching YAML entry: both are programming errors
// caught at process startup, not conditions a caller should have to handle
// at request time.
func (reg *Registry) Register(r FullRule) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	id := r.ID()
	if _, dup := reg.rules[id]; dup {
		panic(fmt.Sprintf("optimization: rule %q registered twice", id))
	}
	if _, ok := reg.defs[string(id)]; !ok {
		panic(fmt.Sprintf("optimization: rule %q has no entry in the YAML rule pack", id))
	}
	reg.rules[id] = r
}

// Rules returns every registered rule, sorted by ID for deterministic
// iteration regardless of registration order.
func (reg *Registry) Rules() []FullRule {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	out := make([]FullRule, 0, len(reg.rules))
	for _, r := range reg.rules {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out
}

// Get looks up one rule by ID.
func (reg *Registry) Get(id optimize.RuleID) (FullRule, bool) {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	r, ok := reg.rules[id]
	return r, ok
}

// RuleInfo returns the full catalogue for the rules API, folding in the
// tenant's overrides and (when supplied) its calibration record.
func (reg *Registry) RuleInfo(tenant core.TenantID, calibrations map[optimize.RuleID]execute.RuleCalibration) []ports.RuleInfo {
	out := make([]ports.RuleInfo, 0, len(reg.rules))
	for _, r := range reg.Rules() {
		info := r.Info()
		info.Enabled = reg.Enabled(tenant, r.ID(), info.Enabled)
		if c, ok := calibrations[r.ID()]; ok {
			cc := c
			info.Calibration = &cc
		}
		out = append(out, info)
	}
	return out
}

// SetTenantOverride layers a tenant-specific enable flag and/or threshold
// overrides on top of the shipped defaults. Passing a nil enabled leaves the
// enabled state untouched; passing nil thresholds leaves thresholds
// untouched. Call with an empty tenantOverride equivalent (both nil) to
// effectively clear an override for that rule.
func (reg *Registry) SetTenantOverride(tenant core.TenantID, rule optimize.RuleID, enabled *bool, thresholds map[string]float64) {
	reg.overridesMu.Lock()
	defer reg.overridesMu.Unlock()
	if reg.overrides[tenant] == nil {
		reg.overrides[tenant] = map[optimize.RuleID]tenantOverride{}
	}
	cur := reg.overrides[tenant][rule]
	if enabled != nil {
		cur.enabled = enabled
	}
	if thresholds != nil {
		if cur.thresholds == nil {
			cur.thresholds = map[string]float64{}
		}
		for k, v := range thresholds {
			cur.thresholds[k] = v
		}
	}
	reg.overrides[tenant][rule] = cur
}

func (reg *Registry) lookupOverride(tenant core.TenantID, rule optimize.RuleID) (tenantOverride, bool) {
	reg.overridesMu.RLock()
	defer reg.overridesMu.RUnlock()
	o, ok := reg.overrides[tenant][rule]
	return o, ok
}

// Enabled reports whether a rule is active for a tenant: tenant override,
// then the YAML pack's shipped default, then the caller-supplied fallback
// (used only for a rule ID absent from the pack, which Register already
// prevents in practice).
func (reg *Registry) Enabled(tenant core.TenantID, rule optimize.RuleID, shippedDefault bool) bool {
	if o, ok := reg.lookupOverride(tenant, rule); ok && o.enabled != nil {
		return *o.enabled
	}
	if def, ok := reg.defs[string(rule)]; ok {
		return def.Enabled
	}
	return shippedDefault
}

func (reg *Registry) resolveFloat(tenant core.TenantID, rule optimize.RuleID, key string, fallback float64) float64 {
	if o, ok := reg.lookupOverride(tenant, rule); ok {
		if v, ok := o.thresholds[key]; ok {
			return v
		}
	}
	if def, ok := reg.defs[string(rule)]; ok {
		if v, ok := def.Thresholds[key]; ok {
			return v
		}
	}
	return fallback
}

// Float implements ThresholdSource.
func (reg *Registry) Float(tenant core.TenantID, rule optimize.RuleID, key string, fallback float64) float64 {
	return reg.resolveFloat(tenant, rule, key, fallback)
}

// Int implements ThresholdSource. Thresholds are stored as float64 in YAML
// (so a pack author never has to think about which keys are integers); Int
// rounds to the nearest whole number at the read site.
func (reg *Registry) Int(tenant core.TenantID, rule optimize.RuleID, key string, fallback int) int {
	return int(reg.resolveFloat(tenant, rule, key, float64(fallback)) + 0.5)
}

// Bool implements ThresholdSource, treating any nonzero threshold value as
// true.
func (reg *Registry) Bool(tenant core.TenantID, rule optimize.RuleID, key string, fallback bool) bool {
	if o, ok := reg.lookupOverride(tenant, rule); ok {
		if v, ok := o.thresholds[key]; ok {
			return v != 0
		}
	}
	if def, ok := reg.defs[string(rule)]; ok {
		if v, ok := def.Thresholds[key]; ok {
			return v != 0
		}
	}
	return fallback
}

// Duration implements ThresholdSource: the YAML value is a plain number in
// the caller-specified unit (days, hours), converted here so rule code works
// in time.Duration throughout.
func (reg *Registry) Duration(tenant core.TenantID, rule optimize.RuleID, key string, unit time.Duration, fallback time.Duration) time.Duration {
	if o, ok := reg.lookupOverride(tenant, rule); ok {
		if v, ok := o.thresholds[key]; ok {
			return time.Duration(v * float64(unit))
		}
	}
	if def, ok := reg.defs[string(rule)]; ok {
		if v, ok := def.Thresholds[key]; ok {
			return time.Duration(v * float64(unit))
		}
	}
	return fallback
}

// EvalDiagnostics reports what happened during a registry evaluation pass,
// beyond the findings themselves — the counts a run report and a warning log
// need.
type EvalDiagnostics struct {
	ResourcesConsidered  int
	RulesEvaluated       int
	RulesSkippedDisabled int
	FindingsProduced     int
	FindingsRejected     int
	Errors               []string
}

// Evaluate runs every enabled rule against every resource in the inventory
// and returns the findings in a fixed, reproducible order: rules sorted by
// ID, resources within a rule sorted by resource ID. Same inputs, same
// output, always — that determinism is the whole point of a rule engine a
// human is meant to trust.
//
// A finding that fails Finding.Validate() (most commonly: no evidence) is
// rejected rather than propagated. A rule producing an unvalidatable finding
// is a bug in that rule, and CloudOptix would rather silently under-report
// than let an unsupported claim reach a human reviewer.
func (reg *Registry) Evaluate(ctx context.Context, ec EvalContext) ([]optimize.Finding, EvalDiagnostics) {
	diag := EvalDiagnostics{}
	resources := make([]cloud.Resource, len(ec.Inventory.All()))
	copy(resources, ec.Inventory.All())
	sort.Slice(resources, func(i, j int) bool { return resources[i].ID < resources[j].ID })
	diag.ResourcesConsidered = len(resources)

	var findings []optimize.Finding
	for _, r := range reg.Rules() {
		if !reg.Enabled(ec.TenantID, r.ID(), r.Info().Enabled) {
			diag.RulesSkippedDisabled++
			continue
		}
		diag.RulesEvaluated++
		for _, res := range resources {
			if res.Deleted || !r.Applies(res) {
				continue
			}
			select {
			case <-ctx.Done():
				diag.Errors = append(diag.Errors, ctx.Err().Error())
				return findings, diag
			default:
			}
			fs, err := r.Evaluate(ec, res)
			if err != nil {
				diag.Errors = append(diag.Errors, fmt.Sprintf("rule %s on resource %s: %v", r.ID(), res.ID, err))
				reg.logger.Warn("optimization: rule evaluation failed",
					"rule", r.ID(), "resource", res.ID, "error", err)
				continue
			}
			for _, f := range fs {
				if err := f.Validate(); err != nil {
					diag.FindingsRejected++
					reg.logger.Warn("optimization: rejected an unvalidatable finding",
						"rule", r.ID(), "resource", res.ID, "error", err)
					continue
				}
				findings = append(findings, f)
				diag.FindingsProduced++
			}
		}
	}
	return findings, diag
}
