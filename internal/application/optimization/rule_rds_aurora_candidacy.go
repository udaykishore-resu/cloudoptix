package optimization

import (
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDRDSAuroraCandidacy flags a standard RDS primary (mysql/postgres)
// carrying multiple read replicas: Aurora's storage-layer replication means
// replicas share the primary's storage rather than each paying full instance
// price for a private copy, so a primary with several replicas is exactly
// the shape that benefits most from migrating. Migration is architectural
// (engine change, connection-string cutover), so this is advisory only.
//
// Traceability: REQ-OPT-005.
const RuleIDRDSAuroraCandidacy optimize.RuleID = "rds-aurora-candidacy"

type ruleRDSAuroraCandidacy struct{}

func NewRDSAuroraCandidacyRule() FullRule { return ruleRDSAuroraCandidacy{} }

func (ruleRDSAuroraCandidacy) ID() optimize.RuleID { return RuleIDRDSAuroraCandidacy }

func (ruleRDSAuroraCandidacy) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDRDSAuroraCandidacy, Name: "Aurora-vs-RDS candidacy", Category: optimize.CategoryArchitecture,
		Action: optimize.ActionAdvisoryOnly,
		Description: "A standard RDS engine carrying multiple read replicas is a candidate for " +
			"Aurora's storage-layer replication.",
		Kinds: []cloud.Kind{cloud.KindRDSInstance}, Enabled: true,
	}
}

func (ruleRDSAuroraCandidacy) Applies(r cloud.Resource) bool {
	return r.Kind == cloud.KindRDSInstance && r.State.Active() && !r.AttrBool("is_read_replica", false) &&
		(r.Engine == "mysql" || r.Engine == "postgres")
}

func countReplicas(ctx EvalContext, primary cloud.Resource) int {
	n := 0
	for _, e := range ctx.Topology.Inbound(primary.ID, cloud.RelReplicaOf) {
		if dep, ok := ctx.Inventory.ByID(e.FromID); ok && dep.Kind == cloud.KindRDSInstance {
			n++
		}
	}
	if n == 0 {
		// Fall back to the attribute-level signal for adapters that have not
		// wired the replica_of edge yet.
		for _, other := range ctx.Inventory.OfKind(cloud.KindRDSInstance) {
			if other.Attr("primary_id", "") == primary.NativeID {
				n++
			}
		}
	}
	return n
}

func decideRDSAuroraCandidacy(ctx EvalContext, r cloud.Resource) (replicas int, replicaCost, auroraEquivalent, saving core.Money, ok bool) {
	replicas = countReplicas(ctx, r)
	minReplicas := ctx.Thresholds.Int(ctx.TenantID, RuleIDRDSAuroraCandidacy, "min_read_replica_count", 2)
	if replicas < minReplicas {
		return
	}
	primaryPrice, ok1 := ctx.Pricing.DatabasePrice(r.Region, r.InstanceType, r.Engine, r.AttrBool("multi_az", false))
	auroraEngine := "aurora-" + r.Engine
	auroraPrimary, ok2 := ctx.Pricing.DatabasePrice(r.Region, r.InstanceType, auroraEngine, false)
	if !ok1 || !ok2 {
		return
	}
	// Each replica currently pays full instance price; Aurora read replicas
	// pay instance price too, but the storage each one would otherwise
	// duplicate is shared, so the saving modelled here is the storage
	// duplication avoided across the replica fleet, priced from the primary's
	// own allocated storage at the RDS gp3 rate as a conservative baseline.
	storagePrice, ok3 := ctx.Pricing.StoragePrice(r.Region, "rds_gp3")
	if !ok3 {
		return
	}
	replicaCost = primaryPrice.Scale(core.HoursPerMonth * float64(replicas))
	duplicatedStorageSaving := storagePrice.Scale(r.Capacity.StorageGiB * float64(replicas))
	saving = duplicatedStorageSaving
	auroraEquivalent = auroraPrimary.Scale(core.HoursPerMonth)
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDRDSAuroraCandidacy, "min_monthly_saving", 50)
	if !MeetsMinSaving(ctx.Spec, minSaving, saving) || ExcludedBySpec(ctx.Spec, r, optimize.ActionAdvisoryOnly) {
		return replicas, core.Money{}, core.Money{}, core.Money{}, false
	}
	return replicas, replicaCost, auroraEquivalent, saving, true
}

func (ruleRDSAuroraCandidacy) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	replicas, replicaCost, auroraEquivalent, saving, ok := decideRDSAuroraCandidacy(ctx, r)
	if !ok {
		return nil, nil
	}
	evidence := []optimize.Evidence{
		TopologyEvidence("read replicas", fmt.Sprintf("%d replicas found via replica_of edges/primary_id attribute", replicas)),
		CostEvidence("replica fleet cost vs Aurora primary", fmt.Sprintf("%s vs %s", replicaCost.Format(), auroraEquivalent.Format()), "pricing_catalog"),
	}
	summary := fmt.Sprintf("%s carries %d read replicas — an Aurora migration candidate", r.DisplayName(), replicas)
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleRDSAuroraCandidacy{}, Resource: r, Severity: core.SeverityInfo,
		Summary: summary, Detail: "Aurora's shared storage layer avoids each replica duplicating the primary's full storage allocation.",
		Evidence: evidence, CurrentCost: CostFor(ctx, r), Saving: saving,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

func (ruleRDSAuroraCandidacy) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	return RuleAction{
		Type:          optimize.ActionAdvisoryOnly,
		Reversibility: optimize.ReversibilityNone,
		Complexity:    optimize.ComplexityProject,
		Title:         fmt.Sprintf("Evaluate Aurora for %s and its replicas", r.DisplayName()),
		Rationale:     f.Detail,
	}
}
