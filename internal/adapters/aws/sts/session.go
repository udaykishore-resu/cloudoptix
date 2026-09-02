package sts

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssdksts "github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/aws/awserr"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// stsAPI is the narrow slice of *sts.Client this package depends on. Every
// caller of AssumeRole and Verify's identity check goes through this
// interface rather than the concrete client, which is what lets the whole
// package be tested without reaching AWS: a test supplies a fake satisfying
// these two methods and nothing else.
type stsAPI interface {
	AssumeRole(ctx context.Context, params *awssdksts.AssumeRoleInput, optFns ...func(*awssdksts.Options)) (*awssdksts.AssumeRoleOutput, error)
	GetCallerIdentity(ctx context.Context, params *awssdksts.GetCallerIdentityInput, optFns ...func(*awssdksts.Options)) (*awssdksts.GetCallerIdentityOutput, error)
}

// Session implements ports.AWSSession. It carries the base aws.Config the
// Broker itself was built from (for region resolution, retry policy, HTTP
// client) plus a credentials provider that resolves to the temporary
// credentials this one AssumeRole call minted — never anything static.
type Session struct {
	accountID   core.AccountID
	scope       cloud.RoleScope
	base        aws.Config
	credentials aws.CredentialsProvider
	expiresAt   time.Time
	assumedARN  string
}

var _ ports.AWSSession = (*Session)(nil)

// AccountID reports the account this session is bound to.
func (s *Session) AccountID() core.AccountID { return s.accountID }

// Scope reports the permission tier this session was assumed for.
func (s *Session) Scope() cloud.RoleScope { return s.scope }

// ExpiresAt reports when the underlying temporary credentials expire. A
// caller should treat this as advisory rather than authoritative for signing
// purposes — the credentials provider itself refreshes ahead of this instant
// — but it is what the Broker's own cache uses to decide when a cached
// Session is due for proactive replacement.
func (s *Session) ExpiresAt() time.Time { return s.expiresAt }

// Config returns an aws.Config scoped to one region, built by copying the
// Broker's base configuration (which carries the retryer, HTTP client and
// other cross-cutting settings CloudOptix's own runtime configured) and
// overriding only Region and Credentials. Every AWS SDK v2 service client in
// the other aws/* packages is constructed with NewFromConfig against exactly
// this value.
func (s *Session) Config(region core.Region) any {
	cfg := s.base.Copy()
	cfg.Region = string(region)
	cfg.Credentials = s.credentials
	return cfg
}

// FromSession recovers the aws.Config a ports.AWSSession produced by this
// package carries for one region. Every discovery, costing, metrics and
// executor adapter calls this rather than assuming the session is a
// *sts.Session, so a test can substitute any ports.AWSSession whose
// Config(region) happens to return an aws.Config.
func FromSession(session ports.AWSSession, region core.Region) (aws.Config, error) {
	if session == nil {
		return aws.Config{}, core.Invalid("aws: nil session")
	}
	cfg, ok := session.Config(region).(aws.Config)
	if !ok {
		return aws.Config{}, core.Invalid(
			"aws: session config is %T, not aws.Config — this session was not issued by the aws/sts broker", session.Config(region))
	}
	return cfg, nil
}

// assumeRoleCredentials builds an aws.CredentialsProvider that mints fresh
// temporary credentials by calling AssumeRole against parent, wrapped in
// aws.NewCredentialsCache so that (a) repeated signing of requests within the
// credential's lifetime reuses the same temporary credentials rather than
// calling AssumeRole per request, (b) the cache's own internal singleflight
// coalesces concurrent Retrieve calls the same way the Broker's per-key
// cache coalesces concurrent Assume calls, and (c) refresh happens
// refreshWindow before actual expiry so a request being signed never
// observes credentials AWS is about to reject as expired.
//
// This is the only function in the package that produces aws.Credentials,
// and every field it sets comes from an AssumeRoleOutput — there is no path
// through this function, or any other in the package, that accepts an
// access key and secret as literal input.
func assumeRoleCredentials(parent stsAPI, roleARN core.ARN, externalID, sessionName string,
	duration, refreshWindow time.Duration) aws.CredentialsProvider {

	provider := aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) {
		out, err := parent.AssumeRole(ctx, &awssdksts.AssumeRoleInput{
			RoleArn:         aws.String(string(roleARN)),
			RoleSessionName: aws.String(sessionName),
			ExternalId:      aws.String(externalID),
			DurationSeconds: aws.Int32(int32(duration.Seconds())),
		})
		if err != nil {
			return aws.Credentials{}, awserr.Translate(err, "sts", "AssumeRole", "sts:AssumeRole")
		}
		c := out.Credentials
		if c == nil {
			return aws.Credentials{}, core.NewError(core.ErrUnavailable, "sts_empty_credentials",
				"sts:AssumeRole for role %s returned no credentials", roleARN)
		}
		return aws.Credentials{
			AccessKeyID:     aws.ToString(c.AccessKeyId),
			SecretAccessKey: aws.ToString(c.SecretAccessKey),
			SessionToken:    aws.ToString(c.SessionToken),
			CanExpire:       true,
			Expires:         aws.ToTime(c.Expiration),
			Source:          "cloudoptix.aws.sts.AssumeRole",
		}, nil
	})

	return aws.NewCredentialsCache(provider, func(o *aws.CredentialsCacheOptions) {
		o.ExpiryWindow = refreshWindow
		o.ExpiryWindowJitterFrac = 0.2
	})
}
