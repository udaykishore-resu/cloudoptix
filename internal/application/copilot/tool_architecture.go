package copilot

import (
	"context"
	"fmt"
	"strings"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// newQueryArchitectureGraphTool answers "what depends on X" / "what would
// break if we changed X" / general "cheapest architecture" questions about
// shape by walking the discovered dependency graph from one resource, or
// summarizing the graph's overall shape when no resource is named.
func newQueryArchitectureGraphTool(uow ports.UnitOfWork) ports.Tool {
	return architectureGraphTool{baseTool{uow: uow, def: ports.ToolDefinition{
		Name:        "query_architecture_graph",
		Description: "Walks the discovered dependency graph: what a resource depends on, and what depends on it. With no resource given, summarizes the graph's overall shape and its most-connected resources.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"resource_id": map[string]any{"type": "string"},
		}},
		ReadOnly: true, RequiredPermission: core.PermResourceRead,
	}}}
}

type architectureGraphTool struct{ baseTool }

func (t architectureGraphTool) Invoke(ctx context.Context, tenant core.TenantID, args map[string]any) (any, error) {
	ident := firstArgString(args, "resource_id", "id")
	res, err := withRepos(ctx, t.uow, func(ctx context.Context, repos ports.Repositories) (map[string]any, error) {
		topo, err := repos.Resources.LoadTopology(ctx, tenant, ports.ResourceFilter{})
		if err != nil {
			return nil, err
		}
		if ident == "" {
			return summarizeGraph(topo), nil
		}
		var target cloud.Resource
		if core.ID(ident).Prefix() == "res" {
			target, _ = repos.Resources.Get(ctx, tenant, core.ID(ident))
		}
		if target.ID == "" {
			page, lerr := repos.Resources.List(ctx, tenant, ports.ResourceFilter{Search: ident}, ports.ListOptions{Limit: 1})
			if lerr != nil {
				return nil, lerr
			}
			if len(page.Items) == 0 {
				return nil, fmt.Errorf("no resource matches %q", ident)
			}
			target = page.Items[0]
		}
		deps := topo.Outbound(target.ID)
		dependents := topo.Inbound(target.ID)
		var summary strings.Builder
		fmt.Fprintf(&summary, "%s depends on %d resource(s) and has %d resource(s) depending on it.", resourceLabel(target), len(deps), len(dependents))
		if len(dependents) > 0 {
			summary.WriteString(" A change here has the largest blast radius through its dependents.")
		}
		return toolResult(summary.String(), map[string]any{
			"resource_id": target.ID, "depends_on": edgesToAny(deps), "depended_on_by": edgesToAny(dependents),
		}), nil
	})
	if err != nil {
		return toolError("could not query the architecture graph: %v", err), nil
	}
	return res, nil
}

func summarizeGraph(topo *cloud.Topology) map[string]any {
	counts := map[core.ID]int{}
	for _, e := range topo.Edges() {
		counts[e.FromID]++
		counts[e.ToID]++
	}
	summary := fmt.Sprintf("The discovered architecture graph has %d relationship(s) across %d connected resource(s).", topo.Len(), len(counts))
	return toolResult(summary, map[string]any{"edge_count": topo.Len(), "connected_resource_count": len(counts)})
}

func edgesToAny(edges []cloud.Relationship) []map[string]any {
	out := make([]map[string]any, 0, len(edges))
	for _, e := range edges {
		out = append(out, map[string]any{"from_id": e.FromID, "to_id": e.ToID, "kind": e.Kind, "confidence": e.Confidence})
	}
	return out
}
