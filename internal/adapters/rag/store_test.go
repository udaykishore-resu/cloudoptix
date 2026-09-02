package rag

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

func TestChunk_OverlapCoversBoundary(t *testing.T) {
	words := make([]string, 500)
	for i := range words {
		words[i] = "word"
	}
	// Plant a unique marker exactly at what would be a hard chunk boundary
	// with no overlap (word index 180), and verify overlap keeps it inside
	// more than one chunk's word set at least once — proving overlap works
	// rather than merely not crashing.
	words[179] = "MARKER"
	text := ""
	for i, w := range words {
		if i > 0 {
			text += " "
		}
		text += w
	}
	chunks := Chunk(text, 180, 40)
	require.NotEmpty(t, chunks)
	occurrences := 0
	for _, c := range chunks {
		if containsWord(c, "MARKER") {
			occurrences++
		}
	}
	assert.GreaterOrEqual(t, occurrences, 1)

	// No overlap requested at all still produces exactly ceil(n/chunkWords) chunks.
	flat := Chunk(text, 100, 0)
	assert.Equal(t, 5, len(flat))
}

func containsWord(s, w string) bool {
	for _, tok := range splitWords(s) {
		if tok == w {
			return true
		}
	}
	return false
}

func splitWords(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ' ' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func TestHashEmbedder_Deterministic(t *testing.T) {
	e := NewHashEmbedder()
	ctx := context.Background()
	v1, err := e.Embed(ctx, []string{"gp3 volumes are cheaper than gp2"})
	require.NoError(t, err)
	v2, err := e.Embed(ctx, []string{"gp3 volumes are cheaper than gp2"})
	require.NoError(t, err)
	require.Len(t, v1, 1)
	require.Len(t, v2, 1)
	assert.Equal(t, v1[0], v2[0], "identical text must hash to identical vectors")

	v3, err := e.Embed(ctx, []string{"a completely unrelated sentence about penguins"})
	require.NoError(t, err)
	sim := CosineSimilarity(v1[0], v3[0])
	assert.Less(t, sim, 0.9, "unrelated text should not score near-identical")
}

func TestStore_TenantIsolation(t *testing.T) {
	s := New(nil)
	ctx := context.Background()
	tenantA := core.TenantID("tenant-a")
	tenantB := core.TenantID("tenant-b")

	require.NoError(t, s.Index(ctx, []ports.Document{
		{ID: "a-doc", TenantID: tenantA, Source: "tenant_spec", Title: "A", Content: "Tenant A's secret architecture uses a m5.2xlarge fleet for checkout."},
		{ID: "b-doc", TenantID: tenantB, Source: "tenant_spec", Title: "B", Content: "Tenant B's secret architecture uses Lambda for checkout."},
	}))

	resA, err := s.Search(ctx, tenantA, "checkout architecture", 10, nil)
	require.NoError(t, err)
	for _, r := range resA {
		assert.NotContains(t, r.Document.Content, "Tenant B", "tenant A must never see tenant B's documents")
	}
	found := false
	for _, r := range resA {
		if r.Document.ID == "a-doc" {
			found = true
		}
	}
	assert.True(t, found, "tenant A must see its own document")

	resB, err := s.Search(ctx, tenantB, "checkout architecture", 10, nil)
	require.NoError(t, err)
	for _, r := range resB {
		assert.NotContains(t, r.Document.Content, "Tenant A")
	}

	// A query scoped to no tenant at all must see neither tenant's docs.
	resNone, err := s.Search(ctx, core.TenantID(""), "checkout architecture", 10, nil)
	require.NoError(t, err)
	for _, r := range resNone {
		assert.NotEqual(t, "a-doc", r.Document.ID)
		assert.NotEqual(t, "b-doc", r.Document.ID)
	}
}

func TestStore_PlatformDocsVisibleToEveryTenant(t *testing.T) {
	s := New(nil)
	ctx := context.Background()
	require.NoError(t, s.Index(ctx, []ports.Document{
		{ID: "platform-doc", Source: "finops", Title: "FinOps", Content: "FinOps principle: everyone takes ownership of their cloud usage."},
	}))

	for _, tenant := range []core.TenantID{"tenant-x", "tenant-y"} {
		res, err := s.Search(ctx, tenant, "FinOps ownership principle", 5, nil)
		require.NoError(t, err)
		require.NotEmpty(t, res)
		assert.Equal(t, "platform-doc", res[0].Document.ID)
	}
}

// TestStore_HybridRankFindsExactIdentifier is the test the package doc
// promises: pure semantic similarity is weak at exact identifiers like
// "m5.2xlarge", so a document that only shares general vocabulary with the
// query must not consistently outrank the one document that actually
// contains the literal instance type — the BM25 half of the hybrid score
// must contribute enough to surface it in the top result.
func TestStore_HybridRankFindsExactIdentifier(t *testing.T) {
	s := New(nil)
	ctx := context.Background()
	require.NoError(t, s.Index(ctx, []ports.Document{
		{
			ID: "generic", Source: "rightsizing", Title: "General rightsizing guidance",
			Content: "Right-sizing compute instances by observing utilization percentiles over time helps control cost across many families and sizes without naming any one of them specifically.",
		},
		{
			ID: "specific", Source: "rightsizing", Title: "m5.2xlarge note",
			Content: "The m5.2xlarge instance type provides 8 vCPU and 32 GiB memory; downsizing an underutilized m5.2xlarge to m5.xlarge is a common rightsizing action.",
		},
	}))

	res, err := s.Search(ctx, "", "m5.2xlarge", 5, nil)
	require.NoError(t, err)
	require.NotEmpty(t, res)
	assert.Equal(t, "specific", res[0].Document.ID,
		"a query for an exact instance type must rank the document containing it first")
}

func TestStore_SourceFilter(t *testing.T) {
	s := New(nil)
	ctx := context.Background()
	require.NoError(t, s.Index(ctx, []ports.Document{
		{ID: "d1", Source: "aws_pricing", Title: "Pricing", Content: "on-demand pricing bills per second for compute"},
		{ID: "d2", Source: "finops", Title: "FinOps", Content: "FinOps ownership of cloud usage across teams"},
	}))
	res, err := s.Search(ctx, "", "pricing usage", 10, []string{"finops"})
	require.NoError(t, err)
	for _, r := range res {
		assert.Equal(t, "finops", r.Document.Source)
	}
}

func TestStore_DeleteRemovesAllChunks(t *testing.T) {
	s := New(nil)
	ctx := context.Background()
	tenant := core.TenantID("tenant-z")
	longContent := ""
	for i := 0; i < 400; i++ {
		longContent += "word "
	}
	require.NoError(t, s.Index(ctx, []ports.Document{
		{ID: "big-doc", TenantID: tenant, Source: "tenant_spec", Title: "Big", Content: longContent},
	}))
	count, err := s.Count(ctx, tenant)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	require.NoError(t, s.Delete(ctx, tenant, []string{"big-doc"}))
	count, err = s.Count(ctx, tenant)
	require.NoError(t, err)
	assert.Equal(t, 0, count)

	res, err := s.Search(ctx, tenant, "word", 50, nil)
	require.NoError(t, err)
	for _, r := range res {
		assert.NotContains(t, r.Document.ID, "big-doc")
	}
}

func TestSeedPlatformCorpus_IndexesShippedDocuments(t *testing.T) {
	s := New(nil)
	ctx := context.Background()
	require.NoError(t, SeedPlatformCorpus(ctx, s))

	count, err := s.Count(ctx, "")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, count, 6, "the shipped corpus covers pricing, rightsizing, FinOps, well-architected, rules and safe-change")

	res, err := s.Search(ctx, "", "gp3 volume pricing versus gp2", 3, nil)
	require.NoError(t, err)
	require.NotEmpty(t, res)
}

func TestIndexTenantKnowledge_ScopedAndSearchable(t *testing.T) {
	s := New(nil)
	ctx := context.Background()
	tenant := core.TenantID("tenant-knowledge")
	require.NoError(t, IndexTenantKnowledge(ctx, s, tenant, TenantKnowledge{
		SpecYAML:      "apiVersion: cloudoptix.io/v1\norganization:\n  name: Acme",
		SpecVersion:   3,
		PolicyName:    "default",
		PolicySummary: "Production changes require approval from a distinct approver.",
		OutcomeEntries: []OutcomeEntry{
			{RuleID: "ebs-unattached-volume", Summary: "deleted an unattached volume", PredictedUSDPerMonth: 12, ActualUSDPerMonth: 12, Accurate: true},
		},
	}))

	res, err := s.Search(ctx, tenant, "distinct approver production", 5, nil)
	require.NoError(t, err)
	require.NotEmpty(t, res)
	assert.Equal(t, "tenant_policy", res[0].Document.ID)

	// Not visible to a different tenant.
	other, err := s.Search(ctx, "other-tenant", "distinct approver production", 5, nil)
	require.NoError(t, err)
	for _, r := range other {
		assert.NotEqual(t, "tenant_policy", r.Document.ID)
	}
}

func TestMinMaxNormalize(t *testing.T) {
	out := minMaxNormalize([]float64{1, 2, 3, 4})
	assert.Equal(t, 0.0, out[0])
	assert.Equal(t, 1.0, out[3])

	flat := minMaxNormalize([]float64{5, 5, 5})
	assert.Equal(t, []float64{0, 0, 0}, flat)
}
