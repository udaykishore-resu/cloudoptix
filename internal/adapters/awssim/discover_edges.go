package awssim

import "github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"

// linkRelationships builds the architecture graph edges once every resource
// in the pass has been added (and is therefore in b.ids). It must run last.
func (b *discoveryBuilder) linkRelationships() {
	// Network containment: VPC -> subnet, VPC -> security group.
	for _, s := range b.estate.Subnets {
		b.edge(cloud.RelContains, s.VPCID, s.ID, 1)
	}
	for _, sg := range b.estate.SecurityGroups {
		b.edge(cloud.RelContains, sg.VPCID, sg.ID, 1)
	}

	// Volumes and Elastic IPs attach to their target.
	for _, v := range b.estate.EBSVolumes {
		if v.AttachedTo != "" {
			b.edge(cloud.RelAttachedTo, v.ID, v.AttachedTo, 1)
		}
	}
	for _, ip := range b.estate.ElasticIPs {
		if ip.AttachedTo != "" {
			b.edge(cloud.RelAttachedTo, ip.ID, ip.AttachedTo, 1)
		}
	}

	// EC2 egress path: an instance's internet-bound traffic leaves through
	// its declared NAT gateway. This is what makes NAT cost attributable to
	// the workload that caused it (Topology.Consumers reads egress_via
	// edges), and is also what makes a NAT gateway naturally shared_by many
	// instances without a separate edge kind: many instances egress_via the
	// same three gateways.
	for _, i := range b.estate.EC2Instances {
		if i.NATGatewayID != "" {
			b.edge(cloud.RelEgressVia, i.ID, i.NATGatewayID, 1)
		}
	}

	// EKS: cluster contains its node groups.
	for _, ng := range b.estate.EKSNodeGroups {
		b.edge(cloud.RelContains, ng.ClusterID, ng.ID, 1)
	}

	// ECS: a service runs on its cluster.
	for _, s := range b.estate.ECSServices {
		b.edge(cloud.RelRunsOn, s.ID, s.ClusterID, 1)
	}

	// RDS: Aurora cluster contains its instances; a replica replica_of its
	// primary (also covers the standalone Multi-AZ replica story instance).
	for _, c := range b.estate.RDSClusters {
		for _, memberID := range c.InstanceIDs {
			b.edge(cloud.RelContains, c.ID, memberID, 1)
		}
	}
	for _, r := range b.estate.RDSInstances {
		if r.IsReadReplica && r.PrimaryID != "" {
			b.edge(cloud.RelReplicaOf, r.ID, r.PrimaryID, 1)
		}
	}

	// Load balancing: ALB/NLB routes_to its target group(s); a target group
	// routes_to each of its target instances.
	for _, lb := range b.estate.LoadBalancers {
		for _, tgID := range lb.TargetGroupIDs {
			b.edge(cloud.RelRoutesTo, lb.ID, tgID, 1)
		}
	}
	for _, tg := range b.estate.TargetGroups {
		weight := 1.0
		if n := len(tg.TargetInstanceIDs); n > 0 {
			weight = 1.0 / float64(n)
		}
		for _, instID := range tg.TargetInstanceIDs {
			b.edge(cloud.RelRoutesTo, tg.ID, instID, weight)
		}
	}

	// CDN and API Gateway route to their declared origin/target.
	for _, d := range b.estate.CloudFront {
		if d.OriginID != "" {
			b.edge(cloud.RelRoutesTo, d.ID, d.OriginID, 1)
		}
	}
	for _, a := range b.estate.APIGateways {
		if a.TargetLambdaID != "" {
			b.edge(cloud.RelRoutesTo, a.ID, a.TargetLambdaID, 1)
		}
		if a.TargetALBID != "" {
			b.edge(cloud.RelRoutesTo, a.ID, a.TargetALBID, 1)
		}
	}
}
