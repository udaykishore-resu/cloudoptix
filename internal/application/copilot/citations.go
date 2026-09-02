package copilot

import (
	"fmt"
	"strings"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// groundingBuilder accumulates the universe of entities and figures an
// answer this turn is allowed to reference, from every tool result the
// agentic loop actually received. It is built incrementally, one tool
// result at a time, and handed to the GroundingVerifier once the loop
// decides to stop calling tools — it is deliberately never populated from
// anything the model said, only from what CloudOptix's own tools returned.
type groundingBuilder struct {
	set       ports.GroundingSet
	citations []ports.Citation
}

func newGroundingBuilder() *groundingBuilder {
	return &groundingBuilder{set: ports.GroundingSet{
		ResourceIDs:     map[string]string{},
		ResourceNames:   map[string]bool{},
		Services:        map[string]bool{},
		Recommendations: map[string]bool{},
		Applications:    map[string]bool{},
		Transactions:    map[string]bool{},
	}}
}

// absorb reads one tool's structured result payload and extends both the
// grounding set (for verification) and the citation list (for the UI).
//
// KEY DESIGN DECISION — this walks the result structure generically by key
// naming convention rather than requiring every tool's payload to conform
// to one fixed schema. Sixteen tools each return a different shape of
// structured data (a breakdown has "items", a resource lookup has
// "resource", a recommendation list has "recommendations"), and a verifier
// that only recognized a hand-maintained list of exact field names would
// silently under-ground every new field a tool grows over time. Instead:
// any key ending in "_usd" holding a number is a known dollar amount; any
// key named "id" or ending in "_id" holding a string or core.ID is a known
// identifier; "key", "label", "name" and "title" name a known
// resource/service. This is deliberately over-inclusive (it is fine to add
// more grounded facts than an answer ends up using) — the correctness bar
// GroundingVerifier enforces is that nothing outside this set gets stated,
// not that this set is minimal.
func (g *groundingBuilder) absorb(toolName string, result map[string]any) {
	g.walk(result)
	if summary, ok := result["summary"].(string); ok && summary != "" {
		g.citations = append(g.citations, ports.Citation{Kind: "tool_result", ID: toolName, Label: toolName, Value: summary})
	}
}

func (g *groundingBuilder) walk(v any) {
	switch val := v.(type) {
	case map[string]any:
		for k, fv := range val {
			g.absorbField(k, fv)
			g.walk(fv)
		}
	case []map[string]any:
		for _, m := range val {
			g.walk(m)
		}
	case []any:
		for _, e := range val {
			g.walk(e)
		}
	}
}

func (g *groundingBuilder) absorbField(key string, v any) {
	switch {
	case strings.HasSuffix(key, "_usd"):
		if n, ok := asNumber(v); ok {
			g.addAmount(n)
		}
	case key == "id" || strings.HasSuffix(key, "_id"):
		if id, ok := asIdentifier(v); ok && id != "" {
			g.set.ResourceIDs[id] = ""
			g.set.Recommendations[id] = true
			g.set.Applications[id] = true
			g.set.Transactions[id] = true
		}
	case key == "key" || key == "label" || key == "name" || key == "title" || key == "transaction":
		if s, ok := v.(string); ok && s != "" {
			g.set.ResourceNames[s] = true
			g.set.Services[s] = true
		}
	}
}

// asNumber tolerates every numeric shape a tool result or its JSON
// round-trip through a message can carry.
func asNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

// asIdentifier tolerates both a plain string and a core.ID (or similar
// named-string type reported via fmt.Stringer) in an "id"/"*_id" field —
// tool code sometimes stores a domain ID type directly rather than
// pre-converting it to string.
func asIdentifier(v any) (string, bool) {
	switch id := v.(type) {
	case string:
		return id, true
	case fmt.Stringer:
		return id.String(), true
	case core.ID:
		return string(id), true
	case core.AccountID:
		return string(id), true
	}
	return "", false
}

func (g *groundingBuilder) addAmount(usd float64) {
	g.set.Amounts = append(g.set.Amounts, core.USDollars(usd))
}

// citationSummary renders a short label for a Citation list, used in
// degraded-mode answers that skip the model but still want to name what was
// consulted.
func citationSummary(cites []ports.Citation) string {
	if len(cites) == 0 {
		return ""
	}
	return fmt.Sprintf("(%d source%s consulted)", len(cites), plural(len(cites)))
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
