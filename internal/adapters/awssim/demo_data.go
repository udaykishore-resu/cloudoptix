package awssim

import (
	"fmt"
	"math/rand"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

// This file continues BuildDemoEstate's construction (started in demo.go)
// for every resource category after the EC2/EBS/snapshot/EIP/AMI layer:
// RDS, DynamoDB, S3, Lambda, ECS/Fargate, EKS, load balancing, CDN/API
// Gateway, ElastiCache, messaging, logging and the security-adjacent
// resources (KMS, Secrets Manager).

func buildRDS(e *Estate, region core.Region, tagged func() bool) {
	azs := []string{"us-east-1a", "us-east-1b"}

	// The oversized multi-AZ primary running at low utilisation, plus its
	// unused read replica — the RDS waste story.
	primaryID := nextID("db")
	e.RDSInstances[primaryID] = &RDSInstance{
		Base: Base{ID: primaryID, Name: "shopfleet-orders-primary", Region: region, AZ: azs[0], State: cloud.StateRunning,
			Tags: mkTags(tagged(), "shopfleet-orders-primary", "order-fulfillment", "production", "fulfillment"), CreatedAt: daysAgo(700)},
		InstanceClass: "db.r5.8xlarge", Engine: "postgres", MultiAZ: true,
		StorageGiB: 4000, StorageType: "gp3", Profile: ProfileIdle,
	}
	replicaID := nextID("db")
	e.RDSInstances[replicaID] = &RDSInstance{
		Base: Base{ID: replicaID, Name: "shopfleet-orders-replica-unused", Region: region, AZ: azs[1], State: cloud.StateRunning,
			Tags: mkTags(tagged(), "shopfleet-orders-replica-unused", "order-fulfillment", "production", "fulfillment"), CreatedAt: daysAgo(500)},
		InstanceClass: "db.r5.8xlarge", Engine: "postgres", MultiAZ: false,
		StorageGiB: 4000, StorageType: "gp3", IsReadReplica: true, PrimaryID: primaryID, Profile: ProfileIdle,
	}

	others := []struct {
		name, class, engine string
		multiAZ             bool
		storageGiB          float64
		app, team           string
		profile             UtilizationProfile
	}{
		{"shopfleet-catalog-db", "db.r5.xlarge", "postgres", false, 200, "catalog-service", "catalog", ProfileSteady},
		{"shopfleet-catalog-db-2", "db.r5.xlarge", "postgres", false, 200, "catalog-service", "catalog", ProfileSteady},
		{"shopfleet-search-meta-db", "db.m5.large", "mysql", false, 100, "search", "search", ProfileSteady},
		{"shopfleet-notifications-db", "db.m5.large", "mysql", false, 100, "notifications", "platform", ProfileSteady},
		{"shopfleet-inventory-db", "db.r6i.xlarge", "postgres", false, 200, "inventory", "fulfillment", ProfileSteady},
	}
	for i, r := range others {
		id := nextID("db")
		e.RDSInstances[id] = &RDSInstance{
			Base: Base{ID: id, Name: r.name, Region: region, AZ: azs[i%2], State: cloud.StateRunning,
				Tags: mkTags(tagged(), r.name, r.app, "production", r.team), CreatedAt: daysAgo(300 + i*20)},
			InstanceClass: r.class, Engine: r.engine, MultiAZ: r.multiAZ, StorageGiB: r.storageGiB,
			StorageType: "gp3", Profile: r.profile,
		}
	}

	// An Aurora PostgreSQL cluster for payments, writer + reader.
	clusterID := nextID("cluster")
	writerID, readerID := nextID("db"), nextID("db")
	e.RDSClusters[clusterID] = &RDSCluster{
		Base: Base{ID: clusterID, Name: "shopfleet-payments-aurora", Region: region, State: cloud.StateAvailable,
			Tags: mkTags(tagged(), "shopfleet-payments-aurora", "checkout-api", "production", "payments"), CreatedAt: daysAgo(600)},
		Engine: "aurora-postgresql", InstanceIDs: []string{writerID, readerID},
	}
	e.RDSInstances[writerID] = &RDSInstance{
		Base: Base{ID: writerID, Name: "shopfleet-payments-aurora-writer", Region: region, AZ: azs[0], State: cloud.StateRunning,
			Tags: mkTags(tagged(), "shopfleet-payments-aurora-writer", "checkout-api", "production", "payments"), CreatedAt: daysAgo(600)},
		InstanceClass: "db.r6g.xlarge", Engine: "aurora-postgresql", StorageGiB: 100, StorageType: "gp3",
		ClusterID: clusterID, Profile: ProfileSteady,
	}
	e.RDSInstances[readerID] = &RDSInstance{
		Base: Base{ID: readerID, Name: "shopfleet-payments-aurora-reader", Region: region, AZ: azs[1], State: cloud.StateRunning,
			Tags: mkTags(tagged(), "shopfleet-payments-aurora-reader", "checkout-api", "production", "payments"), CreatedAt: daysAgo(600)},
		InstanceClass: "db.r6g.xlarge", Engine: "aurora-postgresql", StorageGiB: 100, StorageType: "gp3",
		ClusterID: clusterID, IsReadReplica: true, PrimaryID: writerID, Profile: ProfileSteady,
	}

	// A handful of RDS snapshots.
	for i := 0; i < 6; i++ {
		id := nextID("snap")
		name := fmt.Sprintf("shopfleet-orders-primary-snap-%02d", i+1)
		e.RDSSnapshots[id] = &RDSSnapshot{
			Base: Base{ID: id, Name: name, Region: region, State: cloud.StateAvailable,
				Tags: mkTags(tagged(), name, "order-fulfillment", "production", "fulfillment"), CreatedAt: daysAgo(30 + i*30)},
			SourceID: primaryID, SizeGiB: 4000,
		}
	}
}

func buildDynamoDB(e *Estate, region core.Region, tagged func() bool) {
	tables := []struct {
		name, mode, app, team string
		rcu, wcu, sizeGiB     float64
	}{
		{"shopfleet-sessions", "on_demand", "web-storefront", "storefront", 5, 2, 50},
		{"shopfleet-cart-events-provisioned", "provisioned", "checkout-api", "payments", 200, 200, 20},
		{"shopfleet-feature-flags", "provisioned", "admin-portal", "platform", 10, 10, 5},
	}
	for _, t := range tables {
		id := nextID("table")
		e.DynamoDBTables[id] = &DynamoDBTable{
			Base: Base{ID: id, Name: t.name, Region: region, State: cloud.StateInUse,
				Tags: mkTags(tagged(), t.name, t.app, "production", t.team), CreatedAt: daysAgo(400)},
			BillingMode: t.mode, RCU: t.rcu, WCU: t.wcu, SizeGiB: t.sizeGiB, Profile: ProfileSteady,
		}
	}
}

func buildS3(e *Estate, region core.Region, tagged func() bool) {
	assets := nextID("bucket")
	e.S3Buckets[assets] = &S3Bucket{
		Base: Base{ID: assets, Name: "shopfleet-prod-assets", Region: region, State: cloud.StateInUse,
			Tags: mkTags(tagged(), "shopfleet-prod-assets", "web-storefront", "production", "storefront"), CreatedAt: daysAgo(900)},
		StorageGiB:               map[string]float64{"standard": 45000},
		ObjectCount:              38_000_000,
		HasLifecyclePolicy:       false, // the waste story: nothing ever moves out of Standard
		IncompleteMultipartCount: 1400,
		IncompleteMultipartGiB:   1500,
		NonCurrentVersionGiB:     2500,
		VersioningEnabled:        true,
		PutRequestsPerMonth:      6_000_000,
		GetRequestsPerMonth:      60_000_000,
	}

	backups := nextID("bucket")
	e.S3Buckets[backups] = &S3Bucket{
		Base: Base{ID: backups, Name: "shopfleet-prod-backups", Region: region, State: cloud.StateInUse,
			Tags: mkTags(tagged(), "shopfleet-prod-backups", "analytics-batch", "production", "data"), CreatedAt: daysAgo(800)},
		StorageGiB:          map[string]float64{"deep_archive": 5000, "glacier_flexible": 2000, "standard_ia": 500},
		ObjectCount:         200_000,
		HasLifecyclePolicy:  true,
		PutRequestsPerMonth: 40_000, GetRequestsPerMonth: 5_000,
	}

	logs := nextID("bucket")
	e.S3Buckets[logs] = &S3Bucket{
		Base: Base{ID: logs, Name: "shopfleet-prod-access-logs", Region: region, State: cloud.StateInUse,
			Tags: mkTags(tagged(), "shopfleet-prod-access-logs", "", "", ""), CreatedAt: daysAgo(750)},
		StorageGiB:          map[string]float64{"intelligent_tiering": 9000},
		ObjectCount:         5_000_000,
		HasLifecyclePolicy:  true,
		PutRequestsPerMonth: 500_000, GetRequestsPerMonth: 100_000,
	}

	static := nextID("bucket")
	e.S3Buckets[static] = &S3Bucket{
		Base: Base{ID: static, Name: "shopfleet-static-web", Region: region, State: cloud.StateInUse,
			Tags: mkTags(tagged(), "shopfleet-static-web", "web-storefront", "production", "storefront"), CreatedAt: daysAgo(600)},
		StorageGiB:          map[string]float64{"standard": 200},
		ObjectCount:         12_000,
		HasLifecyclePolicy:  true,
		PutRequestsPerMonth: 2_000, GetRequestsPerMonth: 400_000,
	}
}

func buildLambda(e *Estate, region core.Region, tagged func() bool) {
	fns := []struct {
		name, arch, app, team  string
		memMB                  int
		durMS                  float64
		invocations            int64
		provisionedConcurrency int
		profile                UtilizationProfile
	}{
		{"image-resize", "x86_64", "web-storefront", "storefront", 3008, 180, 5_000_000, 0, ProfileSpiky},
		{"notification-dispatcher", "x86_64", "notifications", "platform", 2048, 90, 2_000_000, 0, ProfileCyclical},
		{"checkout-webhook", "x86_64", "checkout-api", "payments", 1024, 50, 500_000, 20, ProfileIdle},
		{"search-autocomplete", "arm64", "search", "search", 512, 40, 10_000_000, 0, ProfileSteady},
		{"order-processor", "x86_64", "order-fulfillment", "fulfillment", 1024, 120, 3_000_000, 0, ProfileSteady},
		{"cart-abandonment-job", "x86_64", "checkout-api", "payments", 256, 300, 200_000, 0, ProfileCyclical},
	}
	for _, fn := range fns {
		id := nextID("fn")
		e.LambdaFunctions[id] = &LambdaFunction{
			Base: Base{ID: id, Name: fn.name, Region: region, State: cloud.StateInUse,
				Tags: mkTags(tagged(), fn.name, fn.app, "production", fn.team), CreatedAt: daysAgo(250)},
			MemoryMB: fn.memMB, AvgDurationMS: fn.durMS, InvocationsPerMonth: fn.invocations,
			Architecture: fn.arch, ProvisionedConcurrency: fn.provisionedConcurrency, Profile: fn.profile,
		}
	}
}

func buildECSFargate(e *Estate, f fixture, region core.Region, rng *rand.Rand, tagged func() bool) {
	clusterID := nextID("ecscluster")
	e.ECSClusters[clusterID] = &ECSCluster{Base: Base{ID: clusterID, Name: "shopfleet-fargate-cluster",
		Region: region, State: cloud.StateInUse, Tags: mkTags(true, "shopfleet-fargate-cluster", "platform", "production", "platform"), CreatedAt: daysAgo(500)}}

	apps := []string{"web-storefront", "checkout-api", "catalog-service", "order-fulfillment", "search", "inventory", "notifications", "recommendation-engine"}
	teams := map[string]string{
		"web-storefront": "storefront", "checkout-api": "payments", "catalog-service": "catalog",
		"order-fulfillment": "fulfillment", "search": "search", "inventory": "fulfillment",
		"notifications": "platform", "recommendation-engine": "data",
	}
	p := f.Params
	for i := 0; i < p.FargateServiceCount; i++ {
		app := apps[i%len(apps)]
		id := nextID("ecssvc")
		name := fmt.Sprintf("%s-svc-%d", app, i/len(apps)+1)
		desired := p.FargateAvgDesiredCount + randRange(rng, -6, 6)
		if desired < 2 {
			desired = 2
		}
		cpu := p.FargateAvgCPUUnits
		mem := p.FargateAvgMemoryMB
		e.ECSServices[id] = &ECSService{
			Base: Base{ID: id, Name: name, Region: region, State: cloud.StateInUse,
				Tags: mkTags(tagged(), name, app, "production", teams[app]), CreatedAt: daysAgo(200)},
			ClusterID: clusterID, LaunchType: "fargate", DesiredCount: desired,
			CPUUnits: cpu, MemoryMB: mem, Profile: ProfileSteady,
		}
	}
}

func buildEKS(e *Estate, f fixture, region core.Region, tagged func() bool) {
	clusterID := nextID("ekscluster")
	e.EKSClusters[clusterID] = &EKSCluster{Base: Base{ID: clusterID, Name: "shopfleet-eks",
		Region: region, State: cloud.StateInUse, Tags: mkTags(true, "shopfleet-eks", "platform", "production", "platform"), CreatedAt: daysAgo(500)}}

	p := f.Params
	ngA := nextID("nodegroup")
	e.EKSNodeGroups[ngA] = &EKSNodeGroup{
		Base: Base{ID: ngA, Name: "shopfleet-eks-general", Region: region, State: cloud.StateInUse,
			Tags: mkTags(true, "shopfleet-eks-general", "platform", "production", "platform"), CreatedAt: daysAgo(480)},
		ClusterID: clusterID, InstanceType: "m5.2xlarge", DesiredSize: p.EKSNodeGroupASize,
		PackedFraction: p.EKSPackedFraction, RequestedOverActualRatio: p.EKSRequestOverActualRatio,
	}
	ngB := nextID("nodegroup")
	e.EKSNodeGroups[ngB] = &EKSNodeGroup{
		Base: Base{ID: ngB, Name: "shopfleet-eks-compute", Region: region, State: cloud.StateInUse,
			Tags: mkTags(true, "shopfleet-eks-compute", "platform", "production", "platform"), CreatedAt: daysAgo(480)},
		ClusterID: clusterID, InstanceType: "c5.4xlarge", DesiredSize: p.EKSNodeGroupBSize,
		PackedFraction: p.EKSPackedFraction + 0.05, RequestedOverActualRatio: p.EKSRequestOverActualRatio,
	}
}

func buildLoadBalancers(e *Estate, region core.Region, tagged func() bool) {
	albs := []struct {
		name, app, team string
		lcu             float64
	}{
		{"shopfleet-web-alb", "web-storefront", "storefront", 9},
		{"shopfleet-checkout-alb", "checkout-api", "payments", 11},
		{"shopfleet-catalog-alb", "catalog-service", "catalog", 6},
		{"shopfleet-search-alb", "search", "search", 7},
		{"shopfleet-admin-alb", "admin-portal", "platform", 3},
	}
	for _, a := range albs {
		id := nextID("elbv2")
		tgID := nextID("tg")
		targets := instancesForApp(e, a.app, 3)
		e.TargetGroups[tgID] = &TargetGroup{
			Base: Base{ID: tgID, Name: a.name + "-tg", Region: region, State: cloud.StateInUse,
				Tags: mkTags(tagged(), a.name+"-tg", a.app, "production", a.team), CreatedAt: daysAgo(500)},
			LoadBalancerID: id, TargetInstanceIDs: targets, TargetType: "instance",
		}
		e.LoadBalancers[id] = &LoadBalancer{
			Base: Base{ID: id, Name: a.name, Region: region, State: cloud.StateInUse,
				Tags: mkTags(tagged(), a.name, a.app, "production", a.team), CreatedAt: daysAgo(500)},
			Kind: "application", LCUHourAvg: a.lcu, TargetGroupIDs: []string{tgID},
		}
	}
	nlbs := []struct {
		name, app, team string
		lcu             float64
	}{
		{"shopfleet-internal-nlb", "inventory", "fulfillment", 5},
		{"shopfleet-kafka-nlb", "order-fulfillment", "fulfillment", 5},
	}
	for _, n := range nlbs {
		id := nextID("elbv2")
		e.LoadBalancers[id] = &LoadBalancer{
			Base: Base{ID: id, Name: n.name, Region: region, State: cloud.StateInUse,
				Tags: mkTags(tagged(), n.name, n.app, "production", n.team), CreatedAt: daysAgo(500)},
			Kind: "network", LCUHourAvg: n.lcu,
		}
	}
}

func buildCloudFront(e *Estate, region core.Region, tagged func() bool) {
	id := nextID("cfdist")
	e.CloudFront[id] = &CloudFrontDistribution{
		Base: Base{ID: id, Name: "shopfleet-cdn", Region: region, State: cloud.StateInUse,
			Tags: mkTags(tagged(), "shopfleet-cdn", "web-storefront", "production", "storefront"), CreatedAt: daysAgo(600)},
		OriginID:      lbIDByName(e, "shopfleet-web-alb"),
		GBOutPerMonth: 460_000, RequestsPerMonth: 620_000_000,
	}
}

func buildAPIGateways(e *Estate, region core.Region, tagged func() bool) {
	rest := nextID("apigw")
	e.APIGateways[rest] = &APIGateway{
		Base: Base{ID: rest, Name: "shopfleet-rest-api", Region: region, State: cloud.StateInUse,
			Tags: mkTags(tagged(), "shopfleet-rest-api", "checkout-api", "production", "payments"), CreatedAt: daysAgo(500)},
		Kind: "rest", RequestsPerMonth: 20_000_000, TargetLambdaID: lambdaIDByName(e, "checkout-webhook"),
	}
	http := nextID("apigw")
	e.APIGateways[http] = &APIGateway{
		Base: Base{ID: http, Name: "shopfleet-http-api", Region: region, State: cloud.StateInUse,
			Tags: mkTags(tagged(), "shopfleet-http-api", "search", "production", "search"), CreatedAt: daysAgo(400)},
		Kind: "http", RequestsPerMonth: 50_000_000, TargetLambdaID: lambdaIDByName(e, "search-autocomplete"),
	}
}

func buildElastiCache(e *Estate, region core.Region, tagged func() bool) {
	redis := nextID("cachecluster")
	e.ElastiCacheClusters[redis] = &ElastiCacheCluster{
		Base: Base{ID: redis, Name: "shopfleet-session-cache", Region: region, State: cloud.StateAvailable,
			Tags: mkTags(tagged(), "shopfleet-session-cache", "web-storefront", "production", "storefront"), CreatedAt: daysAgo(500)},
		NodeType: "cache.r6g.xlarge", Engine: "redis", NumNodes: 10, Profile: ProfileSteady,
	}
	memcached := nextID("cachecluster")
	e.ElastiCacheClusters[memcached] = &ElastiCacheCluster{
		Base: Base{ID: memcached, Name: "shopfleet-catalog-cache", Region: region, State: cloud.StateAvailable,
			Tags: mkTags(tagged(), "shopfleet-catalog-cache", "catalog-service", "production", "catalog"), CreatedAt: daysAgo(450)},
		NodeType: "cache.m6g.large", Engine: "memcached", NumNodes: 6, Profile: ProfileSteady,
	}
}

func buildMessaging(e *Estate, region core.Region, tagged func() bool) {
	queues := []struct {
		name, app, team string
		reqs            int64
	}{
		{"shopfleet-order-events", "order-fulfillment", "fulfillment", 15_000_000},
		{"shopfleet-checkout-dlq", "checkout-api", "payments", 200_000},
		{"shopfleet-notification-fanout", "notifications", "platform", 25_000_000},
		{"shopfleet-inventory-sync", "inventory", "fulfillment", 8_000_000},
		{"shopfleet-analytics-ingest", "analytics-batch", "data", 2_000_000},
	}
	for _, q := range queues {
		id := nextID("sqs")
		e.SQSQueues[id] = &SQSQueue{
			Base: Base{ID: id, Name: q.name, Region: region, State: cloud.StateInUse,
				Tags: mkTags(tagged(), q.name, q.app, "production", q.team), CreatedAt: daysAgo(400)},
			RequestsPerMonth: q.reqs,
		}
	}
	topics := []struct {
		name, app, team string
		reqs            int64
	}{
		{"shopfleet-order-placed", "order-fulfillment", "fulfillment", 5_000_000},
		{"shopfleet-payment-events", "checkout-api", "payments", 4_000_000},
		{"shopfleet-alerts", "admin-portal", "platform", 1_000_000},
	}
	for _, t := range topics {
		id := nextID("sns")
		e.SNSTopics[id] = &SNSTopic{
			Base: Base{ID: id, Name: t.name, Region: region, State: cloud.StateInUse,
				Tags: mkTags(tagged(), t.name, t.app, "production", t.team), CreatedAt: daysAgo(400)},
			RequestsPerMonth: t.reqs,
		}
	}
}

func buildLogGroups(e *Estate, f fixture, region core.Region, rng *rand.Rand, tagged func() bool) {
	p := f.Params
	apps := []string{"web-storefront", "checkout-api", "catalog-service", "order-fulfillment", "search",
		"inventory", "notifications", "recommendation-engine", "analytics-batch", "admin-portal"}
	for i := 0; i < p.LogGroupCount; i++ {
		app := apps[i%len(apps)]
		id := nextID("loggroup")
		name := fmt.Sprintf("/shopfleet/%s/%d", app, i)
		retention := 0 // never expire
		if rng.Float64() >= p.LogGroupNeverExpireFraction {
			retention = []int{14, 30, 90}[rng.Intn(3)]
		}
		e.LogGroups[id] = &LogGroup{
			Base: Base{ID: id, Name: name, Region: region, State: cloud.StateInUse,
				Tags: mkTags(tagged(), name, app, "production", ""), CreatedAt: daysAgo(randRange(rng, 200, 700))},
			RetentionDays: retention, IngestGBPerMonth: float64(randRange(rng, 20, 90)), StoredGiB: float64(randRange(rng, 100, 500)),
		}
	}
}

func buildKMSAndSecrets(e *Estate, region core.Region, tagged func() bool) {
	keys := []string{"shopfleet-rds-key", "shopfleet-s3-key", "shopfleet-secrets-key", "shopfleet-ebs-key"}
	for _, name := range keys {
		id := nextID("key")
		e.KMSKeys[id] = &KMSKey{Base: Base{ID: id, Name: name, Region: region, State: cloud.StateInUse,
			Tags: mkTags(tagged(), name, "platform", "production", "platform"), CreatedAt: daysAgo(700)}}
	}
	secrets := []string{
		"shopfleet/orders-db/password", "shopfleet/payments-aurora/password", "shopfleet/catalog-db/password",
		"shopfleet/stripe/api-key", "shopfleet/sendgrid/api-key", "shopfleet/inventory-db/password",
		"shopfleet/search-meta-db/password", "shopfleet/notifications-db/password",
		"shopfleet/jwt/signing-key", "shopfleet/internal/service-token",
	}
	for _, name := range secrets {
		id := nextID("secret")
		e.Secrets[id] = &Secret{Base: Base{ID: id, Name: name, Region: region, State: cloud.StateInUse,
			Tags: mkTags(tagged(), name, "platform", "production", "platform"), CreatedAt: daysAgo(600)}}
	}
}

// instancesForApp returns up to n running instance ids tagged for the given
// application, for wiring a target group's membership.
func instancesForApp(e *Estate, app string, n int) []string {
	var out []string
	for id, inst := range e.EC2Instances {
		if inst.State != cloud.StateRunning {
			continue
		}
		if v, ok := inst.Tags["Application"]; ok && v == app {
			out = append(out, id)
			if len(out) >= n {
				break
			}
		}
	}
	return out
}

// lbIDByName and lambdaIDByName look up a just-created resource's id by its
// display name, so later build steps (CloudFront's origin, API Gateway's
// target) can reference resources built by an earlier step without threading
// ids through every function signature.
func lbIDByName(e *Estate, name string) string {
	for id, lb := range e.LoadBalancers {
		if lb.Name == name {
			return id
		}
	}
	return ""
}

func lambdaIDByName(e *Estate, name string) string {
	for id, fn := range e.LambdaFunctions {
		if fn.Name == name {
			return id
		}
	}
	return ""
}
