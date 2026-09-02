package automation

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/awssim"
	"github.com/udaykishore-resu/cloudoptix/internal/adapters/memstore"
	"github.com/udaykishore-resu/cloudoptix/internal/adapters/pricing"
	"github.com/udaykishore-resu/cloudoptix/internal/application/governance"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/execute"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/govern"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/spec"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

const testTenant = core.TenantID("tenant-automation-test")
const testAccountID = core.AccountID("111122223333")
const testRegion = core.Region("us-east-1")
const testInstanceID = "i-0abc123"

// testNow lands inside no declared maintenance window on purpose: most
// tests exercise the human-approval path (governance's own documented
// fail-closed behaviour when no policy is active makes RequireApproval, not
// AutoExecute, the reachable outcome — see governance's doc.go and this
// package's autonomous.go doc comment), so maintenance-window timing only
// matters for the autonomous-loop tests, which set their own windows.
var testNow = time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC) // a Monday

func ctxFor(tenant core.TenantID) context.Context {
	return core.WithPrincipal(context.Background(), core.SystemPrincipal(tenant, "test"))
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, nil))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// testHarness bundles everything a test needs: the automation Service under
// test, the raw repositories to seed fixtures and inspect state directly,
// and the simulated AWS estate/broker so a test can assert on the actual
// simulated resource (e.g. that a resize really landed) rather than only on
// CloudOptix's own records of it.
type testHarness struct {
	svc    *Service
	repos  ports.Repositories
	estate *awssim.Estate
	broker *awssim.Broker
	gov    *governance.Service
}

// newHarness wires a fresh in-memory Store, a fresh simulated AWS estate
// with every scope granted, the real governance.Service (not a fake — the
// interaction between automation's re-evaluation and governance's actual
// deny-bias and fail-closed behaviour is exactly what several of these
// tests exist to prove) and an automation.Service built from all of it.
func newHarness(t *testing.T) *testHarness {
	t.Helper()
	store := memstore.New()
	repos := store.Repositories()

	estate := awssim.NewEstate(testAccountID, "automation-test", []core.Region{testRegion}, pricing.New())
	broker := awssim.NewBroker(estate, cloud.ScopeRead, cloud.ScopeAnalyze, cloud.ScopePlan, cloud.ScopeExecute)

	require := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("test fixture setup failed: %v", err)
		}
	}
	require(repos.AWSAccounts.Create(ctxFor(testTenant), cloud.AWSAccount{
		ID: core.NewID("acct"), TenantID: testTenant, AccountID: testAccountID,
		Environment: core.EnvDevelopment, Regions: []core.Region{testRegion},
		AccessMode: cloud.AccessSimulated, GrantedScopes: []cloud.RoleScope{cloud.ScopeRead, cloud.ScopeAnalyze, cloud.ScopePlan, cloud.ScopeExecute},
		State: cloud.ConnConnected,
	}))

	executors := map[optimize.ActionType]ports.Executor{}
	for _, ex := range awssim.NewExecutors() {
		executors[ex.Action()] = ex
	}

	govSvc, err := governance.NewService(governance.Deps{
		Policies: repos.Policies, Approvals: repos.Approvals, Recommendations: repos.Recommendations,
		Resources: repos.Resources, Specs: repos.Specs, Audit: repos.Audit, Economics: repos.Economics,
		Clock: core.FixedClock{T: testNow}, Logger: discardLogger(),
	})
	require(err)

	svc, err := NewService(Deps{
		Executions: repos.Executions, Recommendations: repos.Recommendations, Resources: repos.Resources,
		AWSAccounts: repos.AWSAccounts, Policies: repos.Policies, Approvals: repos.Approvals,
		Savings: repos.Savings, Specs: repos.Specs, Audit: repos.Audit,
		Metrics: repos.Metrics, Costs: repos.Costs,
		Credentials: broker, Executors: executors, Locker: store.Locker(), Governance: govSvc,
		Clock: core.FixedClock{T: testNow}, Logger: discardLogger(),
	})
	require(err)

	return &testHarness{svc: svc, repos: repos, estate: estate, broker: broker, gov: govSvc}
}

// seedEC2 places one running m5.4xlarge instance into the simulated estate
// and returns the matching cloud.Resource, already persisted to the
// resource repository.
func (h *testHarness) seedEC2(t *testing.T) cloud.Resource {
	t.Helper()
	h.estate.EC2Instances[testInstanceID] = &awssim.EC2Instance{
		Base:         awssim.Base{ID: testInstanceID, Region: testRegion, AZ: "us-east-1a", State: cloud.StateRunning, Tags: core.Tags{}},
		InstanceType: "m5.4xlarge", Platform: "linux",
	}
	res := cloud.Resource{
		ID: core.NewID("res"), TenantID: testTenant, AccountID: testAccountID, Region: testRegion,
		Kind: cloud.KindEC2Instance, NativeID: testInstanceID, State: cloud.StateRunning,
		Environment: core.EnvDevelopment, EnvironmentSource: core.ProvenanceConfirmed,
		Tags: core.Tags{"Name": "web-1"}, MonthlyCost: core.USDollars(500),
		FirstSeenAt: testNow, LastSeenAt: testNow,
	}
	if _, err := h.repos.Resources.UpsertBatch(ctxFor(testTenant), testTenant, []cloud.Resource{res}); err != nil {
		t.Fatalf("seeding resource: %v", err)
	}
	return res
}

// seedRecommendation builds a resize-instance recommendation against res and
// persists it, open and ready to plan.
func (h *testHarness) seedRecommendation(t *testing.T, res cloud.Resource) optimize.Recommendation {
	t.Helper()
	rec := optimize.Recommendation{
		ID: core.NewID("rec"), TenantID: testTenant,
		Finding: optimize.Finding{
			ID: core.NewID("fnd"), TenantID: testTenant, RuleID: "rule.ec2.rightsize", RuleName: "EC2 rightsizing",
			Category: optimize.CategoryRightsizing, ResourceID: res.ID, ResourceKind: res.Kind,
			AccountID: res.AccountID, Region: res.Region, Environment: res.Environment,
			CurrentMonthlyCost: core.USDollars(500), EstimatedMonthlySaving: core.USDollars(150),
			Evidence: []optimize.Evidence{{Kind: "metric", Label: "cpu p95", Value: "8%", Source: "cloudwatch"}},
		},
		Title: "Rightsize web-1", Action: optimize.ActionResizeInstance,
		Parameters:             map[string]any{"instance_type": "m5.large"},
		CurrentState:           optimize.StateSnapshot{MonthlyCost: core.USDollars(500)},
		ProposedState:          optimize.StateSnapshot{MonthlyCost: core.USDollars(350)},
		EstimatedMonthlySaving: core.USDollars(150), EstimatedAnnualSaving: core.USDollars(1800),
		Confidence:    0.92,
		Risk:          optimize.RiskAssessment{Score: 0.1, Level: core.RiskLow},
		BlastRadius:   optimize.BlastRadius{Score: 0.1, CriticalServices: 0, Completeness: 1},
		Reversibility: optimize.ReversibilityFast,
		Status:        optimize.StatusOpen,
		CreatedAt:     testNow, UpdatedAt: testNow,
	}
	if err := h.repos.Recommendations.SaveBatch(ctxFor(testTenant), testTenant, []optimize.Recommendation{rec}); err != nil {
		t.Fatalf("seeding recommendation: %v", err)
	}
	return rec
}

func (h *testHarness) seedSpec(t *testing.T, sp spec.Spec) {
	t.Helper()
	v := spec.Version{
		ID: core.NewID("sv"), TenantID: testTenant, SpecID: core.NewID("spec"), Version: 1,
		Status: spec.StatusApproved, Spec: sp, CreatedAt: testNow,
	}
	if err := h.repos.Specs.SaveDraft(ctxFor(testTenant), v); err != nil {
		t.Fatalf("seeding spec: %v", err)
	}
}

func defaultSpec() spec.Spec {
	var sp spec.Spec
	sp.Automation.Enabled = true
	sp.Automation.ValidationWindowMinutes = 30
	sp.Governance.ProductionChangesRequireApproval = true
	sp.Governance.MinApprovals = 1
	return sp
}

// planAndApprove drives a recommendation through PlanExecution and, when the
// resulting plan needs a human approval (the reachable path today — see
// testNow's comment above), approves it with a reviewer distinct from
// whoever requested it. It returns the now-executable plan.
func (h *testHarness) planAndApprove(t *testing.T, recID core.ID) execute.Plan {
	t.Helper()
	plan, err := h.svc.PlanExecution(ctxFor(testTenant), testTenant, recID, ports.PlanOptions{RequestedBy: "requester@example.com"})
	if err != nil {
		t.Fatalf("PlanExecution: %v", err)
	}
	if plan.State == execute.PlanApproved {
		return plan
	}
	if plan.ApprovalID.IsZero() {
		t.Fatalf("plan %s is %s but carries no approval id", plan.ID, plan.State)
	}
	if _, err := h.gov.Decide(ctxFor(testTenant), testTenant, plan.ApprovalID, govern.Response{
		Principal: "reviewer@example.com", Approved: true,
	}); err != nil {
		t.Fatalf("approving plan %s: %v", plan.ID, err)
	}
	plan, err = h.svc.GetPlan(ctxFor(testTenant), testTenant, plan.ID)
	if err != nil {
		t.Fatalf("re-fetching approved plan: %v", err)
	}
	return plan
}
