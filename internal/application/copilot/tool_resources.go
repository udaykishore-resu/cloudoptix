package copilot

import (
	"context"
	"fmt"
	"strings"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// newListResourcesTool answers "what resources do we have" / "list our EC2
// instances in prod" — a filtered, paged inventory view.
func newListResourcesTool(uow ports.UnitOfWork) ports.Tool {
	return listResourcesTool{baseTool{uow: uow, def: ports.ToolDefinition{
		Name:        "list_resources",
		Description: "Lists cloud resources, optionally filtered by kind (e.g. aws.ec2.instance), environment, region or a free-text search of name/native id.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"kind":        map[string]any{"type": "string", "description": "e.g. aws.ec2.instance, aws.rds.instance, aws.s3.bucket"},
			"environment": map[string]any{"type": "string", "description": "production|staging|development|..."},
			"search":      map[string]any{"type": "string", "description": "free-text match against name or native id"},
			"limit":       map[string]any{"type": "number"},
		}},
		ReadOnly: true, RequiredPermission: core.PermResourceRead,
	}}}
}

type listResourcesTool struct{ baseTool }

func (t listResourcesTool) Invoke(ctx context.Context, tenant core.TenantID, args map[string]any) (any, error) {
	filter := ports.ResourceFilter{Search: argString(args, "search", "")}
	if k := argString(args, "kind", ""); k != "" {
		filter.Kinds = []cloud.Kind{cloud.Kind(k)}
	}
	if env := argString(args, "environment", ""); env != "" {
		filter.Environments = []core.Environment{core.Environment(env)}
	}
	limit := argInt(args, "limit", 10)
	res, err := withRepos(ctx, t.uow, func(ctx context.Context, repos ports.Repositories) (map[string]any, error) {
		page, err := repos.Resources.List(ctx, tenant, filter, ports.ListOptions{Limit: limit})
		if err != nil {
			return nil, err
		}
		if len(page.Items) == 0 {
			return toolResult("No resources matched that filter.", nil), nil
		}
		var parts []string
		for i, r := range page.Items {
			if i >= 5 {
				break
			}
			parts = append(parts, fmt.Sprintf("%s (%s, %s, %s/mo)", resourceLabel(r), r.Kind, r.Region, money(r.MonthlyCost)))
		}
		summary := fmt.Sprintf("Found %d matching resource(s). ", len(page.Items))
		if page.Total > len(page.Items) {
			summary = fmt.Sprintf("Found %d matching resource(s) (showing %d). ", page.Total, len(page.Items))
		}
		summary += "Examples: " + strings.Join(parts, "; ") + "."
		return toolResult(summary, map[string]any{"resources": resourcesToAny(page.Items)}), nil
	})
	if err != nil {
		return toolError("could not list resources: %v", err), nil
	}
	return res, nil
}

// newGetResourceTool answers "tell me about instance i-0abc..." — a single
// resource's detail, resolved by id, ARN, native id or name.
func newGetResourceTool(uow ports.UnitOfWork) ports.Tool {
	return getResourceTool{baseTool{uow: uow, def: ports.ToolDefinition{
		Name:        "get_resource",
		Description: "Looks up one resource by its CloudOptix id, ARN, native id (e.g. i-0abc123) or name, and returns its cost, state and ownership.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"resource_id": map[string]any{"type": "string", "description": "CloudOptix id, ARN, native id or name"},
		}, "required": []string{"resource_id"}},
		ReadOnly: true, RequiredPermission: core.PermResourceRead,
	}}}
}

type getResourceTool struct{ baseTool }

func (t getResourceTool) Invoke(ctx context.Context, tenant core.TenantID, args map[string]any) (any, error) {
	ident := firstArgString(args, "resource_id", "id")
	if ident == "" {
		return toolError("resource_id is required"), nil
	}
	res, err := withRepos(ctx, t.uow, func(ctx context.Context, repos ports.Repositories) (map[string]any, error) {
		var r cloud.Resource
		var err error
		if core.ID(ident).Prefix() == "res" {
			r, err = repos.Resources.Get(ctx, tenant, core.ID(ident))
		}
		if err != nil || r.ID == "" {
			page, lerr := repos.Resources.List(ctx, tenant, ports.ResourceFilter{Search: ident}, ports.ListOptions{Limit: 1})
			if lerr != nil {
				return nil, lerr
			}
			if len(page.Items) == 0 {
				return nil, fmt.Errorf("no resource matches %q", ident)
			}
			r = page.Items[0]
		}
		summary := fmt.Sprintf("%s is a %s %s in %s (%s), currently %s, costing %s/mo.",
			resourceLabel(r), r.Environment, r.Kind, r.Region, r.AccountID, r.State, money(r.MonthlyCost))
		if r.InstanceType != "" {
			summary += fmt.Sprintf(" Instance type: %s.", r.InstanceType)
		}
		if r.Owner != "" {
			summary += fmt.Sprintf(" Owner: %s.", r.Owner)
		}
		return toolResult(summary, map[string]any{"resource": resourceToAny(r)}), nil
	})
	if err != nil {
		return toolError("could not find resource %q: %v", ident, err), nil
	}
	return res, nil
}

func resourceLabel(r cloud.Resource) string {
	if r.Name != "" {
		return r.Name
	}
	if r.NativeID != "" {
		return r.NativeID
	}
	return string(r.ID)
}

func resourceToAny(r cloud.Resource) map[string]any {
	return map[string]any{
		"id": r.ID, "kind": r.Kind, "name": resourceLabel(r), "region": r.Region,
		"account_id": r.AccountID, "environment": r.Environment, "state": r.State,
		"instance_type": r.InstanceType, "monthly_cost_usd": r.MonthlyCost.Units(),
		"owner": r.Owner, "criticality": r.Criticality,
	}
}

func resourcesToAny(items []cloud.Resource) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, r := range items {
		out = append(out, resourceToAny(r))
	}
	return out
}
