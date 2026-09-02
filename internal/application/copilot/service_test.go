package copilot_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/llm/deterministic"
	"github.com/udaykishore-resu/cloudoptix/internal/adapters/memstore"
	"github.com/udaykishore-resu/cloudoptix/internal/adapters/rag"
	"github.com/udaykishore-resu/cloudoptix/internal/application/copilot"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cost"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/econ"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

const testTenant = core.TenantID("tenant-copilot-test")

func testCtx() context.Context {
	return core.WithPrincipal(context.Background(), core.Principal{
		Subject: "test-analyst", TenantID: testTenant, Roles: []core.Role{core.RoleFinOpsAnalyst},
	})
}

// seedTenant populates a memstore with enough realistic data for every
// tool the copilot registers to have something grounded to say: cost
// records across two services, a resource, an open recommendation with a
// blast radius, an economic footprint, a business transaction with unit
// economics, a cost SLO in breach, an efficiency score, and a savings
// funnel.
func seedTenant(t *testing.T, store *memstore.Store) {
	t.Helper()
	ctx := testCtx()
	now := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	period := core.PeriodOfDays(now, 30)

	_, err := store.Repositories().Costs.UpsertBatch(ctx, testTenant, []cost.Record{
		{
			ID: core.NewID("cst"), TenantID: testTenant, AccountID: "111122223333", Region: "us-east-1",
			Period: period, Granularity: cost.GranularityMonthly, Service: "Amazon Elastic Compute Cloud - Compute",
			ChargeType: cost.ChargeUsage, Basis: cost.BasisAmortized, Amount: core.USDollars(45000),
			Environment: core.EnvProduction, Source: "cost_explorer", IngestedAt: now,
		},
		{
			ID: core.NewID("cst"), TenantID: testTenant, AccountID: "111122223333", Region: "us-east-1",
			Period: period, Granularity: cost.GranularityMonthly, Service: "Amazon Simple Storage Service",
			ChargeType: cost.ChargeUsage, Basis: cost.BasisAmortized, Amount: core.USDollars(5000),
			Environment: core.EnvProduction, Source: "cost_explorer", IngestedAt: now,
		},
	})
	require.NoError(t, err)

	require.NoError(t, store.Repositories().Costs.SaveAnomalies(ctx, testTenant, []cost.Anomaly{{
		ID: core.NewID("anm"), TenantID: testTenant, DetectedAt: now.Add(-2 * 24 * time.Hour), Period: period,
		Dimension: "service", Key: "Amazon Elastic Compute Cloud - Compute",
		Expected: core.USDollars(30000), Actual: core.USDollars(45000), Delta: core.USDollars(15000),
		DeltaPct: 0.5, Severity: core.SeverityHigh, Explanation: "Auto Scaling group scaled out for a sustained load increase.",
	}}))

	res := cloud.Resource{
		ID: core.NewID("res"), TenantID: testTenant, AccountID: "111122223333", Region: "us-east-1",
		Kind: cloud.KindEC2Instance, NativeID: "i-0abc123def456789", Name: "checkout-api-worker",
		State: cloud.StateRunning, InstanceType: "m5.2xlarge", Environment: core.EnvProduction,
		MonthlyCost: core.USDollars(4500), FirstSeenAt: now.AddDate(0, -3, 0), LastSeenAt: now,
	}
	n, err := store.Repositories().Resources.UpsertBatch(ctx, testTenant, []cloud.Resource{res})
	require.NoError(t, err)
	require.Equal(t, 1, n)

	rec := optimize.Recommendation{
		ID: core.NewID("rec"), TenantID: testTenant,
		Finding: optimize.Finding{
			ID: core.NewID("fnd"), TenantID: testTenant, Category: optimize.CategoryRightsizing,
			ResourceID: res.ID, ResourceName: res.Name, ResourceKind: res.Kind, AccountID: res.AccountID,
			Region: res.Region, Environment: res.Environment, CurrentMonthlyCost: res.MonthlyCost,
		},
		Title: "Rightsize checkout-api-worker from m5.2xlarge to m5.xlarge", Action: optimize.ActionResizeInstance,
		EstimatedMonthlySaving: core.USDollars(2200), EstimatedAnnualSaving: core.USDollars(26400),
		Confidence: 0.85, Risk: optimize.RiskAssessment{Score: 0.2, Level: core.RiskLow},
		BlastRadius: optimize.BlastRadius{
			ResourcesAffected: 1, ServicesAffected: 1, CriticalServices: 1, APIsAffected: 2,
			TransactionsAffected: []string{"checkout"}, EstimatedUsers: 50000, Score: 0.6, Level: core.RiskMedium,
			Completeness: 0.9, Explanation: "Serves the checkout API directly; no redundant replica in this AZ.",
		},
		Status: optimize.StatusOpen, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, store.Repositories().Recommendations.SaveBatch(ctx, testTenant, []optimize.Recommendation{rec}))

	require.NoError(t, store.Repositories().Economics.SaveFootprints(ctx, testTenant, []econ.Footprint{{
		ID: core.NewID("fpt"), TenantID: testTenant, Scope: econ.ScopeOrganization, Label: "Organization",
		Period: period, Direct: core.USDollars(40000), Indirect: core.USDollars(8000), Shared: core.USDollars(2000),
		Total: core.USDollars(50000), Coverage: 0.96,
	}}))

	tx := econ.BusinessTransaction{ID: core.NewID("txn"), TenantID: testTenant, Name: "checkout", CreatedAt: now, UpdatedAt: now}
	require.NoError(t, store.Repositories().Economics.UpsertTransaction(ctx, tx))
	require.NoError(t, store.Repositories().Economics.SaveUnitEconomics(ctx, testTenant, []econ.UnitEconomics{{
		ID: core.NewID("ue"), TenantID: testTenant, TransactionID: tx.ID, Name: "checkout", Period: period,
		Volume: 500000, TotalCost: core.USDollars(25000), CostPerUnit: core.USDollars(0.05),
		ChangePct: 0.12, ComputedAt: now,
	}}))

	slo := econ.CostSLO{
		ID: core.NewID("slo"), TenantID: testTenant, Name: "Monthly AWS Budget", Kind: econ.SLOAbsoluteSpend,
		Scope: econ.ScopeOrganization, Target: core.USDollars(48000), Window: econ.WindowCalendarMonth,
		ErrorBudgetPct: 0.1, Enabled: true,
	}
	require.NoError(t, store.Repositories().Economics.UpsertCostSLO(ctx, slo))
	budget := econ.EvaluateBudget(slo, core.USDollars(50000), now)
	require.NoError(t, store.Repositories().Economics.SaveBudgetState(ctx, budget))

	require.NoError(t, store.Repositories().Economics.SaveEfficiencyScore(ctx, econ.EfficiencyScore{
		ID: core.NewID("eff"), TenantID: testTenant, Scope: econ.ScopeOrganization, Label: "Organization",
		Period: period, Score: 62, Grade: "C", WasteRatio: 0.18, TotalSpend: core.USDollars(50000),
		IdentifiedWaste: core.USDollars(9000),
		Factors: []econ.EfficiencyFactor{
			{Name: "resource_utilization", Score: 55}, {Name: "commitment_coverage", Score: 40},
		},
		ComputedAt: now,
	}))
}

func newTestKnowledgeStore(t *testing.T) *rag.Store {
	t.Helper()
	knowledge := rag.New(nil)
	require.NoError(t, rag.SeedPlatformCorpus(context.Background(), knowledge))
	return knowledge
}

func newTestService(t *testing.T) (*copilot.Service, *memstore.Store) {
	t.Helper()
	store := memstore.New()
	seedTenant(t, store)
	svc := copilot.New(store, deterministic.New(), newTestKnowledgeStore(t))
	return svc, store
}

func ask(t *testing.T, svc *copilot.Service, question string) ports.CopilotAnswer {
	t.Helper()
	ans, err := svc.Ask(testCtx(), testTenant, ports.CopilotRequest{Question: question, Actor: "test-user"})
	require.NoError(t, err, "question: %s", question)
	require.NotEmpty(t, ans.Answer, "question: %s", question)
	return ans
}

// TestAsk_AnswersThePromisedQuestions drives every specific question the
// copilot is required to answer end-to-end through the deterministic
// provider, asserting each produces a non-empty, grounded answer.
func TestAsk_AnswersThePromisedQuestions(t *testing.T) {
	svc, _ := newTestService(t)
	questions := []string{
		"Why did our AWS cost increase this month?",
		"What's wasting money right now?",
		"Which service is most expensive?",
		"How do we cut cost by 30%?",
		"What is our cost per transaction for checkout?",
		"What happens if traffic doubles?",
		"Which recommendation has the highest blast radius?",
		"What should we optimize first?",
		"How much are we spending overall?",
		"What is FinOps?",
	}
	for _, q := range questions {
		t.Run(q, func(t *testing.T) {
			ans := ask(t, svc, q)
			assert.True(t, ans.Grounded, "answer for %q was not grounded: %v\nanswer: %s", q, ans.GroundingIssues, ans.Answer)
			assert.False(t, ans.Degraded)
		})
	}
}

func TestAsk_PersistsConversationAcrossTurns(t *testing.T) {
	svc, _ := newTestService(t)
	first := ask(t, svc, "How much are we spending?")
	require.NotEmpty(t, first.ConversationID)

	second, err := svc.Ask(testCtx(), testTenant, ports.CopilotRequest{
		ConversationID: first.ConversationID, Question: "What's the most expensive service?", Actor: "test-user",
	})
	require.NoError(t, err)
	assert.Equal(t, first.ConversationID, second.ConversationID)

	conv, err := svc.GetConversation(testCtx(), testTenant, first.ConversationID)
	require.NoError(t, err)
	// Two questions asked, each appending a user + assistant turn.
	assert.Len(t, conv.Turns, 4)
}

func TestAsk_RejectsEmptyQuestion(t *testing.T) {
	svc, _ := newTestService(t)
	_, err := svc.Ask(testCtx(), testTenant, ports.CopilotRequest{Question: "   "})
	assert.Error(t, err)
}

func TestAsk_DegradesGracefullyWithNoProvider(t *testing.T) {
	store := memstore.New()
	seedTenant(t, store)
	svc := copilot.New(store, nil, nil)

	ans := ask(t, svc, "How much are we spending?")
	assert.True(t, ans.Degraded)
	assert.True(t, ans.Grounded)
	assert.Contains(t, ans.Answer, "temporarily unavailable")
}

func TestSuggestions_ReflectsTenantState(t *testing.T) {
	svc, _ := newTestService(t)
	suggestions, err := svc.Suggestions(testCtx(), testTenant)
	require.NoError(t, err)
	assert.NotEmpty(t, suggestions)
	// The seeded tenant has an open recommendation and a breached budget.
	joined := ""
	for _, s := range suggestions {
		joined += s + " "
	}
	assert.Contains(t, joined, "optimize")
}

func TestListConversations_ReturnsAskedConversations(t *testing.T) {
	svc, _ := newTestService(t)
	ask(t, svc, "How much are we spending?")

	page, err := svc.ListConversations(testCtx(), testTenant, ports.ListOptions{})
	require.NoError(t, err)
	assert.NotEmpty(t, page.Items)
}

func TestAsk_TenantMismatchIsRejected(t *testing.T) {
	svc, _ := newTestService(t)
	_, err := svc.Ask(testCtx(), core.TenantID("some-other-tenant"), ports.CopilotRequest{Question: "hi"})
	assert.Error(t, err)
}
