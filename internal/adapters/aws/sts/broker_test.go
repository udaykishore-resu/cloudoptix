package sts

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssdksts "github.com/aws/aws-sdk-go-v2/service/sts"
	ststypes "github.com/aws/aws-sdk-go-v2/service/sts/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// fakeSTS is a hand-written stsAPI used by every test in this package. It
// never touches the network: AssumeRole mints a synthetic credential set and
// records the call it was given, which is what lets a test assert on
// exactly how many calls happened, in what order, and with what parameters
// — the things this package's caching, coalescing and chaining logic exist
// to control.
type fakeSTS struct {
	mu          sync.Mutex
	assumeCalls []awssdksts.AssumeRoleInput
	assumeErr   error
	credSeq     int32
	sessionTTL  time.Duration // how far in the future minted credentials expire
	identityErr error
}

func newFakeSTS() *fakeSTS { return &fakeSTS{sessionTTL: time.Hour} }

func (f *fakeSTS) AssumeRole(_ context.Context, in *awssdksts.AssumeRoleInput, _ ...func(*awssdksts.Options)) (*awssdksts.AssumeRoleOutput, error) {
	f.mu.Lock()
	f.assumeCalls = append(f.assumeCalls, *in)
	err := f.assumeErr
	ttl := f.sessionTTL
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	seq := atomic.AddInt32(&f.credSeq, 1)
	return &awssdksts.AssumeRoleOutput{
		Credentials: &ststypes.Credentials{
			AccessKeyId:     aws.String(fmt.Sprintf("AKIAFAKE%d", seq)),
			SecretAccessKey: aws.String("fake-secret"),
			SessionToken:    aws.String(fmt.Sprintf("fake-token-%d", seq)),
			Expiration:      aws.Time(time.Now().Add(ttl)),
		},
		AssumedRoleUser: &ststypes.AssumedRoleUser{
			Arn: aws.String(fmt.Sprintf("arn:aws:sts::111111111111:assumed-role/fake/%s", aws.ToString(in.RoleSessionName))),
		},
	}, nil
}

func (f *fakeSTS) GetCallerIdentity(_ context.Context, _ *awssdksts.GetCallerIdentityInput, _ ...func(*awssdksts.Options)) (*awssdksts.GetCallerIdentityOutput, error) {
	f.mu.Lock()
	err := f.identityErr
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return &awssdksts.GetCallerIdentityOutput{
		Account: aws.String("222222222222"),
		Arn:     aws.String("arn:aws:sts::222222222222:assumed-role/cloudoptix-read/session"),
		UserId:  aws.String("AROAEXAMPLE:session"),
	}, nil
}

func (f *fakeSTS) calls() []awssdksts.AssumeRoleInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]awssdksts.AssumeRoleInput, len(f.assumeCalls))
	copy(out, f.assumeCalls)
	return out
}

func testAccount(fake *fakeSTS) cloud.AWSAccount {
	return cloud.AWSAccount{
		AccountID:  "222222222222",
		ExternalID: "ext-id-12345",
		Regions:    []core.Region{"us-east-1"},
		AccessMode: cloud.AccessAssumeRole,
		RoleARNs: map[cloud.RoleScope]core.ARN{
			cloud.ScopeRead:    "arn:aws:iam::222222222222:role/cloudoptix-read",
			cloud.ScopeAnalyze: "arn:aws:iam::222222222222:role/cloudoptix-analyze",
			cloud.ScopePlan:    "arn:aws:iam::222222222222:role/cloudoptix-plan",
			cloud.ScopeExecute: "arn:aws:iam::222222222222:role/cloudoptix-execute",
		},
	}
}

func newTestBroker(fake *fakeSTS, opts ...Option) *Broker {
	allOpts := append([]Option{
		withSTSClientFactory(func(aws.Config) stsAPI { return fake }),
		WithPrincipal("test-worker"),
	}, opts...)
	return NewBroker(aws.Config{Region: "us-east-1"}, allOpts...)
}

func TestAssume_MintsAndCaches(t *testing.T) {
	fake := newFakeSTS()
	b := newTestBroker(fake)
	account := testAccount(fake)

	sess1, err := b.Assume(context.Background(), account, cloud.ScopeRead)
	require.NoError(t, err)
	sess2, err := b.Assume(context.Background(), account, cloud.ScopeRead)
	require.NoError(t, err)

	assert.Same(t, sess1, sess2, "a second Assume within the cache window must not mint a new session")
	assert.Len(t, fake.calls(), 1, "only one AssumeRole call should have happened")

	call := fake.calls()[0]
	assert.Equal(t, "arn:aws:iam::222222222222:role/cloudoptix-read", aws.ToString(call.RoleArn))
	assert.Equal(t, "ext-id-12345", aws.ToString(call.ExternalId))
	assert.Contains(t, aws.ToString(call.RoleSessionName), "cloudoptix")
	assert.Contains(t, aws.ToString(call.RoleSessionName), "test-worker")
	assert.LessOrEqual(t, len(aws.ToString(call.RoleSessionName)), 64)
}

func TestAssume_DifferentScopesAreIndependentSessions(t *testing.T) {
	fake := newFakeSTS()
	b := newTestBroker(fake)
	account := testAccount(fake)

	readSess, err := b.Assume(context.Background(), account, cloud.ScopeRead)
	require.NoError(t, err)
	execSess, err := b.Assume(context.Background(), account, cloud.ScopeExecute)
	require.NoError(t, err)

	assert.NotSame(t, readSess, execSess)
	assert.Equal(t, cloud.ScopeRead, readSess.Scope())
	assert.Equal(t, cloud.ScopeExecute, execSess.Scope())
	assert.Len(t, fake.calls(), 2)
}

func TestAssume_ConcurrentRequestsCoalesce(t *testing.T) {
	fake := newFakeSTS()
	b := newTestBroker(fake)
	account := testAccount(fake)

	const n = 50
	var wg sync.WaitGroup
	results := make([]ports.AWSSession, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sess, err := b.Assume(context.Background(), account, cloud.ScopeRead)
			require.NoError(t, err)
			results[i] = sess
		}(i)
	}
	wg.Wait()

	assert.Len(t, fake.calls(), 1, "concurrent Assume calls for the same account+scope must coalesce onto one AssumeRole call")
	for i := 1; i < n; i++ {
		assert.Same(t, results[0], results[i])
	}
}

func TestAssume_ProactiveRefreshBeforeExpiry(t *testing.T) {
	fake := newFakeSTS()
	fake.sessionTTL = 40 * time.Millisecond
	b := newTestBroker(fake, WithRefreshWindow(30*time.Millisecond))
	account := testAccount(fake)

	_, err := b.Assume(context.Background(), account, cloud.ScopeRead)
	require.NoError(t, err)
	assert.Len(t, fake.calls(), 1)

	// The refresh window (30ms) is close to the whole TTL (40ms), so a short
	// sleep crosses into "due for proactive renewal" without needing to wait
	// out full expiry.
	time.Sleep(15 * time.Millisecond)

	_, err = b.Assume(context.Background(), account, cloud.ScopeRead)
	require.NoError(t, err)
	assert.Len(t, fake.calls(), 2, "a session inside its refresh window must be renewed rather than reused")
}

func TestAssume_RequiresRoleARN(t *testing.T) {
	fake := newFakeSTS()
	b := newTestBroker(fake)
	account := testAccount(fake)
	delete(account.RoleARNs, cloud.ScopeExecute)

	_, err := b.Assume(context.Background(), account, cloud.ScopeExecute)
	assert.Error(t, err)
	assert.Empty(t, fake.calls())
}

func TestAssume_RequiresExternalID(t *testing.T) {
	fake := newFakeSTS()
	b := newTestBroker(fake)
	account := testAccount(fake)
	account.ExternalID = ""

	_, err := b.Assume(context.Background(), account, cloud.ScopeRead)
	assert.Error(t, err)
	assert.Empty(t, fake.calls())
}

func TestAssume_OrgChainAssumesManagementRoleFirst(t *testing.T) {
	fake := newFakeSTS()
	b := newTestBroker(fake, WithOrgChain(OrgChain{
		ManagementRoleARN: "arn:aws:iam::999999999999:role/cloudoptix-mgmt",
		ExternalID:        "mgmt-ext-id",
	}))
	account := testAccount(fake)

	_, err := b.Assume(context.Background(), account, cloud.ScopeRead)
	require.NoError(t, err)

	calls := fake.calls()
	require.Len(t, calls, 2, "org-chained access requires two AssumeRole hops")
	assert.Equal(t, "arn:aws:iam::999999999999:role/cloudoptix-mgmt", aws.ToString(calls[0].RoleArn))
	assert.Equal(t, "mgmt-ext-id", aws.ToString(calls[0].ExternalId))
	assert.Equal(t, "arn:aws:iam::222222222222:role/cloudoptix-read", aws.ToString(calls[1].RoleArn))
	assert.Equal(t, "ext-id-12345", aws.ToString(calls[1].ExternalId))
}

func TestAssume_SessionConfigCarriesRegionAndCredentials(t *testing.T) {
	fake := newFakeSTS()
	b := newTestBroker(fake)
	account := testAccount(fake)

	sess, err := b.Assume(context.Background(), account, cloud.ScopeRead)
	require.NoError(t, err)

	cfg, err := FromSession(sess, "eu-west-1")
	require.NoError(t, err)
	assert.Equal(t, "eu-west-1", cfg.Region)
	require.NotNil(t, cfg.Credentials)

	creds, err := cfg.Credentials.Retrieve(context.Background())
	require.NoError(t, err)
	assert.NotEmpty(t, creds.AccessKeyID)
	assert.NotEmpty(t, creds.SecretAccessKey)
	assert.True(t, creds.CanExpire)
}

func TestFromSession_RejectsForeignSession(t *testing.T) {
	_, err := FromSession(foreignSession{}, "us-east-1")
	assert.Error(t, err)
}

// foreignSession implements ports.AWSSession without producing an aws.Config,
// simulating a session issued by a different package (e.g. awssim).
type foreignSession struct{}

func (foreignSession) AccountID() core.AccountID { return "1" }
func (foreignSession) Scope() cloud.RoleScope    { return cloud.ScopeRead }
func (foreignSession) ExpiresAt() time.Time      { return time.Now() }
func (foreignSession) Config(core.Region) any    { return "not an aws.Config" }
