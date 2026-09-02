package compiler

import (
	"encoding/json"
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/simulate"
)

// tfPlan is the subset of `terraform show -json` output this compiler reads.
// The full schema carries configuration, planned_values, prior_state and
// output_changes too; resource_changes is the one section that already
// carries a before/after diff per resource, which is exactly what a cost
// compiler needs and the only section this parser touches.
type tfPlan struct {
	FormatVersion   string           `json:"format_version"`
	ResourceChanges []tfResourceDiff `json:"resource_changes"`
}

type tfResourceDiff struct {
	Address      string   `json:"address"`
	Type         string   `json:"type"`
	Name         string   `json:"name"`
	ProviderName string   `json:"provider_name"`
	Change       tfChange `json:"change"`
}

type tfChange struct {
	Actions []string       `json:"actions"`
	Before  map[string]any `json:"before"`
	After   map[string]any `json:"after"`
}

// tfAction maps Terraform's action-verb list onto simulate.ChangeAction. A
// two-element list is always a replace regardless of ordering
// (create-before-destroy vs destroy-before-create); Terraform never emits any
// other two-element combination.
func tfAction(actions []string) (simulate.ChangeAction, bool) {
	switch len(actions) {
	case 0:
		return simulate.ChangeNoOp, false
	case 1:
		switch actions[0] {
		case "create":
			return simulate.ChangeCreate, true
		case "update":
			return simulate.ChangeUpdate, true
		case "delete":
			return simulate.ChangeDelete, true
		case "no-op":
			return simulate.ChangeNoOp, false
		case "read":
			// A data source read. Data sources are never billed resources in
			// their own right, so they carry no cost information at all.
			return simulate.ChangeNoOp, false
		default:
			return simulate.ChangeNoOp, false
		}
	default:
		// ["create","delete"] or ["delete","create"]: a replace either way.
		return simulate.ChangeReplace, true
	}
}

// ParseTerraformPlanJSON reads `terraform show -json` output and returns one
// RawResource per resource_changes entry that represents a real change.
// Terraform plan JSON already expands count and for_each into distinct
// resource_changes entries (module.web.aws_instance.api[0],
// module.web.aws_instance.api[1], ...), so no explicit expansion step is
// needed here — the compiler's count/for_each CostRisk detection instead
// groups these back together by their shared BaseAddress.
func ParseTerraformPlanJSON(content []byte, fallbackRegion core.Region) ([]RawResource, []string, error) {
	var plan tfPlan
	if err := json.Unmarshal(content, &plan); err != nil {
		return nil, nil, fmt.Errorf("compiler: invalid terraform plan JSON: %w", err)
	}
	if plan.ResourceChanges == nil {
		return nil, nil, fmt.Errorf("compiler: input has no resource_changes array; is this `terraform show -json` output?")
	}
	var out []RawResource
	var warnings []string
	for _, rc := range plan.ResourceChanges {
		action, keep := tfAction(rc.Change.Actions)
		if !keep {
			continue
		}
		before := Attrs(rc.Change.Before)
		after := Attrs(rc.Change.After)
		eff := after
		if action == simulate.ChangeDelete {
			eff = before
		}
		region := regionFromAttrs(eff, fallbackRegion)
		out = append(out, RawResource{
			Address: rc.Address,
			Type:    rc.Type,
			Action:  action,
			Region:  region,
			Before:  before,
			After:   after,
			Tags:    eff.Tags(),
		})
	}
	return out, warnings, nil
}

// regionFromAttrs recovers a resource's region from whatever clue its
// attributes carry (an explicit "region" attribute, or an availability_zone
// whose last character is the AZ letter) before falling back to the request's
// declared region. Terraform resources almost never carry region explicitly
// — it comes from the provider block, which plan JSON does not expose per
// resource — so the availability_zone fallback is the common case for zonal
// resources (EC2, EBS), and the request's region is the common case for
// regional ones (S3, DynamoDB, Lambda).
func regionFromAttrs(a Attrs, fallback core.Region) core.Region {
	if r := a.Str("region", ""); r != "" {
		return core.Region(r)
	}
	if az := a.Str("availability_zone", ""); len(az) > 1 {
		last := az[len(az)-1]
		if last >= 'a' && last <= 'f' {
			return core.Region(az[:len(az)-1])
		}
	}
	return fallback
}
