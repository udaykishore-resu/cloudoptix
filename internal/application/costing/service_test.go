package costing

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/memstore"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cost"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

type fakeSession struct{}

func (fakeSession) AccountID() core.AccountID { return "111111111111" }
func (fakeSession) Scope() cloud.RoleScope    { return cloud.ScopeAnalyze }
func (fakeSession) ExpiresAt() time.Time      { return time.Now().Add(time.Hour) }
func (fakeSession) Config(core.Region) any    { return nil }

type fakeBroker struct{}

func (fakeBroker) Assume(ctx context.Context, account cloud.AWSAccount, scope cloud.RoleScope) (ports.AWSSession, error) {
	return fakeSession{}, nil
}
func (fakeBroker) Verify(ctx context.Context, account cloud.AWSAccount) (ports.ConnectionCheck, error) {
	return ports.ConnectionCheck{}, nil
}

// fakeIngestor returns one fixed record set, tagged with its own source name
// so a test can tell which ingestor actually ran.
type fakeIngestor struct {
	source    string
	available bool
	records   []cost.Record
}

func (f fakeIngestor) Source() string { return f.source }
func (f fakeIngestor) Available(ctx context.Context, session ports.AWSSession, account cloud.AWSAccount) bool {
	return f.available
}
func (f fakeIngestor) Fetch(ctx context.Context, in ports.CostIngestInput) ([]cost.Record, error) {
	return f.records, nil
}

func TestIngest_PrefersCUROverCostExplorer(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant := core.TenantID("tnt_ing1")
	ctx := testCtx(tenant)

	account := cloud.AWSAccount{
		ID: core.NewID("acc"), TenantID: tenant, AccountID: "111111111111",
		Regions: []core.Region{"us-east-1"}, AccessMode: cloud.AccessAssumeRole,
		RoleARNs:   map[cloud.RoleScope]core.ARN{cloud.ScopeRead: "arn:aws:iam::111111111111:role/read"},
		ExternalID: "ext", State: cloud.ConnConnected, CreatedAt: time.Now(),
	}
	require.NoError(t, repos.AWSAccounts.Create(ctx, account))

	period := core.PeriodOfDays(time.Now(), 7)
	curRecords := []cost.Record{{
		TenantID: tenant, AccountID: account.AccountID, Period: period, Basis: cost.BasisAmortized,
		Service: "EC2", ChargeType: cost.ChargeUsage, Amount: core.USDollars(50), Source: "cur",
	}}
	ceRecords := []cost.Record{{
		TenantID: tenant, AccountID: account.AccountID, Period: period, Basis: cost.BasisAmortized,
		Service: "EC2", ChargeType: cost.ChargeUsage, Amount: core.USDollars(999), Source: "cost_explorer",
	}}
	svc := &Service{
		Repos:  repos,
		Broker: fakeBroker{},
		Ingestors: []ports.CostIngestor{
			fakeIngestor{source: "cost_explorer", available: true, records: ceRecords},
			fakeIngestor{source: "cur", available: true, records: curRecords},
		},
		Clock: core.SystemClock{},
	}

	result, err := svc.Ingest(ctx, tenant, account.ID, period)
	require.NoError(t, err)
	assert.Equal(t, "cur", result.Source)
	assert.Equal(t, core.USDollars(50), result.TotalCost)
}

func TestIngest_FallsBackToCostExplorerWhenCURUnavailable(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant := core.TenantID("tnt_ing2")
	ctx := testCtx(tenant)

	account := cloud.AWSAccount{
		ID: core.NewID("acc"), TenantID: tenant, AccountID: "222222222222",
		Regions: []core.Region{"us-east-1"}, AccessMode: cloud.AccessAssumeRole,
		RoleARNs:   map[cloud.RoleScope]core.ARN{cloud.ScopeRead: "arn:aws:iam::222222222222:role/read"},
		ExternalID: "ext", State: cloud.ConnConnected, CreatedAt: time.Now(),
	}
	require.NoError(t, repos.AWSAccounts.Create(ctx, account))

	period := core.PeriodOfDays(time.Now(), 7)
	ceRecords := []cost.Record{{
		TenantID: tenant, AccountID: account.AccountID, Period: period, Basis: cost.BasisAmortized,
		Service: "S3", ChargeType: cost.ChargeUsage, Amount: core.USDollars(75),
	}}
	svc := &Service{
		Repos:  repos,
		Broker: fakeBroker{},
		Ingestors: []ports.CostIngestor{
			fakeIngestor{source: "cur", available: false},
			fakeIngestor{source: "cost_explorer", available: true, records: ceRecords},
		},
		Clock: core.SystemClock{},
	}

	result, err := svc.Ingest(ctx, tenant, account.ID, period)
	require.NoError(t, err)
	assert.Equal(t, "cost_explorer", result.Source)
	assert.Equal(t, core.USDollars(75), result.TotalCost)
}

func TestIngest_ResolvesResourceIDsByARNAndReportsCoverage(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant := core.TenantID("tnt_ing3")
	ctx := testCtx(tenant)

	account := cloud.AWSAccount{
		ID: core.NewID("acc"), TenantID: tenant, AccountID: "333333333333",
		Regions: []core.Region{"us-east-1"}, AccessMode: cloud.AccessAssumeRole,
		RoleARNs:   map[cloud.RoleScope]core.ARN{cloud.ScopeRead: "arn:aws:iam::333333333333:role/read"},
		ExternalID: "ext", State: cloud.ConnConnected, CreatedAt: time.Now(),
	}
	require.NoError(t, repos.AWSAccounts.Create(ctx, account))

	knownARN := core.ARN("arn:aws:ec2:us-east-1:333333333333:instance/i-0abc")
	_, err := repos.Resources.UpsertBatch(ctx, tenant, []cloud.Resource{{
		TenantID: tenant, AccountID: account.AccountID, Region: "us-east-1",
		Kind: cloud.KindEC2Instance, ARN: knownARN, NativeID: "i-0abc", LastSeenAt: time.Now(),
	}})
	require.NoError(t, err)

	period := core.PeriodOfDays(time.Now(), 7)
	records := []cost.Record{
		{TenantID: tenant, AccountID: account.AccountID, Period: period, Basis: cost.BasisAmortized,
			Service: "EC2", ChargeType: cost.ChargeUsage, Amount: core.USDollars(40), ResourceARN: knownARN},
		{TenantID: tenant, AccountID: account.AccountID, Period: period, Basis: cost.BasisAmortized,
			Service: "EC2", ChargeType: cost.ChargeUsage, Amount: core.USDollars(10), ResourceARN: "arn:aws:ec2:us-east-1:333333333333:instance/i-unknown"},
	}
	svc := &Service{
		Repos:     repos,
		Broker:    fakeBroker{},
		Ingestors: []ports.CostIngestor{fakeIngestor{source: "cur", available: true, records: records}},
		Clock:     core.SystemClock{},
	}

	result, err := svc.Ingest(ctx, tenant, account.ID, period)
	require.NoError(t, err)
	assert.InDelta(t, 0.5, result.ResourceCoverage, 0.001, "exactly one of two attributable records should have joined")

	byResource, err := repos.Costs.ByResource(ctx, tenant, ports.CostFilter{Period: period})
	require.NoError(t, err)
	assert.Len(t, byResource, 1, "only the record with a known ARN should have resolved to a resource id")
}

func TestIngest_NoAvailableSourceErrors(t *testing.T) {
	store := memstore.New()
	repos := store.Repositories()
	tenant := core.TenantID("tnt_ing4")
	ctx := testCtx(tenant)
	account := cloud.AWSAccount{
		ID: core.NewID("acc"), TenantID: tenant, AccountID: "444444444444",
		Regions: []core.Region{"us-east-1"}, AccessMode: cloud.AccessAssumeRole,
		RoleARNs:   map[cloud.RoleScope]core.ARN{cloud.ScopeRead: "arn:aws:iam::444444444444:role/read"},
		ExternalID: "ext", State: cloud.ConnConnected, CreatedAt: time.Now(),
	}
	require.NoError(t, repos.AWSAccounts.Create(ctx, account))

	svc := &Service{
		Repos: repos, Broker: fakeBroker{},
		Ingestors: []ports.CostIngestor{fakeIngestor{source: "cur", available: false}},
		Clock:     core.SystemClock{},
	}
	_, err := svc.Ingest(ctx, tenant, account.ID, core.PeriodOfDays(time.Now(), 7))
	require.Error(t, err)
}
