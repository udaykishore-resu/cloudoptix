package discovery

import (
	"context"
	"sync"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

func ctxFor(tenant core.TenantID) context.Context {
	return core.WithPrincipal(context.Background(), core.SystemPrincipal(tenant, "test"))
}

// fakeSession is the minimal ports.AWSSession a test needs: an identity and
// nothing else, since no fake discoverer in this file reads Config.
type fakeSession struct {
	accountID core.AccountID
	scope     cloud.RoleScope
}

func (f fakeSession) AccountID() core.AccountID { return f.accountID }
func (f fakeSession) Scope() cloud.RoleScope    { return f.scope }
func (f fakeSession) ExpiresAt() time.Time      { return time.Now().Add(time.Hour) }
func (f fakeSession) Config(core.Region) any    { return nil }

// fakeBroker hands out fakeSessions, optionally failing Assume for named
// accounts to exercise per-account failure isolation.
type fakeBroker struct {
	failFor map[core.AccountID]error
}

func (b *fakeBroker) Assume(_ context.Context, account cloud.AWSAccount, scope cloud.RoleScope) (ports.AWSSession, error) {
	if b.failFor != nil {
		if err, ok := b.failFor[account.AccountID]; ok {
			return nil, err
		}
	}
	return fakeSession{accountID: account.AccountID, scope: scope}, nil
}

func (b *fakeBroker) Verify(context.Context, cloud.AWSAccount) (ports.ConnectionCheck, error) {
	return ports.ConnectionCheck{}, nil
}

// fakeDiscoverer is a configurable ports.ResourceDiscoverer: it returns
// canned resources/relationships per region, can be told to fail with a
// retryable error for the first N attempts against a region before
// succeeding (to exercise the retry loop), or to fail permanently with a
// fixed error (to exercise permission-denial and non-retryable handling).
type fakeDiscoverer struct {
	service string
	kinds   []cloud.Kind
	actions []string

	byRegion map[core.Region][]cloud.Resource
	rels     map[core.Region][]cloud.Relationship

	// failUntilAttempt, keyed by region, is the number of leading attempts
	// that fail with a retryable throttle error before Discover starts
	// succeeding for that region.
	failUntilAttempt map[core.Region]int
	// permanentErr, keyed by region, is returned on every attempt for that
	// region, overriding failUntilAttempt.
	permanentErr map[core.Region]error

	mu       sync.Mutex
	attempts map[core.Region]int
}

func (d *fakeDiscoverer) Service() string           { return d.service }
func (d *fakeDiscoverer) Kinds() []cloud.Kind       { return d.kinds }
func (d *fakeDiscoverer) RequiredActions() []string { return d.actions }

func (d *fakeDiscoverer) callCount(region core.Region) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.attempts == nil {
		return 0
	}
	return d.attempts[region]
}

func (d *fakeDiscoverer) Discover(_ context.Context, in ports.DiscoveryInput) (ports.DiscoveryOutput, error) {
	d.mu.Lock()
	if d.attempts == nil {
		d.attempts = map[core.Region]int{}
	}
	d.attempts[in.Region]++
	n := d.attempts[in.Region]
	if d.permanentErr != nil {
		if err, ok := d.permanentErr[in.Region]; ok {
			d.mu.Unlock()
			return ports.DiscoveryOutput{}, err
		}
	}
	threshold := d.failUntilAttempt[in.Region]
	d.mu.Unlock()

	if n <= threshold {
		return ports.DiscoveryOutput{Throttled: 1},
			core.NewError(core.ErrThrottled, "throttled", "simulated throttle on attempt %d", n)
	}
	return ports.DiscoveryOutput{
		Resources: d.byRegion[in.Region], Relationships: d.rels[in.Region], APICalls: 1,
	}, nil
}

// mkAccount builds a connected, read-scoped AWSAccount fixture.
func mkAccount(tenant core.TenantID, accountID core.AccountID, env core.Environment, regions ...core.Region) cloud.AWSAccount {
	return cloud.AWSAccount{
		ID: core.NewID("acc"), TenantID: tenant, AccountID: accountID, Environment: env, Regions: regions,
		AccessMode: cloud.AccessAssumeRole, State: cloud.ConnConnected,
		GrantedScopes: []cloud.RoleScope{cloud.ScopeRead},
		RoleARNs:      map[cloud.RoleScope]core.ARN{cloud.ScopeRead: "arn:aws:iam::" + core.ARN(accountID) + ":role/cloudoptix-read"},
		ExternalID:    "ext-1", CreatedAt: time.Now(),
	}
}

func mkResource(tenant core.TenantID, kind cloud.Kind, native string, tags core.Tags) cloud.Resource {
	return cloud.Resource{
		TenantID: tenant, Kind: kind, NativeID: native, Name: native, State: cloud.StateRunning, Tags: tags,
	}
}
