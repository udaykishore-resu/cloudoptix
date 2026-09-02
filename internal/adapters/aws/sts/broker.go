package sts

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	awssdksts "github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/aws/awserr"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// DefaultSessionDuration is how long a minted session's credentials are
// valid for. It sits well inside the one-hour ceiling AWS imposes on a
// chained AssumeRole call (see OrgChain), so the same duration works whether
// or not a given account requires chaining.
const DefaultSessionDuration = 55 * time.Minute

// DefaultRefreshWindow is how long before actual expiry a session is treated
// as due for renewal, both by the Broker's own per-key cache and by the
// aws.CredentialsCache wrapping each session's credentials provider.
const DefaultRefreshWindow = 5 * time.Minute

// OrgChain configures role chaining through an AWS Organizations management
// account. Some customers grant CloudOptix one role in their management
// account that is, in turn, trusted by a role in every member account
// (mirroring how OrganizationAccountAccessRole-style delegation works) rather
// than granting a role directly in every account individually. When set,
// Broker.Assume first assumes ManagementRoleARN using its own base identity,
// then assumes the target account's role using the management session's
// identity rather than its own — which is what lets the member account's
// trust policy name only the management account, not CloudOptix directly.
type OrgChain struct {
	ManagementRoleARN core.ARN
	// ExternalID guards the management-role hop the same way every
	// account's own ExternalID guards the final hop.
	ExternalID string
}

// Broker implements ports.AWSCredentialBroker over sts:AssumeRole.
//
// See the package doc comment for the structural guarantee this type
// enforces: nothing here accepts a static access key.
type Broker struct {
	base      aws.Config
	rootSTS   stsAPI
	newSTS    func(cfg aws.Config) stsAPI
	principal string

	sessionDuration time.Duration
	refreshWindow   time.Duration
	orgChain        *OrgChain
	clock           func() time.Time
	probes          probeSet

	mu    sync.Mutex
	cache map[cacheKey]*cacheEntry
}

var _ ports.AWSCredentialBroker = (*Broker)(nil)

type cacheKey struct {
	account core.AccountID
	scope   cloud.RoleScope
}

// cacheEntry holds the last session minted for one (account, scope) plus a
// dedicated mutex. Refresh runs with that mutex held, which is the whole
// coalescing mechanism: a second goroutine that discovers the same stale
// entry blocks on Lock rather than racing a second AssumeRole call, and by
// the time it acquires the lock the first goroutine has already refreshed —
// so its own post-lock freshness check succeeds and it returns the same
// session the first goroutine just minted, without ever touching STS itself.
type cacheEntry struct {
	mu      sync.Mutex
	session *Session
}

// Option configures a Broker at construction.
type Option func(*Broker)

// WithPrincipal sets the CloudOptix principal identifier embedded in every
// role session name (e.g. the worker's service name and instance, or the
// human operator's id for an interactive session), so the customer's
// CloudTrail can attribute an action to more than just "CloudOptix".
func WithPrincipal(principal string) Option {
	return func(b *Broker) { b.principal = principal }
}

// WithSessionDuration overrides DefaultSessionDuration.
func WithSessionDuration(d time.Duration) Option {
	return func(b *Broker) { b.sessionDuration = d }
}

// WithRefreshWindow overrides DefaultRefreshWindow.
func WithRefreshWindow(d time.Duration) Option {
	return func(b *Broker) { b.refreshWindow = d }
}

// WithOrgChain enables role chaining through a management-account role for
// organization-wide access.
func WithOrgChain(chain OrgChain) Option {
	return func(b *Broker) { b.orgChain = &chain }
}

// WithClock overrides the time source, for deterministic tests of cache
// expiry and refresh behaviour.
func WithClock(now func() time.Time) Option {
	return func(b *Broker) { b.clock = now }
}

// withSTSClientFactory overrides how the Broker builds an stsAPI from an
// aws.Config. It is unexported: production callers always get the real
// *sts.Client, and only this package's own tests need to substitute a fake,
// via newTestBroker in the test file.
func withSTSClientFactory(f func(aws.Config) stsAPI) Option {
	return func(b *Broker) { b.newSTS = f }
}

// LoadBaseConfig resolves CloudOptix's own control-plane identity through the
// AWS SDK's standard default credential chain (environment, an ECS task
// role, an EC2 instance profile, or a local profile in development) — the
// only way this package obtains any starting identity at all. Nothing about
// that chain is influenced by, or influences, the customer accounts this
// identity goes on to assume roles into.
func LoadBaseConfig(ctx context.Context) (aws.Config, error) {
	return config.LoadDefaultConfig(ctx)
}

// NewBroker builds a Broker over base, CloudOptix's own control-plane
// identity (see LoadBaseConfig). base.Credentials is never read for its
// literal value by anything in this package beyond what the SDK itself does
// to sign the AssumeRole calls this package issues.
func NewBroker(base aws.Config, opts ...Option) *Broker {
	b := &Broker{
		base:            base,
		principal:       "cloudoptix",
		sessionDuration: DefaultSessionDuration,
		refreshWindow:   DefaultRefreshWindow,
		clock:           func() time.Time { return time.Now().UTC() },
		cache:           map[cacheKey]*cacheEntry{},
	}
	b.newSTS = func(cfg aws.Config) stsAPI { return awssdksts.NewFromConfig(cfg) }
	for _, o := range opts {
		o(b)
	}
	b.rootSTS = b.newSTS(b.base)
	b.probes = defaultProbes()
	return b
}

// Assume returns a cached or freshly minted Session for one (account, scope)
// pair, chaining through the configured management role first when org-wide
// access is enabled.
func (b *Broker) Assume(ctx context.Context, account cloud.AWSAccount, scope cloud.RoleScope) (ports.AWSSession, error) {
	roleARN := account.RoleARNs[scope]
	if roleARN == "" {
		return nil, core.Forbidden("account %s has not granted scope %s: no role ARN on file", account.AccountID, scope)
	}
	if account.ExternalID == "" {
		return nil, core.Invalid("account %s has no external id configured; refusing to assume a role without the confused-deputy guard", account.AccountID)
	}

	entry := b.entryFor(account.AccountID, scope)
	entry.mu.Lock()
	defer entry.mu.Unlock()

	now := b.clock()
	if entry.session != nil && now.Before(entry.session.expiresAt.Add(-b.refreshWindow)) {
		return entry.session, nil
	}

	parent := b.rootSTS
	if b.orgChain != nil {
		mgmt, err := b.assumeOnce(ctx, b.rootSTS, b.orgChain.ManagementRoleARN, b.orgChain.ExternalID,
			b.sessionName("mgmt", scope))
		if err != nil {
			return nil, fmt.Errorf("aws/sts: assuming management role for org-wide access: %w", err)
		}
		parent = b.newSTS(mgmt.cfg)
	}

	sessionName := b.sessionName(string(account.AccountID), scope)
	result, err := b.assumeOnce(ctx, parent, roleARN, account.ExternalID, sessionName)
	if err != nil {
		return nil, fmt.Errorf("aws/sts: assuming %s role for account %s: %w", scope, account.AccountID, err)
	}

	// assumeOnce already resolved the credentials once to discover the
	// expiry; rebuild a caching provider from the same parent/role so the
	// Session's own Config() calls refresh independently rather than reusing
	// a single already-issued credential set past its window.
	sess := &Session{
		accountID: account.AccountID,
		scope:     scope,
		base:      b.base,
		credentials: assumeRoleCredentials(parent, roleARN, account.ExternalID, sessionName,
			b.sessionDuration, b.refreshWindow),
		expiresAt: result.expiresAt,
	}
	entry.session = sess
	return sess, nil
}

// entryFor returns the cache entry for a key, creating it under the map lock
// if this is the first request for it. The map lock is held only long enough
// to look up or insert the entry pointer — the potentially slow AssumeRole
// call happens after this returns, under the entry's own mutex, so two
// requests for different accounts never block each other.
func (b *Broker) entryFor(account core.AccountID, scope cloud.RoleScope) *cacheEntry {
	key := cacheKey{account: account, scope: scope}
	b.mu.Lock()
	defer b.mu.Unlock()
	e, ok := b.cache[key]
	if !ok {
		e = &cacheEntry{}
		b.cache[key] = e
	}
	return e
}

// assumeResult is the minimal outcome assumeOnce needs to report: the
// resulting aws.Config for a subsequent chained AssumeRole, and the moment
// the minted credentials expire.
type assumeResult struct {
	cfg       aws.Config
	expiresAt time.Time
}

// assumeOnce performs exactly one sts:AssumeRole call and packages the
// result as an aws.Config a further stsAPI client can be built from — used
// both for the (optional) management-role hop and, in that hop's absence,
// directly for the target account's role.
func (b *Broker) assumeOnce(ctx context.Context, parent stsAPI, roleARN core.ARN, externalID, sessionName string) (assumeResult, error) {
	out, err := parent.AssumeRole(ctx, &awssdksts.AssumeRoleInput{
		RoleArn:         aws.String(string(roleARN)),
		RoleSessionName: aws.String(sessionName),
		ExternalId:      aws.String(externalID),
		DurationSeconds: aws.Int32(int32(b.sessionDuration.Seconds())),
	})
	if err != nil {
		return assumeResult{}, awserr.Translate(err, "sts", "AssumeRole", "sts:AssumeRole")
	}
	if out.Credentials == nil {
		return assumeResult{}, core.NewError(core.ErrUnavailable, "sts_empty_credentials",
			"sts:AssumeRole for role %s returned no credentials", roleARN)
	}
	c := out.Credentials
	cfg := b.base.Copy()
	cfg.Credentials = aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{
			AccessKeyID: aws.ToString(c.AccessKeyId), SecretAccessKey: aws.ToString(c.SecretAccessKey),
			SessionToken: aws.ToString(c.SessionToken), CanExpire: true, Expires: aws.ToTime(c.Expiration),
			Source: "cloudoptix.aws.sts.AssumeRole",
		}, nil
	})
	return assumeResult{cfg: cfg, expiresAt: aws.ToTime(c.Expiration)}, nil
}

// sessionNameCharset matches what STS RoleSessionName accepts:
// alphanumerics plus +=,.@-.
var sessionNameCharset = regexp.MustCompile(`[^A-Za-z0-9+=,.@-]`)

// sessionName builds a RoleSessionName that identifies both CloudOptix and
// the requesting principal, so the customer's CloudTrail attributes every
// action to a specific CloudOptix session rather than an anonymous
// AssumeRole call. STS caps this at 64 characters.
func (b *Broker) sessionName(target string, scope cloud.RoleScope) string {
	raw := fmt.Sprintf("cloudoptix-%s-%s-%s", sanitize(b.principal), sanitize(target), scope)
	if len(raw) > 64 {
		raw = raw[:64]
	}
	return raw
}

func sanitize(s string) string {
	return sessionNameCharset.ReplaceAllString(strings.TrimSpace(s), "")
}
