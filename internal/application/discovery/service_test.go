package discovery

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/memstore"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// newTestService wires a Service against a fresh in-memory store and the
// given discoverers, with retry timing collapsed to near-zero so tests that
// exercise the retry loop run fast.
func newTestService(store *memstore.Store, broker ports.AWSCredentialBroker, discoverers ...ports.ResourceDiscoverer) *Service {
	s := NewService(store.Repositories(), broker, discoverers, nil, store.Locker())
	s.BaseBackoff = time.Millisecond
	s.MaxBackoff = 5 * time.Millisecond
	return s
}

func TestRun_HappyPathDiscoversPersistsAndReportsFullCoverage(t *testing.T) {
	store := memstore.New()
	tenant := core.TenantID("tnt_disc1")
	account := mkAccount(tenant, "111111111111", core.EnvProduction, "us-east-1")
	require.NoError(t, store.Repositories().AWSAccounts.Create(ctxFor(tenant), account))

	disc := &fakeDiscoverer{
		service: "ec2", kinds: []cloud.Kind{cloud.KindEC2Instance},
		byRegion: map[core.Region][]cloud.Resource{
			"us-east-1": {mkResource(tenant, cloud.KindEC2Instance, "i-1", nil), mkResource(tenant, cloud.KindEC2Instance, "i-2", nil)},
		},
	}
	broker := &fakeBroker{}
	svc := newTestService(store, broker, disc)

	run, err := svc.Run(ctxFor(tenant), tenant, ports.DiscoveryRequest{})
	require.NoError(t, err)

	assert.Equal(t, "completed", run.State)
	assert.Equal(t, 1.0, run.Coverage)
	assert.Equal(t, 2, run.ResourcesDiscovered)
	assert.NotNil(t, run.FinishedAt)

	inv, err := store.Repositories().Resources.LoadInventory(ctxFor(tenant), tenant, ports.ResourceFilter{})
	require.NoError(t, err)
	assert.Len(t, inv.All(), 2)
}

func TestRun_TombstonesResourceNoLongerObservedByASucceedingDiscoverer(t *testing.T) {
	store := memstore.New()
	tenant := core.TenantID("tnt_disc2")
	account := mkAccount(tenant, "111111111111", core.EnvProduction, "us-east-1")
	require.NoError(t, store.Repositories().AWSAccounts.Create(ctxFor(tenant), account))

	// Seed a pre-existing EC2 instance that this run's discoverer will not
	// report — it must be tombstoned because the same kind, in the same
	// account/region, succeeded this run without seeing it.
	gone := mkResource(tenant, cloud.KindEC2Instance, "i-gone", nil)
	gone.TenantID, gone.AccountID, gone.Region = tenant, "111111111111", "us-east-1"
	_, err := store.Repositories().Resources.UpsertBatch(ctxFor(tenant), tenant, []cloud.Resource{gone})
	require.NoError(t, err)

	disc := &fakeDiscoverer{
		service: "ec2", kinds: []cloud.Kind{cloud.KindEC2Instance},
		byRegion: map[core.Region][]cloud.Resource{
			"us-east-1": {mkResource(tenant, cloud.KindEC2Instance, "i-1", nil)},
		},
	}
	svc := newTestService(store, &fakeBroker{}, disc)

	run, err := svc.Run(ctxFor(tenant), tenant, ports.DiscoveryRequest{})
	require.NoError(t, err)
	assert.Equal(t, 1, run.ResourcesRemoved)

	inv, err := store.Repositories().Resources.LoadInventory(ctxFor(tenant), tenant, ports.ResourceFilter{})
	require.NoError(t, err)
	names := map[string]bool{}
	for _, r := range inv.All() {
		names[r.NativeID] = true
	}
	assert.True(t, names["i-1"])
	assert.False(t, names["i-gone"], "the resource no longer observed should be tombstoned out of the live inventory")
}

func TestRun_PartialScanCannotDeleteResourcesOfAKindItNeverSucceededAt(t *testing.T) {
	store := memstore.New()
	tenant := core.TenantID("tnt_disc3")
	account := mkAccount(tenant, "111111111111", core.EnvProduction, "us-east-1")
	require.NoError(t, store.Repositories().AWSAccounts.Create(ctxFor(tenant), account))

	// Seed an S3 bucket that no discoverer in this run reports — its kind
	// (S3) is only ever covered by the s3 discoverer, which fails outright.
	survivor := mkResource(tenant, cloud.KindS3Bucket, "bucket-1", nil)
	survivor.TenantID, survivor.AccountID, survivor.Region = tenant, "111111111111", "us-east-1"
	_, err := store.Repositories().Resources.UpsertBatch(ctxFor(tenant), tenant, []cloud.Resource{survivor})
	require.NoError(t, err)

	ec2Disc := &fakeDiscoverer{
		service: "ec2", kinds: []cloud.Kind{cloud.KindEC2Instance},
		byRegion: map[core.Region][]cloud.Resource{"us-east-1": {mkResource(tenant, cloud.KindEC2Instance, "i-1", nil)}},
	}
	s3Disc := &fakeDiscoverer{
		service: "s3", kinds: []cloud.Kind{cloud.KindS3Bucket}, actions: []string{"s3:ListAllMyBuckets"},
		permanentErr: map[core.Region]error{"us-east-1": core.Forbidden("access denied").WithDetail("action", "s3:ListAllMyBuckets")},
	}
	svc := newTestService(store, &fakeBroker{}, ec2Disc, s3Disc)
	svc.MaxRetries = 1

	run, err := svc.Run(ctxFor(tenant), tenant, ports.DiscoveryRequest{})
	require.NoError(t, err)
	assert.Equal(t, "partial", run.State)
	assert.Equal(t, 0, run.ResourcesRemoved, "the failed s3 discoverer's kind must not be tombstoned by an unrelated succeeding discoverer")

	inv, err := store.Repositories().Resources.LoadInventory(ctxFor(tenant), tenant, ports.ResourceFilter{})
	require.NoError(t, err)
	found := false
	for _, r := range inv.All() {
		if r.NativeID == "bucket-1" {
			found = true
		}
	}
	assert.True(t, found, "the s3 bucket must survive a scan whose s3 discoverer never succeeded")
}

func TestRun_RetriesThrottledAttemptsThenSucceeds(t *testing.T) {
	store := memstore.New()
	tenant := core.TenantID("tnt_disc4")
	account := mkAccount(tenant, "111111111111", core.EnvProduction, "us-east-1")
	require.NoError(t, store.Repositories().AWSAccounts.Create(ctxFor(tenant), account))

	disc := &fakeDiscoverer{
		service: "ec2", kinds: []cloud.Kind{cloud.KindEC2Instance},
		byRegion:         map[core.Region][]cloud.Resource{"us-east-1": {mkResource(tenant, cloud.KindEC2Instance, "i-1", nil)}},
		failUntilAttempt: map[core.Region]int{"us-east-1": 2},
	}
	svc := newTestService(store, &fakeBroker{}, disc)
	svc.MaxRetries = 4

	run, err := svc.Run(ctxFor(tenant), tenant, ports.DiscoveryRequest{})
	require.NoError(t, err)
	assert.Equal(t, "completed", run.State)
	assert.Equal(t, 3, disc.callCount("us-east-1"), "two throttled attempts then a third that succeeds")
	require.Len(t, run.ServiceResults, 1)
	assert.True(t, run.ServiceResults[0].Succeeded)
	assert.Equal(t, 2, run.ServiceResults[0].Throttled)
}

func TestRun_ExhaustingRetriesReportsFailureWithoutPanicking(t *testing.T) {
	store := memstore.New()
	tenant := core.TenantID("tnt_disc4b")
	account := mkAccount(tenant, "111111111111", core.EnvProduction, "us-east-1")
	require.NoError(t, store.Repositories().AWSAccounts.Create(ctxFor(tenant), account))

	disc := &fakeDiscoverer{
		service: "ec2", kinds: []cloud.Kind{cloud.KindEC2Instance},
		failUntilAttempt: map[core.Region]int{"us-east-1": 99},
	}
	svc := newTestService(store, &fakeBroker{}, disc)
	svc.MaxRetries = 3

	run, err := svc.Run(ctxFor(tenant), tenant, ports.DiscoveryRequest{})
	require.NoError(t, err)
	assert.Equal(t, "failed", run.State)
	assert.Equal(t, 3, disc.callCount("us-east-1"))
	require.Len(t, run.ServiceResults, 1)
	assert.False(t, run.ServiceResults[0].Succeeded)
	assert.NotEmpty(t, run.ServiceResults[0].Error)
}

func TestRun_PermissionDeniedIsNotRetriedAndReportsTheMissingAction(t *testing.T) {
	store := memstore.New()
	tenant := core.TenantID("tnt_disc5")
	account := mkAccount(tenant, "111111111111", core.EnvProduction, "us-east-1")
	require.NoError(t, store.Repositories().AWSAccounts.Create(ctxFor(tenant), account))

	disc := &fakeDiscoverer{
		service: "rds", kinds: []cloud.Kind{cloud.KindRDSInstance}, actions: []string{"rds:DescribeDBInstances"},
		permanentErr: map[core.Region]error{
			"us-east-1": core.Forbidden("access denied").WithDetail("action", "rds:DescribeDBInstances"),
		},
	}
	svc := newTestService(store, &fakeBroker{}, disc)
	svc.MaxRetries = 4

	run, err := svc.Run(ctxFor(tenant), tenant, ports.DiscoveryRequest{})
	require.NoError(t, err)
	assert.Equal(t, 1, disc.callCount("us-east-1"), "a permission failure must never be retried")
	require.Len(t, run.ServiceResults, 1)
	assert.False(t, run.ServiceResults[0].Succeeded)
	assert.Equal(t, "rds:DescribeDBInstances", run.ServiceResults[0].MissingPermission)

	status, err := svc.Status(ctxFor(tenant), tenant)
	require.NoError(t, err)
	require.Len(t, status.PermissionIssues, 1)
	assert.Contains(t, status.PermissionIssues[0], "rds:DescribeDBInstances")
}

func TestRun_AccountAssumeRoleFailureIsIsolatedFromOtherAccounts(t *testing.T) {
	store := memstore.New()
	tenant := core.TenantID("tnt_disc6")
	bad := mkAccount(tenant, "222222222222", core.EnvProduction, "us-east-1")
	good := mkAccount(tenant, "333333333333", core.EnvProduction, "us-east-1")
	require.NoError(t, store.Repositories().AWSAccounts.Create(ctxFor(tenant), bad))
	require.NoError(t, store.Repositories().AWSAccounts.Create(ctxFor(tenant), good))

	disc := &fakeDiscoverer{
		service: "ec2", kinds: []cloud.Kind{cloud.KindEC2Instance},
		byRegion: map[core.Region][]cloud.Resource{"us-east-1": {mkResource(tenant, cloud.KindEC2Instance, "i-1", nil)}},
	}
	broker := &fakeBroker{failFor: map[core.AccountID]error{"222222222222": core.Forbidden("role not assumable")}}
	svc := newTestService(store, broker, disc)

	run, err := svc.Run(ctxFor(tenant), tenant, ports.DiscoveryRequest{})
	require.NoError(t, err)
	assert.Equal(t, "partial", run.State)
	assert.Equal(t, 1, run.ResourcesDiscovered, "the good account's resources must still be discovered")
	require.Len(t, run.Errors, 1)
	assert.Contains(t, run.Errors[0], "222222222222")
}

func TestRun_AttributionResolvesEnvironmentFromTagOverAccountConvention(t *testing.T) {
	store := memstore.New()
	tenant := core.TenantID("tnt_disc7")
	account := mkAccount(tenant, "111111111111", core.EnvProduction, "us-east-1")
	require.NoError(t, store.Repositories().AWSAccounts.Create(ctxFor(tenant), account))

	tagged := mkResource(tenant, cloud.KindEC2Instance, "i-tagged", core.Tags{"Environment": "staging"})
	untagged := mkResource(tenant, cloud.KindEC2Instance, "i-untagged", nil)
	disc := &fakeDiscoverer{
		service: "ec2", kinds: []cloud.Kind{cloud.KindEC2Instance},
		byRegion: map[core.Region][]cloud.Resource{"us-east-1": {tagged, untagged}},
	}
	svc := newTestService(store, &fakeBroker{}, disc)

	_, err := svc.Run(ctxFor(tenant), tenant, ports.DiscoveryRequest{})
	require.NoError(t, err)

	inv, err := store.Repositories().Resources.LoadInventory(ctxFor(tenant), tenant, ports.ResourceFilter{})
	require.NoError(t, err)
	byNative := map[string]cloud.Resource{}
	for _, r := range inv.All() {
		byNative[r.NativeID] = r
	}

	got := byNative["i-tagged"]
	assert.Equal(t, core.EnvStaging, got.Environment)
	assert.Equal(t, core.ProvenanceConfirmed, got.EnvironmentSource, "a tag on the resource itself is a confirmed fact")

	got = byNative["i-untagged"]
	assert.Equal(t, core.EnvProduction, got.Environment, "falls back to the account's onboarded environment")
	assert.Equal(t, core.ProvenanceInferred, got.EnvironmentSource, "an account convention is only ever inferred")
}

func TestRun_AttributionAppliesFirstMatchingRuleByPriorityAndPrefersWorkloadOverApplication(t *testing.T) {
	store := memstore.New()
	tenant := core.TenantID("tnt_disc8")
	account := mkAccount(tenant, "111111111111", core.EnvProduction, "us-east-1")
	require.NoError(t, store.Repositories().AWSAccounts.Create(ctxFor(tenant), account))

	appID := core.NewID("app")
	wlID := core.NewID("wl")
	require.NoError(t, store.Repositories().Applications.UpsertApplication(ctxFor(tenant), cloud.Application{
		ID: appID, TenantID: tenant, Name: "Checkout", Slug: "checkout",
		MatchRules: []cloud.AttributionRule{
			{Priority: 10, NamePrefix: "checkout-"},
		},
	}))
	require.NoError(t, store.Repositories().Applications.UpsertWorkload(ctxFor(tenant), cloud.Workload{
		ID: wlID, TenantID: tenant, ApplicationID: appID, Name: "checkout-api",
		MatchRules: []cloud.AttributionRule{
			{Priority: 5, NamePrefix: "checkout-api-"},
		},
	}))

	apiResource := mkResource(tenant, cloud.KindEC2Instance, "checkout-api-1", nil)
	otherResource := mkResource(tenant, cloud.KindEC2Instance, "checkout-worker-1", nil)
	disc := &fakeDiscoverer{
		service: "ec2", kinds: []cloud.Kind{cloud.KindEC2Instance},
		byRegion: map[core.Region][]cloud.Resource{"us-east-1": {apiResource, otherResource}},
	}
	svc := newTestService(store, &fakeBroker{}, disc)

	_, err := svc.Run(ctxFor(tenant), tenant, ports.DiscoveryRequest{})
	require.NoError(t, err)

	inv, err := store.Repositories().Resources.LoadInventory(ctxFor(tenant), tenant, ports.ResourceFilter{})
	require.NoError(t, err)
	byNative := map[string]cloud.Resource{}
	for _, r := range inv.All() {
		byNative[r.NativeID] = r
	}

	got := byNative["checkout-api-1"]
	assert.Equal(t, appID, got.ApplicationID)
	assert.Equal(t, wlID, got.WorkloadID, "the more specific workload rule must win over the application-level rule")

	got = byNative["checkout-worker-1"]
	assert.Equal(t, appID, got.ApplicationID, "falls through to the application rule when no workload rule matches")
	assert.True(t, got.WorkloadID.IsZero())
}

func TestRun_MultiAccountScanAggregatesIntoOneRunRecord(t *testing.T) {
	store := memstore.New()
	tenant := core.TenantID("tnt_disc9")
	a1 := mkAccount(tenant, "111111111111", core.EnvProduction, "us-east-1")
	a2 := mkAccount(tenant, "222222222222", core.EnvStaging, "us-west-2")
	require.NoError(t, store.Repositories().AWSAccounts.Create(ctxFor(tenant), a1))
	require.NoError(t, store.Repositories().AWSAccounts.Create(ctxFor(tenant), a2))

	disc := &fakeDiscoverer{
		service: "ec2", kinds: []cloud.Kind{cloud.KindEC2Instance},
		byRegion: map[core.Region][]cloud.Resource{
			"us-east-1": {mkResource(tenant, cloud.KindEC2Instance, "i-1", nil)},
			"us-west-2": {mkResource(tenant, cloud.KindEC2Instance, "i-2", nil), mkResource(tenant, cloud.KindEC2Instance, "i-3", nil)},
		},
	}
	svc := newTestService(store, &fakeBroker{}, disc)

	run, err := svc.Run(ctxFor(tenant), tenant, ports.DiscoveryRequest{})
	require.NoError(t, err)

	assert.Equal(t, "completed", run.State)
	assert.Equal(t, 3, run.ResourcesDiscovered)
	assert.Equal(t, core.AccountID(""), run.AccountID, "a multi-account run leaves the single-account field empty rather than picking one arbitrarily")
	assert.ElementsMatch(t, []core.Region{"us-east-1", "us-west-2"}, run.Regions)

	runs, err := store.Repositories().DiscoveryRuns.ListRecent(ctxFor(tenant), tenant, 10)
	require.NoError(t, err)
	assert.Len(t, runs, 1, "one Run call must produce exactly one DiscoveryRun record regardless of account count")
}

func TestRun_AsyncReturnsImmediatelyThenCompletesInTheBackground(t *testing.T) {
	store := memstore.New()
	tenant := core.TenantID("tnt_disc10")
	account := mkAccount(tenant, "111111111111", core.EnvProduction, "us-east-1")
	require.NoError(t, store.Repositories().AWSAccounts.Create(ctxFor(tenant), account))

	disc := &fakeDiscoverer{
		service: "ec2", kinds: []cloud.Kind{cloud.KindEC2Instance},
		byRegion: map[core.Region][]cloud.Resource{"us-east-1": {mkResource(tenant, cloud.KindEC2Instance, "i-1", nil)}},
	}
	svc := newTestService(store, &fakeBroker{}, disc)

	run, err := svc.Run(ctxFor(tenant), tenant, ports.DiscoveryRequest{Async: true})
	require.NoError(t, err)
	assert.Equal(t, "running", run.State)
	assert.Nil(t, run.FinishedAt)

	deadline := time.Now().Add(2 * time.Second)
	var final ports.DiscoveryRun
	for time.Now().Before(deadline) {
		final, err = svc.Get(ctxFor(tenant), tenant, run.ID)
		require.NoError(t, err)
		if final.FinishedAt != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	require.NotNil(t, final.FinishedAt, "the async run must eventually finish")
	assert.Equal(t, "completed", final.State)
	assert.Equal(t, 1, final.ResourcesDiscovered)
}

func TestRun_NoConnectedAccountReturnsAnActionableError(t *testing.T) {
	store := memstore.New()
	tenant := core.TenantID("tnt_disc11")
	svc := newTestService(store, &fakeBroker{})

	_, err := svc.Run(ctxFor(tenant), tenant, ports.DiscoveryRequest{})
	require.Error(t, err)
}

func TestStatusAndListRuns_ReflectCoverageAndRecentHistory(t *testing.T) {
	store := memstore.New()
	tenant := core.TenantID("tnt_disc12")
	account := mkAccount(tenant, "111111111111", core.EnvProduction, "us-east-1")
	require.NoError(t, store.Repositories().AWSAccounts.Create(ctxFor(tenant), account))

	disc := &fakeDiscoverer{
		service: "ec2", kinds: []cloud.Kind{cloud.KindEC2Instance},
		byRegion: map[core.Region][]cloud.Resource{"us-east-1": {mkResource(tenant, cloud.KindEC2Instance, "i-1", nil)}},
	}
	svc := newTestService(store, &fakeBroker{}, disc)

	_, err := svc.Run(ctxFor(tenant), tenant, ports.DiscoveryRequest{})
	require.NoError(t, err)

	runs, err := svc.ListRuns(ctxFor(tenant), tenant, 10)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.Equal(t, "completed", runs[0].State)

	status, err := svc.Status(ctxFor(tenant), tenant)
	require.NoError(t, err)
	assert.False(t, status.InProgress)
	assert.Equal(t, 1, status.AccountsTotal)
	assert.Equal(t, 1, status.AccountsCovered)
	assert.Equal(t, 1.0, status.Coverage)
	assert.Equal(t, 1, status.ResourceCount)
	assert.Empty(t, status.PermissionIssues)
}
