package sts

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	orgtypes "github.com/aws/aws-sdk-go-v2/service/organizations/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
)

// The probe fakes below each satisfy exactly one of the narrow *ProbeAPI
// interfaces verify.go declares, and are wired into a Broker by overwriting
// its probes field directly (this file is in package sts, so the unexported
// field is reachable) rather than through any exported constructor — Verify
// is the only thing under test here, not the client-construction wiring
// defaultProbes provides for production.

type fakeEC2Probe struct {
	describeErr error
	stopErr     error
}

func (f *fakeEC2Probe) DescribeInstances(context.Context, *ec2.DescribeInstancesInput, ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	return &ec2.DescribeInstancesOutput{}, f.describeErr
}
func (f *fakeEC2Probe) StopInstances(context.Context, *ec2.StopInstancesInput, ...func(*ec2.Options)) (*ec2.StopInstancesOutput, error) {
	return &ec2.StopInstancesOutput{}, f.stopErr
}

type fakeCEProbe struct{ err error }

func (f *fakeCEProbe) GetCostAndUsage(context.Context, *costexplorer.GetCostAndUsageInput, ...func(*costexplorer.Options)) (*costexplorer.GetCostAndUsageOutput, error) {
	return &costexplorer.GetCostAndUsageOutput{}, f.err
}

type fakeOrgProbe struct {
	masterAccountID string
	err             error
}

func (f *fakeOrgProbe) DescribeOrganization(context.Context, *organizations.DescribeOrganizationInput, ...func(*organizations.Options)) (*organizations.DescribeOrganizationOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &organizations.DescribeOrganizationOutput{
		Organization: &orgtypes.Organization{MasterAccountId: aws.String(f.masterAccountID)},
	}, nil
}

type fakeS3Probe struct{ err error }

func (f *fakeS3Probe) HeadBucket(context.Context, *s3.HeadBucketInput, ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
	return &s3.HeadBucketOutput{}, f.err
}

func dryRunOK() error {
	return &smithy.GenericAPIError{Code: "DryRunOperation", Message: "Request would have succeeded"}
}

func deniedErr(action string) error {
	return &smithy.GenericAPIError{Code: "UnauthorizedOperation",
		Message: "You are not authorized to perform this operation. User is not authorized to perform: " + action + " on resource"}
}

func setupVerifyBroker(t *testing.T, ec2p *fakeEC2Probe, cep *fakeCEProbe, orgp *fakeOrgProbe, s3p *fakeS3Probe) (*Broker, *fakeSTS) {
	t.Helper()
	fake := newFakeSTS()
	b := newTestBroker(fake)
	b.probes = probeSet{
		ec2: func(aws.Config) ec2ProbeAPI { return ec2p },
		ce:  func(aws.Config) costExplorerProbeAPI { return cep },
		org: func(aws.Config) organizationsProbeAPI { return orgp },
		s3:  func(aws.Config) s3ProbeAPI { return s3p },
	}
	return b, fake
}

func TestVerify_AllScopesGrantedAndPayer(t *testing.T) {
	b, fake := setupVerifyBroker(t,
		&fakeEC2Probe{describeErr: nil, stopErr: dryRunOK()},
		&fakeCEProbe{err: nil},
		&fakeOrgProbe{masterAccountID: "222222222222"},
		&fakeS3Probe{err: nil},
	)
	account := testAccount(fake)
	account.CURBucket = "cloudoptix-cur-222222222222"

	check, err := b.Verify(context.Background(), account)
	require.NoError(t, err)

	assert.True(t, check.Reachable)
	assert.ElementsMatch(t, []cloud.RoleScope{cloud.ScopeRead, cloud.ScopeAnalyze, cloud.ScopePlan, cloud.ScopeExecute}, check.GrantedScopes)
	assert.Empty(t, check.MissingActions)
	assert.True(t, check.CostExplorerAvailable)
	assert.True(t, check.IsPayer)
	assert.True(t, check.CURAvailable)
	assert.NotEmpty(t, check.IdentityARN)
}

func TestVerify_ReportsExactDeniedActions(t *testing.T) {
	b, fake := setupVerifyBroker(t,
		&fakeEC2Probe{describeErr: deniedErr("ec2:DescribeInstances"), stopErr: deniedErr("ec2:StopInstances")},
		&fakeCEProbe{err: deniedErr("ce:GetCostAndUsage")},
		&fakeOrgProbe{err: deniedErr("organizations:DescribeOrganization")},
		&fakeS3Probe{err: deniedErr("s3:HeadBucket")},
	)
	account := testAccount(fake)

	check, err := b.Verify(context.Background(), account)
	require.NoError(t, err)

	// AssumeRole + GetCallerIdentity still succeeded for every scope, so
	// every scope is still "granted" at the role level — it is the
	// finer-grained probe that reports the specific denied action.
	assert.ElementsMatch(t, []cloud.RoleScope{cloud.ScopeRead, cloud.ScopeAnalyze, cloud.ScopePlan, cloud.ScopeExecute}, check.GrantedScopes)

	require.Contains(t, check.MissingActions, string(cloud.ScopeRead))
	assert.Equal(t, []string{"ec2:DescribeInstances"}, check.MissingActions[string(cloud.ScopeRead)])

	require.Contains(t, check.MissingActions, string(cloud.ScopeAnalyze))
	assert.Equal(t, []string{"ce:GetCostAndUsage"}, check.MissingActions[string(cloud.ScopeAnalyze)])

	require.Contains(t, check.MissingActions, string(cloud.ScopePlan))
	assert.Equal(t, []string{"ec2:DescribeInstances"}, check.MissingActions[string(cloud.ScopePlan)])

	require.Contains(t, check.MissingActions, string(cloud.ScopeExecute))
	assert.Equal(t, []string{"ec2:StopInstances"}, check.MissingActions[string(cloud.ScopeExecute)])

	assert.False(t, check.CostExplorerAvailable)
	assert.False(t, check.IsPayer)
}

func TestVerify_UngrantedScopeIsSkippedNotFailed(t *testing.T) {
	b, fake := setupVerifyBroker(t, &fakeEC2Probe{}, &fakeCEProbe{}, &fakeOrgProbe{}, &fakeS3Probe{})
	account := testAccount(fake)
	delete(account.RoleARNs, cloud.ScopeExecute)

	check, err := b.Verify(context.Background(), account)
	require.NoError(t, err)

	assert.NotContains(t, check.GrantedScopes, cloud.ScopeExecute)
	assert.Empty(t, check.Errors)
}

func TestVerify_AssumeRoleFailureIsReported(t *testing.T) {
	b, fake := setupVerifyBroker(t, &fakeEC2Probe{}, &fakeCEProbe{}, &fakeOrgProbe{}, &fakeS3Probe{})
	fake.assumeErr = deniedErr("sts:AssumeRole")
	account := testAccount(fake)

	check, err := b.Verify(context.Background(), account)
	require.NoError(t, err)

	assert.False(t, check.Reachable)
	assert.Empty(t, check.GrantedScopes)
	assert.NotEmpty(t, check.Errors)
}
