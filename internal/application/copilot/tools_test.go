package copilot_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/memstore"
	"github.com/udaykishore-resu/cloudoptix/internal/application/copilot"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/simulate"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// TestTool_GetResource_ResolvesByNativeID exercises "tell me about this
// specific instance" directly against the tool (rather than through the
// agentic loop), which is the promised question about a specific resource.
func TestTool_GetResource_ResolvesByNativeID(t *testing.T) {
	store := memstore.New()
	seedTenant(t, store)
	r := copilot.BuildRegistry(store, nil)

	tool, ok := r.Get("get_resource")
	require.True(t, ok)
	out, err := tool.Invoke(testCtx(), testTenant, map[string]any{"id": "i-0abc123def456789"})
	require.NoError(t, err)
	m, ok := out.(map[string]any)
	require.True(t, ok)
	summary, _ := m["summary"].(string)
	assert.Contains(t, summary, "checkout-api-worker")
	assert.Contains(t, summary, "m5.2xlarge")
}

func TestTool_GetResource_UnknownIDReturnsError(t *testing.T) {
	store := memstore.New()
	seedTenant(t, store)
	r := copilot.BuildRegistry(store, nil)

	tool, _ := r.Get("get_resource")
	out, err := tool.Invoke(testCtx(), testTenant, map[string]any{"id": "i-doesnotexist"})
	require.NoError(t, err)
	m := out.(map[string]any)
	assert.NotEmpty(t, m["error"])
}

// TestTool_ExplainCostChange_CompilationID answers "what Terraform change
// increased cost" for a specific, named infrastructure change — one of the
// explicitly promised copilot questions.
func TestTool_ExplainCostChange_CompilationID(t *testing.T) {
	store := memstore.New()
	seedTenant(t, store)
	now := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	comp := simulate.CompilationResult{
		ID: core.NewID("cmp"), TenantID: testTenant, Source: simulate.SourceTerraformPlan,
		Label: "Add read replica to checkout-db", BaselineMonthly: core.USDollars(50000),
		ProjectedMonthly: core.USDollars(57500), MonthlyDelta: core.USDollars(7500),
		AnnualDelta: core.USDollars(90000), DeltaPct: 0.15, CreatedCount: 1, Coverage: 1.0,
		CompiledAt: now,
	}
	require.NoError(t, store.Repositories().Simulations.SaveCompilation(testCtx(), comp))

	r := copilot.BuildRegistry(store, nil)
	tool, ok := r.Get("explain_cost_change")
	require.True(t, ok)
	out, err := tool.Invoke(testCtx(), testTenant, map[string]any{"compilation_id": string(comp.ID)})
	require.NoError(t, err)
	m := out.(map[string]any)
	summary, _ := m["summary"].(string)
	assert.Contains(t, summary, "Add read replica to checkout-db")
	assert.Contains(t, summary, "increased")
	assert.Empty(t, m["error"])
}

// TestTool_QueryArchitectureGraph_CheapestArchitectureQuestion answers a
// "what depends on this / what's the shape of our architecture" query, the
// grounding behind a "which architecture is cheapest" comparison.
func TestTool_QueryArchitectureGraph_WalksDependencies(t *testing.T) {
	store := memstore.New()
	seedTenant(t, store)
	ctx := testCtx()

	lb := cloud.Resource{
		ID: core.NewID("res"), TenantID: testTenant, AccountID: "111122223333", Region: "us-east-1",
		Kind: cloud.KindALB, NativeID: "alb-checkout", Name: "checkout-alb", State: cloud.StateRunning,
		Environment: core.EnvProduction, FirstSeenAt: time.Now(), LastSeenAt: time.Now(),
	}
	_, err := store.Repositories().Resources.UpsertBatch(ctx, testTenant, []cloud.Resource{lb})
	require.NoError(t, err)

	page, err := store.Repositories().Resources.List(ctx, testTenant, ports.ResourceFilter{Search: "checkout-api-worker"}, ports.ListOptions{Limit: 1})
	require.NoError(t, err)
	require.NotEmpty(t, page.Items)
	worker := page.Items[0]

	require.NoError(t, store.Repositories().Resources.ReplaceRelationships(ctx, testTenant, "111122223333", "us-east-1", []cloud.Relationship{
		{ID: core.NewID("rel"), TenantID: testTenant, FromID: lb.ID, ToID: worker.ID, Kind: cloud.RelRoutesTo, Weight: 1, Confidence: 0.9},
	}))

	r := copilot.BuildRegistry(store, nil)
	tool, ok := r.Get("query_architecture_graph")
	require.True(t, ok)
	out, err := tool.Invoke(ctx, testTenant, map[string]any{"resource_id": string(worker.ID)})
	require.NoError(t, err)
	m := out.(map[string]any)
	summary, _ := m["summary"].(string)
	assert.Contains(t, summary, "depending on it")
}

func TestTool_QueryArchitectureGraph_SummarizesWithNoResourceGiven(t *testing.T) {
	store := memstore.New()
	seedTenant(t, store)
	r := copilot.BuildRegistry(store, nil)
	tool, _ := r.Get("query_architecture_graph")
	out, err := tool.Invoke(testCtx(), testTenant, map[string]any{})
	require.NoError(t, err)
	m := out.(map[string]any)
	assert.NotEmpty(t, m["summary"])
}

func TestTool_RunCounterfactual_TrafficDoubling(t *testing.T) {
	store := memstore.New()
	seedTenant(t, store)
	r := copilot.BuildRegistry(store, nil)
	tool, _ := r.Get("run_counterfactual")
	out, err := tool.Invoke(testCtx(), testTenant, map[string]any{"multiplier": 2.0})
	require.NoError(t, err)
	m := out.(map[string]any)
	summary, _ := m["summary"].(string)
	assert.Contains(t, summary, "2.0x")
	assert.Contains(t, summary, "coarse")
}

func TestTool_SearchKnowledge_ReturnsPassages(t *testing.T) {
	store := memstore.New()
	seedTenant(t, store)
	knowledge := newTestKnowledgeStore(t)
	r := copilot.BuildRegistry(store, knowledge)
	tool, _ := r.Get("search_knowledge")
	out, err := tool.Invoke(testCtx(), testTenant, map[string]any{"query": "reserved instance commitment discount"})
	require.NoError(t, err)
	m := out.(map[string]any)
	summary, _ := m["summary"].(string)
	assert.NotEmpty(t, summary)
}

func TestTool_SearchKnowledge_NoStoreReturnsError(t *testing.T) {
	store := memstore.New()
	seedTenant(t, store)
	r := copilot.BuildRegistry(store, nil)
	tool, _ := r.Get("search_knowledge")
	out, err := tool.Invoke(testCtx(), testTenant, map[string]any{"query": "anything"})
	require.NoError(t, err)
	m := out.(map[string]any)
	assert.NotEmpty(t, m["error"])
}
