package optimization

import (
	"log/slog"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/pricing"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/spec"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
	rulepack "github.com/udaykishore-resu/cloudoptix/rules"
)

// testTenant is the fixed tenant every rule test builds resources under.
const testTenant = core.TenantID("tenant-test")

// regionUSEast1 is the price book's base region (multiplier 1.0), used by
// every test that needs a region with no regional markup to reason about.
const regionUSEast1 = core.Region("us-east-1")

// testNow is the fixed instant every rule test evaluates against, so age-based
// guards (daysSince, r.Age) are deterministic across runs.
var testNow = time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)

// testCatalog loads the real embedded price book. Using the production
// catalog rather than a hand-rolled fake means a rule test is exercising the
// same numbers a real deployment would compute with, and a pricebook change
// that quietly breaks a rule's arithmetic shows up here too.
func newTestCatalog(t interface{ Helper() }) ports.PricingCatalog {
	t.Helper()
	return pricing.New()
}

// emptyThresholds returns a Registry with no YAML-declared rule defaults, so
// every Thresholds.Float/Int/Bool/Duration call in a rule under test falls
// through to the fallback value the rule itself passes at the call site —
// exactly the values documented in rules/*.yaml as the shipped defaults.
func emptyThresholds() *Registry {
	return NewRegistry(rulepack.Pack{Defs: map[string]rulepack.RuleDef{}}, slog.New(slog.NewTextHandler(discardWriter{}, nil)))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// testEvalContext builds a minimal, deterministic EvalContext for exercising
// a single rule's decide function directly.
func testEvalContext(inv *cloud.Inventory, topo *cloud.Topology, metrics map[core.ID]ports.ResourceMetrics, sp spec.Spec) EvalContext {
	if topo == nil {
		topo = cloud.NewTopology(nil)
	}
	return EvalContext{
		TenantID:       testTenant,
		Inventory:      inv,
		Topology:       topo,
		Metrics:        metrics,
		CostByResource: map[core.ID]core.Money{},
		Pricing:        pricing.New(),
		Spec:           sp,
		Calibrations:   nil, // no calibration history: every rule reads as uncalibrated (multiplier 1.0)
		Thresholds:     emptyThresholds(),
		Clock:          core.FixedClock{T: testNow},
	}
}

// mkResource builds a resource with sane defaults for the fields every rule
// guards on (tenant, kind, state, region), letting the caller override the
// rest via the returned value.
func mkResource(kind cloud.Kind, instanceType string) cloud.Resource {
	return cloud.Resource{
		ID:           core.NewID("res"),
		TenantID:     testTenant,
		AccountID:    core.AccountID("111122223333"),
		Region:       core.Region("us-east-1"),
		Kind:         kind,
		NativeID:     string(core.NewID("native")),
		Name:         "test-" + string(kind),
		State:        cloud.StateRunning,
		InstanceType: instanceType,
		Environment:  core.EnvProduction,
		FirstSeenAt:  testNow.Add(-90 * 24 * time.Hour),
		LastSeenAt:   testNow,
		CreatedAt:    testNow.Add(-90 * 24 * time.Hour),
	}
}

func pct(p50, p95, p99, mean float64) *core.Percentiles {
	return &core.Percentiles{P50: p50, P95: p95, P99: p99, Mean: mean, Coverage: 1.0, Samples: 1000}
}

// testSpec returns a permissive-but-plausible tenant specification: medium
// risk tolerance, Spot opted in, no blanket saving floor beyond a rule's own
// — the neutral baseline most rule tests build on, overridden per test where
// the spec itself is what's under test.
func testSpec() spec.Spec {
	var sp spec.Spec
	sp.Optimization.RiskTolerance = "medium"
	sp.Optimization.SpotAllowed = true
	return sp
}
