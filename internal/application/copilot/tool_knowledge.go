package copilot

import (
	"context"
	"fmt"
	"strings"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// newSearchKnowledgeTool answers general "what is", "explain", "best
// practice" questions by retrieving from the RAG corpus (AWS pricing,
// FinOps principles, well-architected guidance, CloudOptix's own rules, and
// this tenant's own policy/spec documents) rather than the model's
// parametric knowledge — every claim the copilot makes about pricing rules
// or FinOps practice traces back to a corpus document, not to whatever the
// underlying model was trained on.
func newSearchKnowledgeTool(store ports.KnowledgeStore) ports.Tool {
	return knowledgeTool{def: ports.ToolDefinition{
		Name:        "search_knowledge",
		Description: "Searches CloudOptix's knowledge base — AWS pricing and service guidance, FinOps principles, well-architected cost optimization, CloudOptix's own rules, and this tenant's own policies — for passages relevant to a question.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"query": map[string]any{"type": "string"},
		}, "required": []string{"query"}},
		ReadOnly: true, RequiredPermission: core.PermCopilotUse,
	}, store: store}
}

type knowledgeTool struct {
	def   ports.ToolDefinition
	store ports.KnowledgeStore
}

func (t knowledgeTool) Definition() ports.ToolDefinition { return t.def }

func (t knowledgeTool) Invoke(ctx context.Context, tenant core.TenantID, args map[string]any) (any, error) {
	query := argString(args, "query", "")
	if query == "" {
		return toolError("query is required"), nil
	}
	if t.store == nil {
		return toolError("the knowledge base is not available"), nil
	}
	hits, err := t.store.Search(ctx, tenant, query, 4, nil)
	if err != nil {
		return toolError("could not search the knowledge base: %v", err), nil
	}
	if len(hits) == 0 {
		return toolResult("No knowledge base passages matched that question.", nil), nil
	}
	var parts []string
	for _, h := range hits {
		snippet := h.Snippet
		if snippet == "" {
			snippet = h.Document.Content
		}
		snippet = sanitizeRetrievedText(snippet)
		parts = append(parts, fmt.Sprintf("%q: %s", h.Document.Title, truncate(snippet, 400)))
	}
	summary := "From the knowledge base — " + strings.Join(parts, " | ")
	return toolResult(summary, map[string]any{"documents": documentsToAny(hits)}), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func documentsToAny(hits []ports.RetrievedDocument) []map[string]any {
	out := make([]map[string]any, 0, len(hits))
	for _, h := range hits {
		out = append(out, map[string]any{
			"id": h.Document.ID, "title": h.Document.Title, "source": h.Document.Source, "score": h.Score,
		})
	}
	return out
}
