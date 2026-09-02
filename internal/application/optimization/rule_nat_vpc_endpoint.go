package optimization

import (
	"fmt"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RuleIDNATVPCEndpoint flags a NAT gateway whose processed-bytes charge is
// dominated by traffic to S3 or DynamoDB. Gateway endpoints for those two
// services carry no hourly or per-GB charge, so routing that share of
// traffic through one eliminates the corresponding NAT data-processing
// charge exactly — the saving is the S3/DynamoDB share of processed bytes
// times the NAT's own per-GB rate, not an estimate.
//
// The S3/DynamoDB traffic share is read from an attribute
// (s3_dynamodb_traffic_fraction) that a flow-log-aware discovery adapter
// attaches; this package's reference simulator does not populate it, so the
// rule requires the attribute present rather than guessing a fraction —
// see decideNATVPCEndpoint.
//
// Traceability: REQ-OPT-007.
const RuleIDNATVPCEndpoint optimize.RuleID = "nat-gateway-vpc-endpoint-opportunity"

type ruleNATVPCEndpoint struct{}

func NewNATVPCEndpointRule() FullRule { return ruleNATVPCEndpoint{} }

func (ruleNATVPCEndpoint) ID() optimize.RuleID { return RuleIDNATVPCEndpoint }

func (ruleNATVPCEndpoint) Info() ports.RuleInfo {
	return ports.RuleInfo{
		ID: RuleIDNATVPCEndpoint, Name: "NAT data processing dominated by S3/DynamoDB traffic",
		Category: optimize.CategoryNetwork, Action: optimize.ActionCreateVPCEndpoint,
		Description: "A free VPC gateway endpoint eliminates the NAT data-processing charge for S3/DynamoDB traffic entirely.",
		Kinds:       []cloud.Kind{cloud.KindNATGateway}, Enabled: true,
	}
}

func (ruleNATVPCEndpoint) Applies(r cloud.Resource) bool {
	return r.Kind == cloud.KindNATGateway && r.State.Active()
}

func decideNATVPCEndpoint(ctx EvalContext, r cloud.Resource) (totalGB, fraction float64, saving core.Money, ok bool) {
	totalGB = parseFloatAttr(r.Attr("gb_processed_month", ""), -1)
	fraction = parseFloatAttr(r.Attr("s3_dynamodb_traffic_fraction", ""), -1)
	if totalGB < 0 || fraction < 0 {
		return 0, 0, core.Money{}, false
	}
	minFraction := ctx.Thresholds.Float(ctx.TenantID, RuleIDNATVPCEndpoint, "min_s3_dynamo_fraction", 0.25)
	if fraction < minFraction {
		return totalGB, fraction, core.Money{}, false
	}
	gbPrice, found := ctx.Pricing.ServicePrice(r.Region, "nat_gateway", "gb_processed")
	if !found {
		return totalGB, fraction, core.Money{}, false
	}
	saving = gbPrice.Scale(totalGB * fraction)
	minSaving := ctx.Thresholds.Float(ctx.TenantID, RuleIDNATVPCEndpoint, "min_monthly_saving", 5)
	if !MeetsMinSaving(ctx.Spec, minSaving, saving) || ExcludedBySpec(ctx.Spec, r, optimize.ActionCreateVPCEndpoint) {
		return totalGB, fraction, core.Money{}, false
	}
	return totalGB, fraction, saving, true
}

func (ruleNATVPCEndpoint) Evaluate(ctx EvalContext, r cloud.Resource) ([]optimize.Finding, error) {
	totalGB, fraction, saving, ok := decideNATVPCEndpoint(ctx, r)
	if !ok {
		return nil, nil
	}
	evidence := []optimize.Evidence{
		ConfigEvidence("processed GB / month", fmt.Sprintf("%.0f", totalGB)),
		ConfigEvidence("S3/DynamoDB share of processed bytes", fmt.Sprintf("%.0f%%", fraction*100)),
	}
	summary := fmt.Sprintf("%s's processed bytes are %.0f%% S3/DynamoDB traffic — a gateway endpoint eliminates that charge", r.DisplayName(), fraction*100)
	f, err := NewFinding(ctx, findingInput{
		Rule: ruleNATVPCEndpoint{}, Resource: r, Severity: core.SeverityInfo,
		Summary: summary, Detail: "S3 and DynamoDB gateway endpoints carry no hourly or per-GB charge.",
		Evidence: evidence, CurrentCost: CostFor(ctx, r), Saving: saving,
	})
	if err != nil {
		return nil, err
	}
	return []optimize.Finding{f}, nil
}

// GatewayEndpointServiceName is the AWS service name of a gateway VPC
// endpoint, in the exact form ec2:CreateVpcEndpoint's ServiceName field
// takes. The create_vpc_endpoint executor creates one endpoint per call and
// reads a single "service_name" string, so this is what the rule emits — one
// endpoint, named the way the API names it.
func GatewayEndpointServiceName(region core.Region, service string) string {
	return fmt.Sprintf("com.amazonaws.%s.%s", region, service)
}

func (ruleNATVPCEndpoint) BuildAction(ctx EvalContext, r cloud.Resource, f optimize.Finding) RuleAction {
	return RuleAction{
		Type: optimize.ActionCreateVPCEndpoint,
		// service_name, singular, is the executor's vocabulary: both the
		// simulated and the live executor create exactly one gateway endpoint
		// per plan and read one service name. The rule previously emitted
		// services: []string{"s3","dynamodb"}, a key no executor reads and a
		// shape no executor could consume even if it did — the plan reached
		// the mutate step and fell back to a default, so the recommendation's
		// own choice of services never actually reached AWS. S3 is the
		// endpoint named here because it is the dominant share of the
		// S3/DynamoDB traffic this rule fires on; the DynamoDB gateway
		// endpoint is the same action against the same VPC with the other
		// service name, which a reviewer can raise separately.
		Parameters: map[string]any{
			"vpc_id":       r.Attr("vpc_id", ""),
			"service_name": GatewayEndpointServiceName(r.Region, "s3"),
		},
		Reversibility: optimize.ReversibilityInstant,
		Complexity:    optimize.ComplexityLow,
		Title:         fmt.Sprintf("Add an S3 gateway endpoint alongside %s", r.DisplayName()),
		Rationale:     f.Detail,
	}
}
