package awsaccounts

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/execute"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

func TestRegister_RefusedWithNoApprovedSpec(t *testing.T) {
	svc, repos := newTestService(t, &fakeBroker{})
	seedTenant(t, repos, testTenant, false, false) // no approved spec

	_, _, err := svc.Register(ctxFor(testTenant), testTenant, ports.RegisterAccountInput{
		AccountID: "111122223333", Environment: core.EnvProduction, Regions: []core.Region{"us-east-1"},
		AccessMode: cloud.AccessAssumeRole,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrInvalidInput)
}

func TestRegister_SimulatedAccessRefusedForNonDemoTenant(t *testing.T) {
	svc, repos := newTestService(t, &fakeBroker{})
	seedTenant(t, repos, testTenant, false, true) // approved spec, not demo

	_, _, err := svc.Register(ctxFor(testTenant), testTenant, ports.RegisterAccountInput{
		AccountID: "111122223333", Environment: core.EnvProduction, Regions: []core.Region{"us-east-1"},
		AccessMode: cloud.AccessSimulated,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrForbidden)
}

func TestRegister_SimulatedAccessAllowedForDemoTenant(t *testing.T) {
	svc, repos := newTestService(t, &fakeBroker{})
	seedTenant(t, repos, testTenant, true, true) // approved spec, demo tenant

	account, instructions, err := svc.Register(ctxFor(testTenant), testTenant, ports.RegisterAccountInput{
		AccountID: "111122223333", Environment: core.EnvProduction, Regions: []core.Region{"us-east-1"},
		AccessMode: cloud.AccessSimulated,
	})
	require.NoError(t, err)
	assert.Equal(t, cloud.ConnPending, account.State)
	assert.Empty(t, account.ExternalID, "simulated accounts never need a confused-deputy external id")
	assert.NotEmpty(t, instructions.Instructions)
}

func TestRegister_AssumeRoleGeneratesExternalIDAndInstructions(t *testing.T) {
	svc, repos := newTestService(t, &fakeBroker{})
	seedTenant(t, repos, testTenant, false, true)

	account, instructions, err := svc.Register(ctxFor(testTenant), testTenant, ports.RegisterAccountInput{
		AccountID: "111122223333", Environment: core.EnvProduction, Regions: []core.Region{"us-east-1"},
		AccessMode: cloud.AccessAssumeRole,
		RoleARNs:   map[cloud.RoleScope]core.ARN{cloud.ScopeRead: "arn:aws:iam::111122223333:role/CloudOptix-acme-Read"},
	})
	require.NoError(t, err)
	assert.Equal(t, cloud.ConnPending, account.State)
	assert.NotEmpty(t, account.ExternalID)
	assert.Equal(t, account.ExternalID, instructions.ExternalID, "instructions must describe the exact external id the account was persisted with")
	assert.Contains(t, instructions.RoleNames, cloud.ScopeRead)
	assert.Contains(t, instructions.RoleNames, cloud.ScopeAnalyze)
	assert.NotContains(t, instructions.RoleNames, cloud.ScopeExecute, "automation is not enabled, so no execute-tier role should be requested")
}

func mkAccount(t interface{ Helper() }, repos ports.Repositories, tenant core.TenantID) cloud.AWSAccount {
	t.Helper()
	a := cloud.AWSAccount{
		ID: core.NewID("acct"), TenantID: tenant, AccountID: "111122223333",
		Environment: core.EnvProduction, Regions: []core.Region{"us-east-1"},
		AccessMode: cloud.AccessAssumeRole, ExternalID: "cloudoptix-test", State: cloud.ConnPending,
		CreatedAt: testNow, UpdatedAt: testNow,
	}
	if err := repos.AWSAccounts.Create(ctxFor(tenant), a); err != nil {
		panic(err)
	}
	return a
}

func TestVerify_FullGrantConnectsAccount(t *testing.T) {
	broker := &fakeBroker{check: ports.ConnectionCheck{
		Reachable: true, GrantedScopes: []cloud.RoleScope{cloud.ScopeRead, cloud.ScopeAnalyze},
	}}
	svc, repos := newTestService(t, broker)
	seedTenant(t, repos, testTenant, false, true)
	account := mkAccount(t, repos, testTenant)

	got, check, err := svc.Verify(ctxFor(testTenant), testTenant, account.ID)
	require.NoError(t, err)
	assert.Equal(t, cloud.ConnConnected, got.State)
	assert.Empty(t, got.MissingActions)
	assert.True(t, check.Reachable)
}

func TestVerify_PartialGrantDegradesWithMissingActionsPreserved(t *testing.T) {
	broker := &fakeBroker{check: ports.ConnectionCheck{
		Reachable:     true,
		GrantedScopes: []cloud.RoleScope{cloud.ScopeRead},
		MissingActions: map[string][]string{
			"analyze": {"ce:GetCostAndUsage", "cloudwatch:GetMetricData"},
		},
	}}
	svc, repos := newTestService(t, broker)
	seedTenant(t, repos, testTenant, false, true)
	account := mkAccount(t, repos, testTenant)

	got, _, err := svc.Verify(ctxFor(testTenant), testTenant, account.ID)
	require.NoError(t, err)
	assert.Equal(t, cloud.ConnDegraded, got.State)
	assert.ElementsMatch(t, []string{"ce:GetCostAndUsage", "cloudwatch:GetMetricData"}, got.MissingActions,
		"the exact missing IAM actions reported by the broker must be preserved")
}

func TestVerify_UnreachableFails(t *testing.T) {
	broker := &fakeBroker{check: ports.ConnectionCheck{Reachable: false, Errors: []string{"AssumeRole denied"}}}
	svc, repos := newTestService(t, broker)
	seedTenant(t, repos, testTenant, false, true)
	account := mkAccount(t, repos, testTenant)

	got, _, err := svc.Verify(ctxFor(testTenant), testTenant, account.ID)
	require.NoError(t, err)
	assert.Equal(t, cloud.ConnFailed, got.State)
}

func TestRemove_RefusedWithInFlightPlan(t *testing.T) {
	svc, repos := newTestService(t, &fakeBroker{})
	seedTenant(t, repos, testTenant, false, true)
	account := mkAccount(t, repos, testTenant)

	plan := execute.Plan{
		ID: core.NewID("plan"), TenantID: testTenant, AccountID: account.AccountID,
		Action: optimize.ActionStopInstance, State: execute.PlanExecuting, CreatedAt: testNow,
	}
	require.NoError(t, repos.Executions.CreatePlan(ctxFor(testTenant), plan))

	err := svc.Remove(ctxFor(testTenant), testTenant, account.ID)
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrConflict)

	// The account must still exist: the refusal must not have partially applied.
	_, getErr := svc.Get(ctxFor(testTenant), testTenant, account.ID)
	assert.NoError(t, getErr)
}

func TestRemove_SucceedsWithNoInFlightPlan(t *testing.T) {
	svc, repos := newTestService(t, &fakeBroker{})
	seedTenant(t, repos, testTenant, false, true)
	account := mkAccount(t, repos, testTenant)

	require.NoError(t, svc.Remove(ctxFor(testTenant), testTenant, account.ID))
	_, err := svc.Get(ctxFor(testTenant), testTenant, account.ID)
	assert.ErrorIs(t, err, core.ErrNotFound)
}

func TestRemove_SucceedsWhenOnlyTerminalPlansExist(t *testing.T) {
	svc, repos := newTestService(t, &fakeBroker{})
	seedTenant(t, repos, testTenant, false, true)
	account := mkAccount(t, repos, testTenant)

	plan := execute.Plan{
		ID: core.NewID("plan"), TenantID: testTenant, AccountID: account.AccountID,
		Action: optimize.ActionStopInstance, State: execute.PlanValidated, CreatedAt: testNow,
	}
	require.NoError(t, repos.Executions.CreatePlan(ctxFor(testTenant), plan))

	require.NoError(t, svc.Remove(ctxFor(testTenant), testTenant, account.ID))
}
