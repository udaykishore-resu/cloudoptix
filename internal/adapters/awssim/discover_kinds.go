package awssim

import (
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// This file holds discoveryBuilder's per-category walks. Split out of
// discover.go purely for size — every method here follows the same shape:
// range the estate map for one kind, filter by region, call add(), then
// (in linkRelationships) wire the edges that reference this kind.

func (b *discoveryBuilder) discoverNetwork() {
	for _, v := range b.estate.VPCs {
		if !inRegion(b.in, v.Region) {
			continue
		}
		b.add(cloud.KindVPC, v.ID, v.Name, v.Region, v.AZ, v.State, v.Tags, "", "",
			cloud.Capacity{}, cloud.PurchaseUnknown, attrs("cidr", v.CIDR), v.CreatedAt, core.ZeroUSD())
	}
	for _, s := range b.estate.Subnets {
		if !inRegion(b.in, s.Region) {
			continue
		}
		b.add(cloud.KindSubnet, s.ID, s.Name, s.Region, s.AZ, s.State, s.Tags, "", "",
			cloud.Capacity{}, cloud.PurchaseUnknown, attrs("cidr", s.CIDR, "vpc_id", s.VPCID), s.CreatedAt, core.ZeroUSD())
	}
	for _, sg := range b.estate.SecurityGroups {
		if !inRegion(b.in, sg.Region) {
			continue
		}
		b.add(cloud.KindSecurityGroup, sg.ID, sg.Name, sg.Region, sg.AZ, sg.State, sg.Tags, "", "",
			cloud.Capacity{}, cloud.PurchaseUnknown, attrs("vpc_id", sg.VPCID), sg.CreatedAt, core.ZeroUSD())
	}
	for _, ep := range b.estate.VPCEndpoints {
		if !inRegion(b.in, ep.Region) {
			continue
		}
		b.add(cloud.KindVPCEndpoint, ep.ID, ep.Name, ep.Region, ep.AZ, ep.State, ep.Tags, "", "",
			cloud.Capacity{}, cloud.PurchaseUnknown,
			attrs("vpc_id", ep.VPCID, "service_name", ep.ServiceName), ep.CreatedAt,
			b.estate.VPCEndpointMonthlyCost(ep))
	}
}

func (b *discoveryBuilder) discoverEC2() {
	for _, i := range b.estate.EC2Instances {
		if !inRegion(b.in, i.Region) {
			continue
		}
		spec, _ := b.estate.Catalog.InstanceSpec(i.InstanceType)
		cap := cloud.Capacity{VCPU: spec.VCPU, MemoryGiB: spec.MemoryGiB, NetworkGbps: spec.NetworkGbps, InstanceCount: 1}
		a := attrs("profile", string(i.Profile), "cpu_p50", fstr(i.CPUBaselineP50), "architecture", spec.Architecture)
		if i.StoppedAt != nil {
			a["stopped_at"] = i.StoppedAt.Format("2006-01-02")
		}
		b.add(cloud.KindEC2Instance, i.ID, i.Name, i.Region, i.AZ, i.State, i.Tags, i.InstanceType, "",
			cap, cloud.PurchaseOnDemand, a, i.CreatedAt, b.estate.InstanceMonthlyCost(i))
	}
}

func (b *discoveryBuilder) discoverStorage() {
	for _, v := range b.estate.EBSVolumes {
		if !inRegion(b.in, v.Region) {
			continue
		}
		state := cloud.StateAvailable
		if v.AttachedTo != "" {
			state = cloud.StateInUse
		}
		cap := cloud.Capacity{StorageGiB: v.SizeGiB, ProvisionedIOPS: v.IOPS, ThroughputMiBps: v.ThroughputMiBps}
		b.add(cloud.KindEBSVolume, v.ID, v.Name, v.Region, v.AZ, state, v.Tags, v.VolumeType, "",
			cap, cloud.PurchaseOnDemand, attrs("encrypted", boolStr(v.Encrypted)), v.CreatedAt,
			b.estate.VolumeMonthlyCost(v))
	}
	for _, s := range b.estate.EBSSnapshots {
		if !inRegion(b.in, s.Region) {
			continue
		}
		b.add(cloud.KindEBSSnapshot, s.ID, s.Name, s.Region, "", cloud.StateAvailable, s.Tags, "", "",
			cloud.Capacity{StorageGiB: s.SizeGiB}, cloud.PurchaseUnknown,
			attrs("volume_id", s.VolumeID, "age_days", fstr(float64(int(demoNow.Sub(s.CreatedAt).Hours()/24)))),
			s.CreatedAt, b.estate.SnapshotMonthlyCost(s))
	}
	for _, ip := range b.estate.ElasticIPs {
		if !inRegion(b.in, ip.Region) {
			continue
		}
		state := cloud.StateAvailable
		if ip.AttachedTo != "" {
			state = cloud.StateInUse
		}
		b.add(cloud.KindElasticIP, ip.ID, ip.Name, ip.Region, "", state, ip.Tags, "", "",
			cloud.Capacity{}, cloud.PurchaseUnknown, attrs("public_ip", ip.PublicIP), ip.CreatedAt,
			b.estate.ElasticIPMonthlyCost(ip))
	}
	for _, a := range b.estate.AMIs {
		if !inRegion(b.in, a.Region) {
			continue
		}
		b.add(cloud.KindAMI, a.ID, a.Name, a.Region, "", cloud.StateAvailable, a.Tags, "", "",
			cloud.Capacity{StorageGiB: a.SizeGiB}, cloud.PurchaseUnknown, nil, a.CreatedAt,
			b.estate.AMIMonthlyCost(a))
	}
}

func (b *discoveryBuilder) discoverRDS() {
	for _, r := range b.estate.RDSInstances {
		if !inRegion(b.in, r.Region) {
			continue
		}
		cap := cloud.Capacity{StorageGiB: r.StorageGiB, ReadReplicas: 0}
		a := attrs("multi_az", boolStr(r.MultiAZ), "storage_type", r.StorageType, "is_read_replica", boolStr(r.IsReadReplica))
		if r.PrimaryID != "" {
			a["primary_id"] = r.PrimaryID
		}
		b.add(cloud.KindRDSInstance, r.ID, r.Name, r.Region, r.AZ, r.State, r.Tags, r.InstanceClass, r.Engine,
			cap, cloud.PurchaseOnDemand, a, r.CreatedAt, b.estate.RDSInstanceMonthlyCost(r))
	}
	for _, c := range b.estate.RDSClusters {
		if !inRegion(b.in, c.Region) {
			continue
		}
		b.add(cloud.KindRDSCluster, c.ID, c.Name, c.Region, "", cloud.StateAvailable, c.Tags, "", c.Engine,
			cloud.Capacity{InstanceCount: len(c.InstanceIDs)}, cloud.PurchaseOnDemand, nil, c.CreatedAt, core.ZeroUSD())
	}
	for _, s := range b.estate.RDSSnapshots {
		if !inRegion(b.in, s.Region) {
			continue
		}
		b.add(cloud.KindRDSSnapshot, s.ID, s.Name, s.Region, "", cloud.StateAvailable, s.Tags, "", "",
			cloud.Capacity{StorageGiB: s.SizeGiB}, cloud.PurchaseUnknown, attrs("source_id", s.SourceID),
			s.CreatedAt, b.estate.RDSSnapshotMonthlyCost(s))
	}
}

func (b *discoveryBuilder) discoverDynamoDB() {
	for _, t := range b.estate.DynamoDBTables {
		if !inRegion(b.in, t.Region) {
			continue
		}
		cap := cloud.Capacity{StorageGiB: t.SizeGiB, WriteCapacityRCU: t.RCU, ReadCapacityWCU: t.WCU}
		purchase := cloud.PurchaseOnDemand
		if t.BillingMode == "provisioned" {
			purchase = cloud.PurchaseReserved
		}
		b.add(cloud.KindDynamoDBTable, t.ID, t.Name, t.Region, "", t.State, t.Tags, "", "",
			cap, purchase, attrs("billing_mode", t.BillingMode), t.CreatedAt, b.estate.DynamoDBMonthlyCost(t))
	}
}

func (b *discoveryBuilder) discoverS3() {
	for _, s := range b.estate.S3Buckets {
		var total float64
		for _, g := range s.StorageGiB {
			total += g
		}
		cap := cloud.Capacity{StorageGiB: total, ObjectCount: s.ObjectCount}
		a := attrs("has_lifecycle_policy", boolStr(s.HasLifecyclePolicy),
			"versioning_enabled", boolStr(s.VersioningEnabled),
			"incomplete_multipart_count", fstr(float64(s.IncompleteMultipartCount)),
			"incomplete_multipart_gib", fstr(s.IncompleteMultipartGiB),
			"non_current_version_gib", fstr(s.NonCurrentVersionGiB))
		b.add(cloud.KindS3Bucket, s.ID, s.Name, s.Region, "", s.State, s.Tags, "", "",
			cap, cloud.PurchaseUnknown, a, s.CreatedAt, b.estate.S3MonthlyCost(s))
	}
}

func (b *discoveryBuilder) discoverLambda() {
	for _, f := range b.estate.LambdaFunctions {
		if !inRegion(b.in, f.Region) {
			continue
		}
		cap := cloud.Capacity{MemoryMB: f.MemoryMB, Concurrency: f.ProvisionedConcurrency}
		a := attrs("architecture", f.Architecture, "avg_duration_ms", fstr(f.AvgDurationMS),
			"invocations_per_month", fstr(float64(f.InvocationsPerMonth)), "profile", string(f.Profile))
		b.add(cloud.KindLambdaFunction, f.ID, f.Name, f.Region, "", f.State, f.Tags, "", f.Architecture,
			cap, cloud.PurchaseServerless, a, f.CreatedAt, b.estate.LambdaMonthlyCost(f))
	}
}

func (b *discoveryBuilder) discoverECS() {
	for _, c := range b.estate.ECSClusters {
		if !inRegion(b.in, c.Region) {
			continue
		}
		b.add(cloud.KindECSCluster, c.ID, c.Name, c.Region, "", c.State, c.Tags, "", "",
			cloud.Capacity{}, cloud.PurchaseUnknown, nil, c.CreatedAt, core.ZeroUSD())
	}
	for _, s := range b.estate.ECSServices {
		if !inRegion(b.in, s.Region) {
			continue
		}
		cap := cloud.Capacity{DesiredCount: s.DesiredCount, VCPU: float64(s.CPUUnits) / 1024, MemoryGiB: float64(s.MemoryMB) / 1024}
		b.add(cloud.KindECSService, s.ID, s.Name, s.Region, "", s.State, s.Tags, "", "",
			cap, cloud.PurchaseServerless, attrs("launch_type", s.LaunchType, "cluster_id", s.ClusterID),
			s.CreatedAt, b.estate.ECSServiceMonthlyCost(s))
	}
}

func (b *discoveryBuilder) discoverEKS() {
	for _, c := range b.estate.EKSClusters {
		if !inRegion(b.in, c.Region) {
			continue
		}
		b.add(cloud.KindEKSCluster, c.ID, c.Name, c.Region, "", c.State, c.Tags, "", "",
			cloud.Capacity{}, cloud.PurchaseUnknown, nil, c.CreatedAt, b.estate.EKSClusterMonthlyCost(c))
	}
	for _, ng := range b.estate.EKSNodeGroups {
		if !inRegion(b.in, ng.Region) {
			continue
		}
		cap := cloud.Capacity{InstanceCount: ng.DesiredSize, DesiredCount: ng.DesiredSize}
		a := attrs("cluster_id", ng.ClusterID, "packed_fraction", fstr(ng.PackedFraction),
			"requested_over_actual_ratio", fstr(ng.RequestedOverActualRatio))
		b.add(cloud.KindEKSNodeGroup, ng.ID, ng.Name, ng.Region, "", ng.State, ng.Tags, ng.InstanceType, "",
			cap, cloud.PurchaseOnDemand, a, ng.CreatedAt, b.estate.NodeGroupMonthlyCost(ng))
	}
}

func (b *discoveryBuilder) discoverLoadBalancing() {
	for _, lb := range b.estate.LoadBalancers {
		if !inRegion(b.in, lb.Region) {
			continue
		}
		kind := cloud.KindALB
		if lb.Kind == "network" {
			kind = cloud.KindNLB
		}
		b.add(kind, lb.ID, lb.Name, lb.Region, "", lb.State, lb.Tags, "", "",
			cloud.Capacity{}, cloud.PurchaseUnknown, attrs("lcu_hour_avg", fstr(lb.LCUHourAvg)),
			lb.CreatedAt, b.estate.LoadBalancerMonthlyCost(lb))
	}
	// Target groups carry no region of their own in this model (matching
	// the real ELBv2 API, where a target group's region is implicit in the
	// endpoint you called, not a field on the object); they are gated on
	// their parent load balancer's region instead.
	for _, tg := range b.estate.TargetGroups {
		parent, ok := b.estate.LoadBalancers[tg.LoadBalancerID]
		region := core.Region("")
		if ok {
			region = parent.Region
			if !inRegion(b.in, region) {
				continue
			}
		}
		b.add(cloud.KindTargetGroup, tg.ID, tg.Name, region, "", cloud.StateInUse, tg.Tags, "", "",
			cloud.Capacity{InstanceCount: len(tg.TargetInstanceIDs)}, cloud.PurchaseUnknown,
			attrs("target_type", tg.TargetType, "load_balancer_id", tg.LoadBalancerID), tg.CreatedAt, core.ZeroUSD())
	}
}

func (b *discoveryBuilder) discoverCloudFront() {
	for _, d := range b.estate.CloudFront {
		b.add(cloud.KindCloudFront, d.ID, d.Name, "", "", d.State, d.Tags, "", "",
			cloud.Capacity{}, cloud.PurchaseUnknown, attrs("origin_id", d.OriginID), d.CreatedAt,
			b.estate.CloudFrontMonthlyCost(d))
	}
}

func (b *discoveryBuilder) discoverAPIGateway() {
	for _, a := range b.estate.APIGateways {
		if !inRegion(b.in, a.Region) {
			continue
		}
		b.add(cloud.KindAPIGateway, a.ID, a.Name, a.Region, "", a.State, a.Tags, "", "",
			cloud.Capacity{}, cloud.PurchaseUnknown, attrs("kind", a.Kind, "target_lambda_id", a.TargetLambdaID),
			a.CreatedAt, b.estate.APIGatewayMonthlyCost(a))
	}
}

func (b *discoveryBuilder) discoverNAT() {
	for _, n := range b.estate.NATGateways {
		if !inRegion(b.in, n.Region) {
			continue
		}
		vpcID := ""
		if sn, ok := b.estate.Subnets[n.SubnetID]; ok {
			vpcID = sn.VPCID
		}
		// s3_dynamodb_traffic_fraction is what a flow-log-aware discovery
		// adapter would attach, and the simulator is entitled to attach it
		// because it is the same fraction the estate was built around:
		// natS3TrafficShare is what waste.go charges the NAT gateway for and
		// what the create_vpc_endpoint executor shifts off it. Omitting it
		// left the whole NAT story invisible to the rule engine — the estate
		// designed ~$12K/month of avoidable NAT processing, the executor
		// modelled the fix, and no rule could ever fire on it because the one
		// signal the rule requires was never published. The rule deliberately
		// refuses to guess a fraction (see rule_nat_vpc_endpoint.go), so the
		// signal has to come from here or not at all.
		b.add(cloud.KindNATGateway, n.ID, n.Name, n.Region, n.AZ, n.State, n.Tags, "", "",
			cloud.Capacity{}, cloud.PurchaseUnknown, attrs(
				"subnet_id", n.SubnetID,
				"vpc_id", vpcID,
				"gb_processed_month", fstr(n.GBProcessedPerMonth),
				"s3_dynamodb_traffic_fraction", fstr(natS3TrafficShare),
			),
			n.CreatedAt, b.estate.NATGatewayMonthlyCost(n))
	}
}

func (b *discoveryBuilder) discoverElastiCache() {
	for _, c := range b.estate.ElastiCacheClusters {
		if !inRegion(b.in, c.Region) {
			continue
		}
		cap := cloud.Capacity{InstanceCount: c.NumNodes}
		b.add(cloud.KindElastiCache, c.ID, c.Name, c.Region, "", c.State, c.Tags, c.NodeType, c.Engine,
			cap, cloud.PurchaseOnDemand, nil, c.CreatedAt, b.estate.ElastiCacheMonthlyCost(c))
	}
}

func (b *discoveryBuilder) discoverMessaging() {
	for _, q := range b.estate.SQSQueues {
		if !inRegion(b.in, q.Region) {
			continue
		}
		b.add(cloud.KindSQSQueue, q.ID, q.Name, q.Region, "", q.State, q.Tags, "", "",
			cloud.Capacity{}, cloud.PurchaseUnknown, nil, q.CreatedAt, b.estate.SQSMonthlyCost(q))
	}
	for _, t := range b.estate.SNSTopics {
		if !inRegion(b.in, t.Region) {
			continue
		}
		b.add(cloud.KindSNSTopic, t.ID, t.Name, t.Region, "", t.State, t.Tags, "", "",
			cloud.Capacity{}, cloud.PurchaseUnknown, nil, t.CreatedAt, b.estate.SNSMonthlyCost(t))
	}
}

func (b *discoveryBuilder) discoverLogsAndSecurity() {
	for _, g := range b.estate.LogGroups {
		if !inRegion(b.in, g.Region) {
			continue
		}
		b.add(cloud.KindLogGroup, g.ID, g.Name, g.Region, "", g.State, g.Tags, "", "",
			cloud.Capacity{RetentionDays: g.RetentionDays}, cloud.PurchaseUnknown, nil, g.CreatedAt,
			b.estate.LogGroupMonthlyCost(g))
	}
	for _, k := range b.estate.KMSKeys {
		if !inRegion(b.in, k.Region) {
			continue
		}
		b.add(cloud.KindKMSKey, k.ID, k.Name, k.Region, "", k.State, k.Tags, "", "",
			cloud.Capacity{}, cloud.PurchaseUnknown, nil, k.CreatedAt, b.estate.KMSKeyMonthlyCost(k))
	}
	for _, s := range b.estate.Secrets {
		if !inRegion(b.in, s.Region) {
			continue
		}
		b.add(cloud.KindSecret, s.ID, s.Name, s.Region, "", s.State, s.Tags, "", "",
			cloud.Capacity{}, cloud.PurchaseUnknown, nil, s.CreatedAt, b.estate.SecretMonthlyCost(s))
	}
}

func boolStr(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
