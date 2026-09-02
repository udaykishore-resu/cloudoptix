package awssim

import (
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// This file computes the monthly cost of every simulated resource against
// the estate's pricing catalog. It exists as one place so that a discovered
// Resource's MonthlyCost, a CostIngestor-generated cost.Record and the
// before/after comparison an Executor makes all agree with each other by
// construction — three independently-hand-rolled cost formulas would drift
// the moment one of them was touched and not the others, which is exactly
// the kind of bug this simulator is supposed to catch in the real engines,
// not reproduce in itself.

func addMoney(vals ...core.Money) core.Money {
	total := core.ZeroUSD()
	for _, v := range vals {
		total = total.MustAdd(v)
	}
	return total
}

func priceOr0(m core.Money, ok bool) core.Money {
	if !ok {
		return core.ZeroUSD()
	}
	return m
}

// InstanceMonthlyCost prices an EC2 instance's compute only; its root/data
// volumes are priced separately via VolumeMonthlyCost, matching how AWS
// itself bills them as distinct line items. A stopped instance costs
// nothing to compute.
func (e *Estate) InstanceMonthlyCost(i *EC2Instance) core.Money {
	if i.State != "running" && i.State != "pending" {
		return core.ZeroUSD()
	}
	hourly, ok := e.Catalog.InstancePrice(i.Region, i.InstanceType, i.Platform)
	if !ok {
		return core.ZeroUSD()
	}
	return hourly.Scale(core.HoursPerMonth)
}

// VolumeMonthlyCost prices an EBS volume: capacity, plus provisioned IOPS
// and throughput above gp3's included baseline (3,000 IOPS / 125 MiBps),
// which is the tier the catalog's gp3 rate documents.
func (e *Estate) VolumeMonthlyCost(v *EBSVolume) core.Money {
	capacity := priceOr0(e.Catalog.StoragePrice(v.Region, v.VolumeType))
	capacity = capacity.Scale(v.SizeGiB)

	var iopsCost, tpCost core.Money = core.ZeroUSD(), core.ZeroUSD()
	switch v.VolumeType {
	case "gp3":
		if v.IOPS > 3000 {
			iopsCost = priceOr0(e.Catalog.IOPSPrice(v.Region, v.VolumeType)).Scale(float64(v.IOPS - 3000))
		}
		if v.ThroughputMiBps > 125 {
			tpCost = priceOr0(e.Catalog.ThroughputPrice(v.Region, v.VolumeType)).Scale(v.ThroughputMiBps - 125)
		}
	case "io1", "io2":
		iopsCost = priceOr0(e.Catalog.IOPSPrice(v.Region, v.VolumeType)).Scale(float64(v.IOPS))
	}
	return addMoney(capacity, iopsCost, tpCost)
}

// SnapshotMonthlyCost prices an EBS snapshot's incremental storage.
func (e *Estate) SnapshotMonthlyCost(s *EBSSnapshot) core.Money {
	return priceOr0(e.Catalog.StoragePrice(s.Region, "snapshot")).Scale(s.SizeGiB)
}

// ElasticIPMonthlyCost prices an EIP: free while attached to a running
// resource, metered hourly while unattached (an idle allocation).
func (e *Estate) ElasticIPMonthlyCost(ip *ElasticIP) core.Money {
	if ip.AttachedTo != "" {
		return core.ZeroUSD()
	}
	return priceOr0(e.Catalog.ServicePrice(ip.Region, "elastic_ip", "idle_hour")).Scale(core.HoursPerMonth)
}

// AMIMonthlyCost prices a custom AMI's backing snapshot storage.
func (e *Estate) AMIMonthlyCost(a *AMI) core.Money {
	return priceOr0(e.Catalog.StoragePrice(a.Region, "snapshot")).Scale(a.SizeGiB)
}

// RDSInstanceMonthlyCost prices an RDS/Aurora instance's compute and
// storage. Read replicas are billed the same as any other running instance;
// AWS does not discount them.
func (e *Estate) RDSInstanceMonthlyCost(r *RDSInstance) core.Money {
	if r.State != "running" && r.State != "available" {
		return core.ZeroUSD()
	}
	hourly := priceOr0(e.Catalog.DatabasePrice(r.Region, r.InstanceClass, r.Engine, r.MultiAZ))
	compute := hourly.Scale(core.HoursPerMonth)

	storageKey := "rds_" + r.StorageType
	storage := priceOr0(e.Catalog.StoragePrice(r.Region, storageKey)).Scale(r.StorageGiB)
	var iops core.Money = core.ZeroUSD()
	if r.StorageType == "io1" {
		// A representative provisioned rate: 3 IOPS per GiB, matching the
		// common RDS io1 sizing rule of thumb.
		iops = priceOr0(e.Catalog.IOPSPrice(r.Region, "rds_io1")).Scale(r.StorageGiB * 3)
	}
	// Backup storage: retained backups typically run at roughly half the
	// allocated storage size once the free-tier allowance (100% of DB size)
	// is exceeded by long retention windows or Multi-AZ snapshotting.
	backup := priceOr0(e.Catalog.StoragePrice(r.Region, "rds_backup")).Scale(r.StorageGiB * 0.5)

	return addMoney(compute, storage, iops, backup)
}

// RDSSnapshotMonthlyCost prices an RDS snapshot's storage, reusing the
// backup-storage rate.
func (e *Estate) RDSSnapshotMonthlyCost(s *RDSSnapshot) core.Money {
	return priceOr0(e.Catalog.StoragePrice(s.Region, "rds_backup")).Scale(s.SizeGiB)
}

// DynamoDBMonthlyCost prices a table under provisioned or on-demand billing.
func (e *Estate) DynamoDBMonthlyCost(t *DynamoDBTable) core.Money {
	var throughput core.Money
	if t.BillingMode == "on_demand" {
		// on_demand_read/write are priced per 1,000 request-units; RCU/WCU
		// here stand in for average request-units/second under on-demand.
		monthlyReadUnits := t.RCU * core.HoursPerMonth * 3600 / 1000
		monthlyWriteUnits := t.WCU * core.HoursPerMonth * 3600 / 1000
		throughput = addMoney(
			priceOr0(e.Catalog.ServicePrice(t.Region, "dynamodb", "on_demand_read")).Scale(monthlyReadUnits),
			priceOr0(e.Catalog.ServicePrice(t.Region, "dynamodb", "on_demand_write")).Scale(monthlyWriteUnits),
		)
	} else {
		throughput = addMoney(
			priceOr0(e.Catalog.ServicePrice(t.Region, "dynamodb", "rcu_hour")).Scale(t.RCU*core.HoursPerMonth),
			priceOr0(e.Catalog.ServicePrice(t.Region, "dynamodb", "wcu_hour")).Scale(t.WCU*core.HoursPerMonth),
		)
	}
	storage := priceOr0(e.Catalog.ServicePrice(t.Region, "dynamodb", "storage_gb_month")).Scale(t.SizeGiB)
	return addMoney(throughput, storage)
}

// S3MonthlyCost prices a bucket's storage across classes plus request and
// monitoring charges.
func (e *Estate) S3MonthlyCost(b *S3Bucket) core.Money {
	total := core.ZeroUSD()
	for class, gib := range b.StorageGiB {
		total = total.MustAdd(priceOr0(e.Catalog.StoragePrice(b.Region, class)).Scale(gib))
		if class == "intelligent_tiering" {
			// Monitoring is billed per object, independent of size; assume
			// a representative 128 KiB average object size for this class.
			objects := gib * 1024 * 1024 / 128
			total = total.MustAdd(priceOr0(e.Catalog.ServicePrice(b.Region, "s3", "monitoring_per_million_objects")).Scale(objects / 1_000_000))
		}
	}
	// Incomplete multipart uploads and old non-current versions sit in
	// Standard until lifecycle rules (or an executor) clear them.
	total = total.MustAdd(priceOr0(e.Catalog.StoragePrice(b.Region, "standard")).Scale(b.IncompleteMultipartGiB))
	total = total.MustAdd(priceOr0(e.Catalog.StoragePrice(b.Region, "standard")).Scale(b.NonCurrentVersionGiB))

	requests := addMoney(
		priceOr0(e.Catalog.ServicePrice(b.Region, "s3", "put_request_per_1k")).Scale(float64(b.PutRequestsPerMonth)/1000),
		priceOr0(e.Catalog.ServicePrice(b.Region, "s3", "get_request_per_1k")).Scale(float64(b.GetRequestsPerMonth)/1000),
	)
	return addMoney(total, requests)
}

// LambdaMonthlyCost prices invocation cost (duration x memory) plus any
// provisioned concurrency held around the clock.
func (e *Estate) LambdaMonthlyCost(f *LambdaFunction) core.Money {
	gbSecDim := "gb_second"
	if f.Architecture == "arm64" {
		gbSecDim = "arm_gb_second"
	}
	gb := float64(f.MemoryMB) / 1024
	gbSeconds := gb * (f.AvgDurationMS / 1000) * float64(f.InvocationsPerMonth)
	compute := priceOr0(e.Catalog.ServicePrice(f.Region, "lambda", gbSecDim)).Scale(gbSeconds)
	requests := priceOr0(e.Catalog.ServicePrice(f.Region, "lambda", "request")).Scale(float64(f.InvocationsPerMonth) / 1000)

	var provisioned core.Money = core.ZeroUSD()
	if f.ProvisionedConcurrency > 0 {
		pcGBSeconds := gb * float64(f.ProvisionedConcurrency) * core.HoursPerMonth * 3600
		provisioned = priceOr0(e.Catalog.ServicePrice(f.Region, "lambda", "provisioned_concurrency_gb_second")).Scale(pcGBSeconds)
	}
	return addMoney(compute, requests, provisioned)
}

// ECSServiceMonthlyCost prices a Fargate service's reserved vCPU/memory. An
// EC2-launch-type service's cost is carried by its underlying instances
// instead (there is no separate Fargate charge to add).
func (e *Estate) ECSServiceMonthlyCost(s *ECSService) core.Money {
	if s.LaunchType != "fargate" {
		return core.ZeroUSD()
	}
	vcpu := float64(s.CPUUnits) / 1024
	gb := float64(s.MemoryMB) / 1024
	vcpuCost := priceOr0(e.Catalog.ServicePrice(s.Region, "fargate", "vcpu_hour")).Scale(vcpu * core.HoursPerMonth * float64(s.DesiredCount))
	memCost := priceOr0(e.Catalog.ServicePrice(s.Region, "fargate", "gb_hour")).Scale(gb * core.HoursPerMonth * float64(s.DesiredCount))
	return addMoney(vcpuCost, memCost)
}

// EKSClusterMonthlyCost prices the control-plane hourly charge.
func (e *Estate) EKSClusterMonthlyCost(c *EKSCluster) core.Money {
	return priceOr0(e.Catalog.ServicePrice(c.Region, "eks", "cluster_hour")).Scale(core.HoursPerMonth)
}

// NodeGroupMonthlyCost prices a node group's underlying EC2 instances.
func (e *Estate) NodeGroupMonthlyCost(ng *EKSNodeGroup) core.Money {
	hourly, ok := e.Catalog.InstancePrice(ng.Region, ng.InstanceType, "linux")
	if !ok {
		return core.ZeroUSD()
	}
	return hourly.Scale(core.HoursPerMonth * float64(ng.DesiredSize))
}

// LoadBalancerMonthlyCost prices an ALB or NLB's hourly charge plus average
// LCU consumption.
func (e *Estate) LoadBalancerMonthlyCost(lb *LoadBalancer) core.Money {
	kind := "alb"
	if lb.Kind == "network" {
		kind = "nlb"
	}
	hours := priceOr0(e.Catalog.ServicePrice(lb.Region, kind, "hours")).Scale(core.HoursPerMonth)
	lcu := priceOr0(e.Catalog.ServicePrice(lb.Region, kind, "lcu_hour")).Scale(lb.LCUHourAvg * core.HoursPerMonth)
	return addMoney(hours, lcu)
}

// CloudFrontMonthlyCost prices data transfer out and requests.
func (e *Estate) CloudFrontMonthlyCost(d *CloudFrontDistribution) core.Money {
	gb := priceOr0(e.Catalog.ServicePrice(d.Region, "cloudfront", "gb_out")).Scale(d.GBOutPerMonth)
	req := priceOr0(e.Catalog.ServicePrice(d.Region, "cloudfront", "requests")).Scale(float64(d.RequestsPerMonth) / 1000)
	return addMoney(gb, req)
}

// APIGatewayMonthlyCost prices per-request charges by API type.
func (e *Estate) APIGatewayMonthlyCost(a *APIGateway) core.Money {
	dim := "http_request"
	if a.Kind == "rest" {
		dim = "rest_request"
	}
	return priceOr0(e.Catalog.ServicePrice(a.Region, "api_gateway", dim)).Scale(float64(a.RequestsPerMonth) / 1000)
}

// NATGatewayMonthlyCost prices the hourly charge plus data processing.
func (e *Estate) NATGatewayMonthlyCost(n *NATGateway) core.Money {
	hours := priceOr0(e.Catalog.ServicePrice(n.Region, "nat_gateway", "hours")).Scale(core.HoursPerMonth)
	gb := priceOr0(e.Catalog.ServicePrice(n.Region, "nat_gateway", "gb_processed")).Scale(n.GBProcessedPerMonth)
	return addMoney(hours, gb)
}

// VPCEndpointMonthlyCost prices an interface endpoint's hourly charge. (Gateway
// endpoints for S3/DynamoDB are free; this simulator only creates interface
// endpoints, which always carry the hourly charge.)
func (e *Estate) VPCEndpointMonthlyCost(v *VPCEndpoint) core.Money {
	return priceOr0(e.Catalog.ServicePrice(v.Region, "vpc_endpoint", "hour")).Scale(core.HoursPerMonth)
}

// ElastiCacheMonthlyCost prices every node in the cluster.
func (e *Estate) ElastiCacheMonthlyCost(c *ElastiCacheCluster) core.Money {
	hourly := priceOr0(e.Catalog.CachePrice(c.Region, c.NodeType, c.Engine))
	return hourly.Scale(core.HoursPerMonth * float64(c.NumNodes))
}

// SQSMonthlyCost prices request volume.
func (e *Estate) SQSMonthlyCost(q *SQSQueue) core.Money {
	return priceOr0(e.Catalog.ServicePrice(q.Region, "sqs", "requests")).Scale(float64(q.RequestsPerMonth) / 1000)
}

// SNSMonthlyCost prices request volume.
func (e *Estate) SNSMonthlyCost(t *SNSTopic) core.Money {
	return priceOr0(e.Catalog.ServicePrice(t.Region, "sns", "requests")).Scale(float64(t.RequestsPerMonth) / 1000)
}

// LogGroupMonthlyCost prices ingestion and stored volume.
func (e *Estate) LogGroupMonthlyCost(g *LogGroup) core.Money {
	ingest := priceOr0(e.Catalog.ServicePrice(g.Region, "cloudwatch", "log_ingest_gb")).Scale(g.IngestGBPerMonth)
	stored := priceOr0(e.Catalog.ServicePrice(g.Region, "cloudwatch", "log_storage_gb")).Scale(g.StoredGiB)
	return addMoney(ingest, stored)
}

// KMSKeyMonthlyCost prices the flat per-key charge.
func (e *Estate) KMSKeyMonthlyCost(k *KMSKey) core.Money {
	return priceOr0(e.Catalog.ServicePrice(k.Region, "kms", "key_month"))
}

// SecretMonthlyCost prices the flat per-secret charge.
func (e *Estate) SecretMonthlyCost(s *Secret) core.Money {
	return priceOr0(e.Catalog.ServicePrice(s.Region, "secretsmanager", "secret_month"))
}

// TotalMonthlyCost sums every priced resource in the estate. It is the
// figure the demo estate's target range is measured against, and the figure
// an executed mutation must actually move.
func (e *Estate) TotalMonthlyCost() core.Money {
	e.mu.RLock()
	defer e.mu.RUnlock()
	total := core.ZeroUSD()
	for _, r := range e.EC2Instances {
		total = total.MustAdd(e.InstanceMonthlyCost(r))
	}
	for _, r := range e.EBSVolumes {
		total = total.MustAdd(e.VolumeMonthlyCost(r))
	}
	for _, r := range e.EBSSnapshots {
		total = total.MustAdd(e.SnapshotMonthlyCost(r))
	}
	for _, r := range e.ElasticIPs {
		total = total.MustAdd(e.ElasticIPMonthlyCost(r))
	}
	for _, r := range e.AMIs {
		total = total.MustAdd(e.AMIMonthlyCost(r))
	}
	for _, r := range e.RDSInstances {
		total = total.MustAdd(e.RDSInstanceMonthlyCost(r))
	}
	for _, r := range e.RDSSnapshots {
		total = total.MustAdd(e.RDSSnapshotMonthlyCost(r))
	}
	for _, r := range e.DynamoDBTables {
		total = total.MustAdd(e.DynamoDBMonthlyCost(r))
	}
	for _, r := range e.S3Buckets {
		total = total.MustAdd(e.S3MonthlyCost(r))
	}
	for _, r := range e.LambdaFunctions {
		total = total.MustAdd(e.LambdaMonthlyCost(r))
	}
	for _, r := range e.ECSServices {
		total = total.MustAdd(e.ECSServiceMonthlyCost(r))
	}
	for _, r := range e.EKSClusters {
		total = total.MustAdd(e.EKSClusterMonthlyCost(r))
	}
	for _, r := range e.EKSNodeGroups {
		total = total.MustAdd(e.NodeGroupMonthlyCost(r))
	}
	for _, r := range e.LoadBalancers {
		total = total.MustAdd(e.LoadBalancerMonthlyCost(r))
	}
	for _, r := range e.CloudFront {
		total = total.MustAdd(e.CloudFrontMonthlyCost(r))
	}
	for _, r := range e.APIGateways {
		total = total.MustAdd(e.APIGatewayMonthlyCost(r))
	}
	for _, r := range e.NATGateways {
		total = total.MustAdd(e.NATGatewayMonthlyCost(r))
	}
	for _, r := range e.VPCEndpoints {
		total = total.MustAdd(e.VPCEndpointMonthlyCost(r))
	}
	for _, r := range e.ElastiCacheClusters {
		total = total.MustAdd(e.ElastiCacheMonthlyCost(r))
	}
	for _, r := range e.SQSQueues {
		total = total.MustAdd(e.SQSMonthlyCost(r))
	}
	for _, r := range e.SNSTopics {
		total = total.MustAdd(e.SNSMonthlyCost(r))
	}
	for _, r := range e.LogGroups {
		total = total.MustAdd(e.LogGroupMonthlyCost(r))
	}
	for _, r := range e.KMSKeys {
		total = total.MustAdd(e.KMSKeyMonthlyCost(r))
	}
	for _, r := range e.Secrets {
		total = total.MustAdd(e.SecretMonthlyCost(r))
	}
	return total
}
