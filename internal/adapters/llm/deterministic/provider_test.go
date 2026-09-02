package deterministic

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

func TestHealthy_AlwaysTrue(t *testing.T) {
	p := New()
	assert.True(t, p.Healthy(context.Background()))
}

func TestComplete_Deterministic_SameInputSameOutput(t *testing.T) {
	p := New()
	req := ports.CompletionRequest{
		Messages: []ports.Message{{Role: ports.RoleUser, Content: "We are Meridian Retail, an e-commerce company. Cut our AWS bill by 25%."}},
		ResponseSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"organization_name":     map[string]any{"type": "string"},
				"industry":              map[string]any{"type": "string"},
				"cost_reduction_target": map[string]any{"type": "number"},
			},
		},
	}
	r1, err := p.Complete(context.Background(), req)
	require.NoError(t, err)
	r2, err := p.Complete(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, r1.Structured, r2.Structured)
}

func TestCompleteExtraction_FillsRecognizedFields(t *testing.T) {
	p := New()
	req := ports.CompletionRequest{
		Messages: []ports.Message{{Role: ports.RoleUser, Content: "We are Meridian Retail, an e-commerce company. Cut our AWS bill by 25%. Our availability target is 99.95%."}},
		ResponseSchema: map[string]any{
			"properties": map[string]any{
				"organization_name":     map[string]any{"type": "string"},
				"industry":              map[string]any{"type": "string"},
				"cost_reduction_target": map[string]any{"type": "number"},
				"availability_target":   map[string]any{"type": "number"},
				"unrecognized_field":    map[string]any{"type": "string"},
			},
		},
	}
	resp, err := p.Complete(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, resp.Structured)
	assert.Equal(t, "Meridian Retail", resp.Structured["organization_name"])
	assert.Equal(t, "e-commerce", resp.Structured["industry"])
	assert.InDelta(t, 0.25, resp.Structured["cost_reduction_target"], 0.001)
	assert.InDelta(t, 0.9995, resp.Structured["availability_target"], 0.0001)
	_, present := resp.Structured["unrecognized_field"]
	assert.False(t, present, "a property with no extractor and no evidence must be absent, not zero-valued")
}

func TestCompleteAgentic_RoutesQuestionToTool(t *testing.T) {
	p := New()
	tools := []ports.ToolDefinition{
		{Name: "get_cost_summary", Description: "cost summary", ReadOnly: true},
		{Name: "get_cost_breakdown", Description: "cost breakdown", ReadOnly: true},
		{Name: "search_knowledge", Description: "search", ReadOnly: true},
	}
	resp, err := p.Complete(context.Background(), ports.CompletionRequest{
		Messages: []ports.Message{{Role: ports.RoleUser, Content: "Which service is most expensive this month?"}},
		Tools:    tools,
	})
	require.NoError(t, err)
	require.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, "get_cost_breakdown", resp.ToolCalls[0].Name)
	assert.Equal(t, "service", resp.ToolCalls[0].Arguments["dimension"])
}

func TestCompleteAgentic_ComposesFinalAnswerFromToolResult(t *testing.T) {
	p := New()
	tools := []ports.ToolDefinition{{Name: "get_cost_breakdown", Description: "cost breakdown", ReadOnly: true}}

	toolResultJSON, err := json.Marshal(map[string]any{
		"summary": "EC2 is the most expensive service at $12,400/mo, 38% of total spend.",
	})
	require.NoError(t, err)

	resp, err := p.Complete(context.Background(), ports.CompletionRequest{
		Messages: []ports.Message{
			{Role: ports.RoleUser, Content: "Which service is most expensive this month?"},
			{Role: ports.RoleAssistant, ToolCalls: []ports.ToolCall{{ID: "det_get_cost_breakdown", Name: "get_cost_breakdown"}}},
			{Role: ports.RoleTool, Name: "get_cost_breakdown", ToolCallID: "det_get_cost_breakdown", Content: string(toolResultJSON)},
		},
		Tools: tools,
	})
	require.NoError(t, err)
	assert.Empty(t, resp.ToolCalls)
	assert.Contains(t, resp.Content, "$12,400/mo")
	assert.Contains(t, resp.Content, "EC2")
}

func TestCompleteAgentic_FallsBackToSearchKnowledgeWhenNothingMatches(t *testing.T) {
	p := New()
	tools := []ports.ToolDefinition{
		{Name: "get_cost_summary", Description: "cost summary", ReadOnly: true},
		{Name: "search_knowledge", Description: "search", ReadOnly: true},
	}
	resp, err := p.Complete(context.Background(), ports.CompletionRequest{
		Messages: []ports.Message{{Role: ports.RoleUser, Content: "asdkjaslkdj random gibberish query"}},
		Tools:    tools,
	})
	require.NoError(t, err)
	require.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, "search_knowledge", resp.ToolCalls[0].Name)
}

func TestCompleteAgentic_StopsAfterMaxRounds(t *testing.T) {
	p := New()
	tools := []ports.ToolDefinition{
		{Name: "get_cost_summary", Description: "x", ReadOnly: true},
		{Name: "get_cost_breakdown", Description: "x", ReadOnly: true},
		{Name: "explain_cost_change", Description: "x", ReadOnly: true},
		{Name: "get_efficiency_score", Description: "x", ReadOnly: true},
	}
	msgs := []ports.Message{{Role: ports.RoleUser, Content: "why did cost increase and what is most expensive and efficiency score"}}
	for i := 0; i < MaxToolRounds; i++ {
		resultJSON, _ := json.Marshal(map[string]any{"summary": "some result"})
		msgs = append(msgs,
			ports.Message{Role: ports.RoleAssistant, ToolCalls: []ports.ToolCall{{ID: "x", Name: "tool"}}},
			ports.Message{Role: ports.RoleTool, Name: toolNameFor(i), ToolCallID: "x", Content: string(resultJSON)},
		)
	}
	resp, err := p.Complete(context.Background(), ports.CompletionRequest{Messages: msgs, Tools: tools})
	require.NoError(t, err)
	assert.Empty(t, resp.ToolCalls, "after MaxToolRounds the provider must compose a final answer, not keep calling tools")
	assert.NotEmpty(t, resp.Content)
}

func toolNameFor(i int) string {
	names := []string{"get_cost_summary", "get_cost_breakdown", "explain_cost_change", "get_efficiency_score"}
	return names[i%len(names)]
}

func TestEmbed_Deterministic(t *testing.T) {
	p := New()
	v1, err := p.Embed(context.Background(), []string{"gp3 volumes"})
	require.NoError(t, err)
	v2, err := p.Embed(context.Background(), []string{"gp3 volumes"})
	require.NoError(t, err)
	assert.Equal(t, v1, v2)
}
