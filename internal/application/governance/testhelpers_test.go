package governance

import (
	"context"
	"log/slog"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/memstore"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/spec"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

const testTenant = core.TenantID("tenant-governance-test")

var testNow = time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC) // a Monday

func ctxFor(tenant core.TenantID) context.Context {
	return core.WithPrincipal(context.Background(), core.SystemPrincipal(tenant, "test"))
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, nil))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// newTestService wires a Service against a fresh in-memory Store, returning
// both so a test can seed fixtures directly through the repositories.
func newTestService(t interface{ Helper() }) (*Service, ports.Repositories) {
	t.Helper()
	repos := memstore.New().Repositories()
	svc, err := NewService(Deps{
		Policies: repos.Policies, Approvals: repos.Approvals, Recommendations: repos.Recommendations,
		Resources: repos.Resources, Specs: repos.Specs, Audit: repos.Audit, Economics: repos.Economics,
		Clock: core.FixedClock{T: testNow}, Logger: discardLogger(),
	})
	if err != nil {
		panic(err) // programmer error in the test fixture, not a real assertion failure
	}
	return svc, repos
}

func mkResource(tenant core.TenantID) cloud.Resource {
	return cloud.Resource{
		ID: core.NewID("res"), TenantID: tenant, AccountID: "111122223333", Region: "us-east-1",
		Kind: cloud.KindEC2Instance, NativeID: "i-0abc123", State: cloud.StateRunning,
		Environment: core.EnvProduction, EnvironmentSource: core.ProvenanceConfirmed,
		Tags: core.Tags{"Name": "web-1"}, MonthlyCost: core.USDollars(500),
		FirstSeenAt: testNow, LastSeenAt: testNow,
	}
}

// mkRecommendation builds a plausible, fully-populated recommendation whose
// Finding, Risk and BlastRadius are complete enough to pass buildInput's
// required-field check — the baseline every governance test starts from and
// mutates.
func mkRecommendation(tenant core.TenantID, res cloud.Resource, action optimize.ActionType) optimize.Recommendation {
	return optimize.Recommendation{
		ID:       core.NewID("rec"),
		TenantID: tenant,
		Finding: optimize.Finding{
			ID: core.NewID("fnd"), TenantID: tenant, RuleID: "rule.ec2.rightsize", RuleName: "EC2 rightsizing",
			Category: optimize.CategoryRightsizing, ResourceID: res.ID, ResourceKind: res.Kind,
			AccountID: res.AccountID, Region: res.Region, Environment: res.Environment,
			CurrentMonthlyCost: core.USDollars(500), EstimatedMonthlySaving: core.USDollars(150),
			Evidence: []optimize.Evidence{{Kind: "metric", Label: "cpu p95", Value: "8%", Source: "cloudwatch"}},
		},
		Title: "Rightsize web-1", Action: action,
		CurrentState:           optimize.StateSnapshot{MonthlyCost: core.USDollars(500)},
		ProposedState:          optimize.StateSnapshot{MonthlyCost: core.USDollars(350)},
		EstimatedMonthlySaving: core.USDollars(150),
		Confidence:             0.92,
		Risk:                   optimize.RiskAssessment{Score: 0.1, Level: core.RiskLow},
		BlastRadius:            optimize.BlastRadius{Score: 0.1, CriticalServices: 0, Completeness: 1},
		Reversibility:          optimize.ReversibilityFast,
		Status:                 optimize.StatusOpen,
		CreatedAt:              testNow, UpdatedAt: testNow,
	}
}

func testSpec(automationEnabled bool) spec.Spec {
	var sp spec.Spec
	sp.Automation.Enabled = automationEnabled
	sp.Governance.ProductionChangesRequireApproval = true
	sp.Governance.MinApprovals = 1
	return sp
}

func seedSpec(t interface{ Helper() }, repos ports.Repositories, tenant core.TenantID, sp spec.Spec) {
	t.Helper()
	v := spec.Version{
		ID: core.NewID("sv"), TenantID: tenant, SpecID: core.NewID("spec"), Version: 1,
		Status: spec.StatusApproved, Spec: sp, CreatedAt: testNow,
	}
	if err := repos.Specs.SaveDraft(ctxFor(tenant), v); err != nil {
		panic(err)
	}
}
