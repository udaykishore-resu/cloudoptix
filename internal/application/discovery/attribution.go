package discovery

import (
	"context"
	"sort"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// ruleTarget pairs one AttributionRule with the application (and, for a
// workload's own rules, the workload) it would assign a matching resource
// to.
type ruleTarget struct {
	rule       cloud.AttributionRule
	appID      core.ID
	workloadID core.ID // zero for an application-level rule
}

// attributionContext is everything applyAttribution needs, loaded once per
// account scan rather than once per resource — a tenant's rule set does not
// change mid-scan, and re-fetching every application and workload for each
// of a few thousand resources would dominate a run's latency for no benefit.
type attributionContext struct {
	account  cloud.AWSAccount
	appRules []ruleTarget
	wlRules  []ruleTarget
}

func (s *Service) loadAttributionContext(ctx context.Context, tenant core.TenantID, account cloud.AWSAccount) (attributionContext, error) {
	apps, err := s.Repos.Applications.ListApplications(ctx, tenant)
	if err != nil {
		return attributionContext{}, err
	}
	ac := attributionContext{account: account}
	for _, app := range apps {
		for _, r := range app.MatchRules {
			ac.appRules = append(ac.appRules, ruleTarget{rule: r, appID: app.ID})
		}
		workloads, werr := s.Repos.Applications.ListWorkloads(ctx, tenant, app.ID)
		if werr != nil {
			continue
		}
		for _, w := range workloads {
			for _, r := range w.MatchRules {
				ac.wlRules = append(ac.wlRules, ruleTarget{rule: r, appID: app.ID, workloadID: w.ID})
			}
		}
	}
	// Lower Priority evaluates first — see cloud.AttributionRule's doc
	// comment: "the first match wins", which only means something once the
	// set is in a defined order.
	sort.SliceStable(ac.appRules, func(i, j int) bool { return ac.appRules[i].rule.Priority < ac.appRules[j].rule.Priority })
	sort.SliceStable(ac.wlRules, func(i, j int) bool { return ac.wlRules[i].rule.Priority < ac.wlRules[j].rule.Priority })
	return ac, nil
}

// applyAttribution resolves Environment, ApplicationID, WorkloadID, Owner,
// CostCenter and Criticality for one discovered resource, in place, before
// it is persisted. Every field records how it was learned via
// core.Provenance (Environment does explicitly; the others are only ever set
// from a tag the resource itself carries or a rule match, both of which are
// facts about this specific resource rather than an inference, so they do
// not need their own provenance field the way Environment's ambiguity
// between "the tag said so" and "the account convention said so" does).
func applyAttribution(r *cloud.Resource, ac attributionContext) {
	resolveEnvironment(r, ac.account)

	// A workload-level rule is more specific than an application-level one,
	// so it is tried first; a workload match implies the workload's own
	// application too, without needing a second, redundant rule for it.
	for _, wr := range ac.wlRules {
		if wr.rule.Matches(*r) {
			r.WorkloadID = wr.workloadID
			r.ApplicationID = wr.appID
			break
		}
	}
	if r.ApplicationID.IsZero() {
		for _, ar := range ac.appRules {
			if ar.rule.Matches(*r) {
				r.ApplicationID = ar.appID
				break
			}
		}
	}

	if v := r.Tags.First("Owner", "owner", "Team", "team"); v != "" {
		r.Owner = v
	}
	if v := r.Tags.First("CostCenter", "cost-center", "costcenter", "cost_center"); v != "" {
		r.CostCenter = v
	}
	if v := r.Tags.First("Criticality", "criticality", "Tier", "tier"); v != "" {
		r.Criticality = normalizeCriticality(v)
	} else if r.Criticality == "" {
		r.Criticality = core.CriticalityUnset
	}
}

// resolveEnvironment tries, in order of trust: a recognised tag on the
// resource (CONFIRMED — a tag is a fact about this specific resource,
// deliberately set by whoever provisioned it); the account's own onboarded
// Environment as a fallback (INFERRED — a convention about the account, not
// a fact about the resource, since a shared non-production account can still
// host one mistagged production canary); or, absent both, UNKNOWN rather
// than a guess.
func resolveEnvironment(r *cloud.Resource, account cloud.AWSAccount) {
	if v := r.Tags.First("Environment", "environment", "Env", "env"); v != "" {
		if env := core.NormalizeEnvironment(v); env != core.EnvUnknown {
			r.Environment = env
			r.EnvironmentSource = core.ProvenanceConfirmed
			return
		}
	}
	if account.Environment != "" && account.Environment != core.EnvUnknown {
		r.Environment = account.Environment
		r.EnvironmentSource = core.ProvenanceInferred
		return
	}
	r.Environment = core.EnvUnknown
	r.EnvironmentSource = core.ProvenanceUnknown
}

func normalizeCriticality(v string) core.Criticality {
	switch core.Normalize(v) {
	case "tier0", "tier_0", "0", "critical", "production-critical", "revenue-critical":
		return core.CriticalityTier0
	case "tier1", "tier_1", "1", "high":
		return core.CriticalityTier1
	case "tier2", "tier_2", "2", "medium":
		return core.CriticalityTier2
	case "tier3", "tier_3", "3", "low", "batch", "best-effort":
		return core.CriticalityTier3
	default:
		return core.CriticalityUnset
	}
}
