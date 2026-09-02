package awssim

import (
	"fmt"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
)

// endpointNativeID derives a deterministic native id for the interface VPC
// endpoint create_vpc_endpoint creates for one NAT gateway, so Apply,
// isApplied and Rollback all agree on the same id without needing to
// thread a freshly-minted one through the plan.
func endpointNativeID(natID string) string { return "vpce-" + natID }

// createVPCEndpointSpec targets a NAT gateway whose traffic is dominated
// by S3 calls that never needed to cross the NAT at all. It creates an
// interface VPC endpoint for S3 and shifts natS3TrafficShare of the
// gateway's processed bytes onto it — the same fraction natWaste already
// charges the NAT gateway for in the waste breakdown, so the executor's
// saving and the rule's estimate agree by construction.
var createVPCEndpointSpec = actionSpec{
	action:          optimize.ActionCreateVPCEndpoint,
	requiredActions: []string{"ec2:DescribeNatGateways", "ec2:DescribeSubnets", "ec2:CreateVpcEndpoint"},
	kind:            cloud.KindNATGateway,
	awsAction:       "ec2:CreateVpcEndpoint",
	titleFmt:        "Create an S3 VPC endpoint to offload NAT gateway %s",

	rollbackFeasible: true,
	dataLossRisk:     core.RiskNone,

	captureState: func(e *Estate, id string) (map[string]any, bool) {
		n, ok := e.NATGateways[id]
		if !ok {
			return nil, false
		}
		return map[string]any{"gb_processed_per_month": n.GBProcessedPerMonth}, true
	},
	isApplied: func(e *Estate, id string, params map[string]any) bool {
		_, exists := e.VPCEndpoints[endpointNativeID(id)]
		return exists
	},
	mutate: func(e *Estate, id string, params map[string]any) (map[string]any, error) {
		n := e.NATGateways[id]
		svc, ok := paramStr(params, "service_name")
		if !ok || svc == "" {
			svc = fmt.Sprintf("com.amazonaws.%s.s3", n.Region)
		}
		vpcID := ""
		if sn, ok := e.Subnets[n.SubnetID]; ok {
			vpcID = sn.VPCID
		}
		epID := endpointNativeID(id)
		e.VPCEndpoints[epID] = &VPCEndpoint{
			Base: Base{
				ID: epID, Name: fmt.Sprintf("vpce-s3-%s", n.AZ), Region: n.Region, AZ: n.AZ,
				State: cloud.StateAvailable, Tags: core.Tags{}, CreatedAt: time.Now().UTC(),
			},
			VPCID: vpcID, ServiceName: svc,
		}
		prevGB := n.GBProcessedPerMonth
		n.GBProcessedPerMonth = n.GBProcessedPerMonth * (1 - natS3TrafficShare)
		return map[string]any{
			"vpc_endpoint_id": epID, "previous_gb_processed": prevGB, "gb_processed_per_month": n.GBProcessedPerMonth,
			"new_monthly_cost_micros": addMoney(e.NATGatewayMonthlyCost(n), e.VPCEndpointMonthlyCost(e.VPCEndpoints[epID])).Micros(),
		}, nil
	},
	restore: func(e *Estate, id string, before map[string]any) error {
		n, ok := e.NATGateways[id]
		if !ok {
			return core.NotFound("nat_gateway", id)
		}
		delete(e.VPCEndpoints, endpointNativeID(id))
		gb, ok := paramFloat(before, "gb_processed_per_month")
		if !ok {
			return core.Invalid("rollback snapshot for %s is missing gb_processed_per_month", id)
		}
		n.GBProcessedPerMonth = gb
		return nil
	},
}
