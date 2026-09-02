package compiler

import (
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/simulate"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// priceOutcome is what a single-side pricing function (pricing either the
// Before or the After attribute bag) produces. priceRawResource below calls
// it up to twice per RawResource and assembles a simulate.PricedChange from
// the results.
type priceOutcome struct {
	Monthly        core.Money
	UsageDependent bool
	Unpriced       bool
	UnpricedReason string
	Components     []simulate.PriceComponent
	Assumptions    []simulate.Assumption
	Warnings       []string
}

func unpricedOutcome(reason string, args ...any) priceOutcome {
	return priceOutcome{Unpriced: true, UnpricedReason: fmt.Sprintf(reason, args...)}
}

// priceFunc prices one attribute bag (either the before-state or the
// after-state of a RawResource) for one canonical resource type.
type priceFunc func(pc *pricerCtx, r RawResource, a Attrs) priceOutcome

// pricerCtx is the shared, read-only context every pricing function needs:
// the pricing catalog, the caller's overridable usage assumptions, and the
// cross-resource lookups (launch template instance types, ECS task
// definition shapes) resolved once per Compile() call so individual pricing
// functions never re-scan the whole change set.
type pricerCtx struct {
	pricing         ports.PricingCatalog
	req             ports.CompileRequest
	launchTemplates map[string]Attrs // terraform resource name -> its attrs
	taskDefs        map[string]Attrs // terraform resource name -> its attrs
}

// resolveAssumption returns the usage figure to price a usage-dependent
// resource with: an address-specific override
// (CompileRequest.Assumptions["<address>:<key>"]) takes precedence over a
// change-set-wide override (Assumptions["<key>"]), which takes precedence
// over the built-in default. The bool reports whether either override fired,
// which the caller uses to set the Assumption's Provenance (a user-supplied
// figure is CONFIRMED; the built-in default is INFERRED).
func (pc *pricerCtx) resolveAssumption(address, key string, def float64) (float64, bool) {
	if pc.req.Assumptions != nil {
		if v, ok := pc.req.Assumptions[address+":"+key]; ok {
			return v, true
		}
		if v, ok := pc.req.Assumptions[key]; ok {
			return v, true
		}
	}
	return def, false
}

func usageAssumption(key, label string, value float64, unit string, overridden bool, note string) simulate.Assumption {
	prov := core.ProvenanceInferred
	if overridden {
		prov = core.ProvenanceConfirmed
	}
	return simulate.Assumption{
		Key:        key,
		Label:      label,
		Value:      fmt.Sprintf("%g", value),
		Unit:       unit,
		Provenance: prov,
		Note:       note,
	}
}

func monthlyFromHourly(hourly core.Money) core.Money { return hourly.Scale(core.HoursPerMonth) }

// priceDispatch is the resource-type pricing map: every AWS resource type
// listed in the task's "moves the bill" set, plus the three Kubernetes
// workload kinds. A type absent from this map (and not in
// knownFreeTerraformTypes) becomes Unpriced.
var priceDispatch = map[string]priceFunc{
	"aws_instance":                       priceEC2Instance,
	"aws_autoscaling_group":              priceASG,
	"aws_ebs_volume":                     priceEBSVolume,
	"aws_db_instance":                    priceRDSInstance,
	"aws_rds_cluster":                    priceRDSCluster,
	"aws_rds_cluster_instance":           priceRDSClusterInstance,
	"aws_elasticache_cluster":            priceElastiCacheCluster,
	"aws_elasticache_replication_group":  priceElastiCacheReplicationGroup,
	"aws_dynamodb_table":                 priceDynamoDBTable,
	"aws_s3_bucket":                      priceS3Bucket,
	"aws_lambda_function":                priceLambdaFunction,
	"aws_nat_gateway":                    priceNATGateway,
	"aws_lb":                             priceLB,
	"aws_cloudfront_distribution":        priceCloudFront,
	"aws_apigatewayv2_api":               priceAPIGatewayV2,
	"aws_api_gateway_rest_api":           priceAPIGatewayREST,
	"aws_eks_cluster":                    priceEKSCluster,
	"aws_eks_node_group":                 priceEKSNodeGroup,
	"aws_ecs_service":                    priceECSService,
	"aws_vpc_endpoint":                   priceVPCEndpoint,
	"aws_cloudwatch_log_group":           priceLogGroup,
	"aws_kms_key":                        priceKMSKey,
	"aws_secretsmanager_secret":          priceSecret,
	"aws_sqs_queue":                      priceSQSQueue,
	"aws_msk_cluster":                    priceMSKCluster,
	"aws_eip":                            priceEIP,
	"aws_transit_gateway_vpc_attachment": priceTGWAttachment,
	"k8s_deployment":                     priceK8sWorkload,
	"k8s_statefulset":                    priceK8sWorkload,
	"k8s_daemonset":                      priceK8sWorkload,
}

// priceRawResource prices one changed resource end-to-end: it resolves the
// dispatch function (or the free/unpriced fallback), prices the Before and
// After sides independently, and assembles the simulate.PricedChange —
// including MonthlyDelta, which the domain type carries but does not compute
// for itself (simulate.CompilationResult.Summarize only rolls up totals; the
// per-change delta that the PR comment's "top movers" table sorts by is this
// package's responsibility).
func priceRawResource(pc *pricerCtx, r RawResource) simulate.PricedChange {
	out := simulate.PricedChange{
		Address:      r.Address,
		ResourceType: r.Type,
		Action:       r.Action,
		Region:       r.Region,
	}
	if k, ok := terraformKindHints[r.Type]; ok {
		out.Kind = string(k)
	}
	if r.Action != simulate.ChangeDelete {
		// PricedChange (a domain type this package may not modify) carries no
		// Tags field, yet the cost-regression require_tags check needs to see
		// them and this package owns both the writer and only reader of this
		// warning. See tagWarningPrefix's doc comment.
		out.Warnings = append(out.Warnings, tagsWarning(r.Tags))
	}

	if reason, free := knownFreeTerraformTypes[r.Type]; free {
		out.Warnings = append(out.Warnings, "$0: "+reason)
		out.BeforeMonthly, out.AfterMonthly = core.ZeroUSD(), core.ZeroUSD()
		if r.Action == simulate.ChangeDelete {
			out.AfterMonthly = core.ZeroUSD()
		}
		out.MonthlyDelta = core.ZeroUSD()
		return out
	}

	fn, known := priceDispatch[r.Type]
	if !known {
		out.Unpriced = true
		out.UnpricedReason = fmt.Sprintf("resource type %q is not in the compiler's pricing map", r.Type)
		out.BeforeMonthly, out.AfterMonthly, out.MonthlyDelta = core.ZeroUSD(), core.ZeroUSD(), core.ZeroUSD()
		return out
	}

	out.BeforeMonthly, out.AfterMonthly = core.ZeroUSD(), core.ZeroUSD()
	var chosen priceOutcome
	haveChosen := false
	if r.Before != nil {
		bo := fn(pc, r, r.Before)
		out.BeforeMonthly = bo.Monthly
		chosen = bo
		haveChosen = true
	}
	if r.After != nil {
		ao := fn(pc, r, r.After)
		out.AfterMonthly = ao.Monthly
		// The after-state describes what the resource will look like once
		// the change lands, so its metadata (assumptions, components,
		// warnings) is what a reviewer needs — it supersedes the before-state
		// metadata whenever both exist (an update or replace).
		chosen = ao
		haveChosen = true
	}
	if !haveChosen {
		// Neither side present: a no-op that still reached here, or a
		// malformed input. Treat as free rather than fabricating a reason.
		out.MonthlyDelta = core.ZeroUSD()
		return out
	}

	if chosen.Unpriced {
		out.Unpriced = true
		out.UnpricedReason = chosen.UnpricedReason
		// An unpriced resource contributes nothing to the totals: a fabricated
		// before/after figure for a resource type the catalog cannot price
		// would be worse than an honest gap in coverage.
		out.BeforeMonthly, out.AfterMonthly = core.ZeroUSD(), core.ZeroUSD()
	} else {
		out.UsageDependent = chosen.UsageDependent
		out.PriceComponents = chosen.Components
		out.Assumptions = chosen.Assumptions
	}
	out.Warnings = append(out.Warnings, chosen.Warnings...)
	out.MonthlyDelta = out.AfterMonthly.MustSub(out.BeforeMonthly)
	return out
}
