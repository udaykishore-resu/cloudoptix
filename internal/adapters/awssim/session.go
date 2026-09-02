package awssim

import (
	"context"
	"fmt"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// Session implements ports.AWSSession against an in-memory Estate. Its
// Config method returns the *Estate itself (as the `any` the port requires,
// since the interface must stay provider-neutral): every other adapter in
// this package type-asserts it back with FromSession, which is how a
// Discoverer/CostIngestor/MetricCollector/Executor gets at the same estate
// a real one would reach via an aws.Config-backed SDK client.
type Session struct {
	accountID core.AccountID
	scope     cloud.RoleScope
	expiresAt time.Time
	estate    *Estate
}

var _ ports.AWSSession = (*Session)(nil)

// NewSession wraps an estate as an assumed-role session for one scope.
func NewSession(estate *Estate, scope cloud.RoleScope, ttl time.Duration) *Session {
	return &Session{
		accountID: estate.AccountID, scope: scope,
		expiresAt: time.Now().UTC().Add(ttl), estate: estate,
	}
}

// AccountID reports the account the session is bound to.
func (s *Session) AccountID() core.AccountID { return s.accountID }

// Scope reports the permission tier the session was assumed for.
func (s *Session) Scope() cloud.RoleScope { return s.scope }

// ExpiresAt reports when the session's credentials expire.
func (s *Session) ExpiresAt() time.Time { return s.expiresAt }

// Config returns the estate this session is bound to. Region is accepted to
// satisfy the port's per-region SDK-config shape, but the simulator keeps
// one estate per account rather than one per region: the estate's resources
// already carry their own Region field, and a discoverer scoped to a region
// filters on that field rather than needing a region-specific client.
func (s *Session) Config(_ core.Region) any { return s.estate }

// FromSession recovers the Estate a Session (or any ports.AWSSession this
// package produced) is bound to. Every adapter in this package calls this
// rather than holding its own Estate pointer, which is what lets the same
// Discoverer/CostIngestor/MetricCollector/Executor value be reused across
// multiple simulated accounts in a test without carrying account-specific
// state.
func FromSession(session ports.AWSSession, region core.Region) (*Estate, error) {
	if session == nil {
		return nil, fmt.Errorf("awssim: nil session")
	}
	cfg := session.Config(region)
	estate, ok := cfg.(*Estate)
	if !ok {
		return nil, fmt.Errorf("awssim: session config is %T, not *awssim.Estate — this session was not issued by awssim.Broker", cfg)
	}
	return estate, nil
}

// Broker implements ports.AWSCredentialBroker for cloud.AccessSimulated
// accounts. It is the only way anything in this package's callers obtain a
// Session, matching the real port's contract that credentials are never
// handed out directly.
type Broker struct {
	estate  *Estate
	granted map[cloud.RoleScope]bool
	// missing lists the IAM actions Verify reports as absent for a scope
	// that was not granted, so a demo account can realistically show a
	// degraded connection rather than every scope being all-or-nothing.
	missing map[cloud.RoleScope][]string
}

var _ ports.AWSCredentialBroker = (*Broker)(nil)

// NewBroker builds a broker over an estate. grantedScopes lists the scopes
// this simulated account has actually granted CloudOptix — mirroring how a
// real tenant might connect Read+Analyze but decline Execute.
func NewBroker(estate *Estate, grantedScopes ...cloud.RoleScope) *Broker {
	granted := make(map[cloud.RoleScope]bool, len(grantedScopes))
	for _, s := range grantedScopes {
		granted[s] = true
	}
	return &Broker{
		estate:  estate,
		granted: granted,
		missing: map[cloud.RoleScope][]string{
			cloud.ScopeRead:    {"ec2:Describe*", "rds:Describe*", "s3:ListAllMyBuckets", "lambda:ListFunctions"},
			cloud.ScopeAnalyze: {"cloudwatch:GetMetricData", "ce:GetCostAndUsage"},
			cloud.ScopePlan:    {"ec2:DryRun"},
			cloud.ScopeExecute: {"ec2:ModifyInstanceAttribute", "ec2:StopInstances", "rds:ModifyDBInstance"},
		},
	}
}

// Assume returns a Session for the requested scope, or an error if the
// simulated account never granted it — mirroring how a real AssumeRole call
// fails when the role does not exist.
func (b *Broker) Assume(ctx context.Context, account cloud.AWSAccount, scope cloud.RoleScope) (ports.AWSSession, error) {
	if !b.granted[scope] {
		return nil, core.NewError(core.ErrForbidden, "scope_not_granted",
			"simulated account %s has not granted scope %s", account.AccountID, scope)
	}
	return NewSession(b.estate, scope, time.Hour), nil
}

// Verify reports which scopes are granted and, for scopes that are not, a
// representative set of missing IAM actions — the same shape a real
// permission probe against a customer's role would return.
func (b *Broker) Verify(ctx context.Context, account cloud.AWSAccount) (ports.ConnectionCheck, error) {
	check := ports.ConnectionCheck{
		AccountID:             account.AccountID,
		Reachable:             true,
		Regions:               b.estate.Regions,
		IdentityARN:           fmt.Sprintf("arn:aws:iam::%s:role/cloudoptix-simulated", account.AccountID),
		IsPayer:               true,
		CURAvailable:          true,
		CostExplorerAvailable: true,
		CheckedAt:             time.Now().UTC(),
	}
	missing := map[string][]string{}
	for _, scope := range []cloud.RoleScope{cloud.ScopeRead, cloud.ScopeAnalyze, cloud.ScopePlan, cloud.ScopeExecute} {
		if b.granted[scope] {
			check.GrantedScopes = append(check.GrantedScopes, scope)
		} else if actions := b.missing[scope]; len(actions) > 0 {
			missing[string(scope)] = actions
		}
	}
	if len(missing) > 0 {
		check.MissingActions = missing
	}
	return check, nil
}
