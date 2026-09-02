package sts

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	cetypes "github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	awssdksts "github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/aws/awserr"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// The four scopes Verify walks, in the fixed order they are reported.
var verifyScopes = []cloud.RoleScope{cloud.ScopeRead, cloud.ScopeAnalyze, cloud.ScopePlan, cloud.ScopeExecute}

// Narrow client interfaces for the harmless probe calls Verify runs. Kept
// separate from stsAPI in session.go because Verify's probes span several
// AWS services, none of which Assume itself needs.
type (
	ec2ProbeAPI interface {
		DescribeInstances(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
		StopInstances(ctx context.Context, params *ec2.StopInstancesInput, optFns ...func(*ec2.Options)) (*ec2.StopInstancesOutput, error)
	}
	costExplorerProbeAPI interface {
		GetCostAndUsage(ctx context.Context, params *costexplorer.GetCostAndUsageInput, optFns ...func(*costexplorer.Options)) (*costexplorer.GetCostAndUsageOutput, error)
	}
	organizationsProbeAPI interface {
		DescribeOrganization(ctx context.Context, params *organizations.DescribeOrganizationInput, optFns ...func(*organizations.Options)) (*organizations.DescribeOrganizationOutput, error)
	}
	s3ProbeAPI interface {
		HeadBucket(ctx context.Context, params *s3.HeadBucketInput, optFns ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
	}
)

// probeSet is the set of client factories Verify's probes run against. It is
// a struct of functions rather than a struct of clients because each probe
// needs a client built from a *different* region's aws.Config (the one
// belonging to the scope being probed), not one fixed client.
type probeSet struct {
	ec2 func(aws.Config) ec2ProbeAPI
	ce  func(aws.Config) costExplorerProbeAPI
	org func(aws.Config) organizationsProbeAPI
	s3  func(aws.Config) s3ProbeAPI
}

func defaultProbes() probeSet {
	return probeSet{
		ec2: func(cfg aws.Config) ec2ProbeAPI { return ec2.NewFromConfig(cfg) },
		ce:  func(cfg aws.Config) costExplorerProbeAPI { return costexplorer.NewFromConfig(cfg) },
		org: func(cfg aws.Config) organizationsProbeAPI { return organizations.NewFromConfig(cfg) },
		s3:  func(cfg aws.Config) s3ProbeAPI { return s3.NewFromConfig(cfg) },
	}
}

// placeholderInstanceID is used only as the target of a DryRun StopInstances
// call. AWS evaluates IAM authorization for a dry-run request before it
// checks whether the target exists, so this never needs to name a real
// instance — it exists purely to give the API call a syntactically valid
// argument.
const placeholderInstanceID = "i-000000000000000ff"

// Verify probes account's roles scope by scope: assume the role, confirm the
// resulting identity with sts:GetCallerIdentity, then run one harmless,
// dry-run-where-possible probe call representative of that scope's real
// workload. A probe that comes back AccessDenied contributes the exact
// denied action to ConnectionCheck.MissingActions; a probe that comes back
// DryRunOperation (the success signal AWS's own dry-run convention uses) or
// with no error at all means that scope's permission is present.
func (b *Broker) Verify(ctx context.Context, account cloud.AWSAccount) (ports.ConnectionCheck, error) {
	check := ports.ConnectionCheck{
		AccountID: account.AccountID,
		Regions:   account.Regions,
		CheckedAt: b.clock(),
	}
	region := core.Region("us-east-1")
	if len(account.Regions) > 0 {
		region = account.Regions[0]
	}

	var analyzeCfg aws.Config
	haveAnalyze := false
	analyzeProbeOK := false

	for _, scope := range verifyScopes {
		if account.RoleARNs[scope] == "" {
			continue
		}
		sess, err := b.Assume(ctx, account, scope)
		if err != nil {
			check.Errors = append(check.Errors, fmt.Sprintf("%s: assume role failed: %v", scope, err))
			continue
		}
		cfg, err := FromSession(sess, region)
		if err != nil {
			check.Errors = append(check.Errors, fmt.Sprintf("%s: %v", scope, err))
			continue
		}
		scopeCtx, cancel := context.WithTimeout(ctx, verifyTimeout)
		scopedSTS := b.newSTS(cfg)
		identOut, err := scopedSTS.GetCallerIdentity(scopeCtx, &awssdksts.GetCallerIdentityInput{})
		cancel()
		if err != nil {
			check.Errors = append(check.Errors, fmt.Sprintf("%s: sts:GetCallerIdentity failed: %v", scope, err))
			continue
		}
		check.Reachable = true
		check.GrantedScopes = append(check.GrantedScopes, scope)
		if check.IdentityARN == "" {
			check.IdentityARN = aws.ToString(identOut.Arn)
		}

		probeCtx, probeCancel := context.WithTimeout(ctx, verifyTimeout)
		missing, probeErr := b.probeScope(probeCtx, cfg, scope)
		probeCancel()
		if len(missing) > 0 {
			if check.MissingActions == nil {
				check.MissingActions = map[string][]string{}
			}
			check.MissingActions[string(scope)] = missing
		}
		if scope == cloud.ScopeAnalyze {
			analyzeCfg, haveAnalyze = cfg, true
			analyzeProbeOK = probeErr == nil
		}
	}

	if haveAnalyze {
		check.CostExplorerAvailable = analyzeProbeOK
		b.checkPayerAndCUR(ctx, analyzeCfg, account, &check)
	}

	return check, nil
}

// probeScope runs the one representative call for a scope and reports which
// IAM action, if any, was denied. probeErr is the raw error the call
// returned (nil on success, including the DryRunOperation "success" signal),
// which callers use to decide whether the underlying capability actually
// works — a denied action list can be empty while probeErr is still non-nil
// for reasons unrelated to permissions (e.g. a malformed placeholder was
// rejected before the authorization check ran).
func (b *Broker) probeScope(ctx context.Context, cfg aws.Config, scope cloud.RoleScope) (missing []string, probeErr error) {
	switch scope {
	case cloud.ScopeRead:
		_, err := b.probes.ec2(cfg).DescribeInstances(ctx, &ec2.DescribeInstancesInput{MaxResults: aws.Int32(5)})
		return deniedActions(err, "ec2:DescribeInstances"), err

	case cloud.ScopeAnalyze:
		now := b.clock()
		_, err := b.probes.ce(cfg).GetCostAndUsage(ctx, &costexplorer.GetCostAndUsageInput{
			Granularity: cetypes.GranularityDaily,
			Metrics:     []string{"UnblendedCost"},
			TimePeriod: &cetypes.DateInterval{
				Start: aws.String(now.AddDate(0, 0, -1).Format("2006-01-02")),
				End:   aws.String(now.Format("2006-01-02")),
			},
		})
		// Cost Explorer bills a small fixed amount per API call regardless of
		// the query; this probe therefore runs once per Verify call, not on
		// a poll loop, and asks for the narrowest possible window.
		return deniedActions(err, "ce:GetCostAndUsage"), err

	case cloud.ScopePlan:
		_, err := b.probes.ec2(cfg).DescribeInstances(ctx, &ec2.DescribeInstancesInput{
			DryRun: aws.Bool(true), MaxResults: aws.Int32(5),
		})
		return deniedActions(dryRunSuccessAsNil(err), "ec2:DescribeInstances"), dryRunSuccessAsNil(err)

	case cloud.ScopeExecute:
		_, err := b.probes.ec2(cfg).StopInstances(ctx, &ec2.StopInstancesInput{
			DryRun: aws.Bool(true), InstanceIds: []string{placeholderInstanceID},
		})
		return deniedActions(dryRunSuccessAsNil(err), "ec2:StopInstances"), dryRunSuccessAsNil(err)

	default:
		return nil, nil
	}
}

// dryRunSuccessAsNil converts the DryRunOperation pseudo-error AWS returns
// for an authorized dry-run call into a nil error, so probeScope's shared
// deniedActions logic and its probeErr result both see "this succeeded"
// rather than treating AWS's own success signal as a failure.
func dryRunSuccessAsNil(err error) error {
	if ae, ok := awserr.APIErrorOf(err); ok && ae.ErrorCode() == "DryRunOperation" {
		return nil
	}
	return err
}

// deniedActions reports the single denied action for a probe error, or nil
// when the probe succeeded or failed for a reason other than authorization.
func deniedActions(err error, fallbackAction string) []string {
	if err == nil || !awserr.AccessDenied(err) {
		return nil
	}
	return []string{awserr.DeniedAction(err, fallbackAction)}
}

// checkPayerAndCUR fills IsPayer and CURAvailable using the Analyze-scoped
// session, since both organizations:DescribeOrganization and reading the CUR
// bucket are billing-adjacent checks that belong with Cost Explorer rather
// than with any of the other three scopes.
func (b *Broker) checkPayerAndCUR(ctx context.Context, cfg aws.Config, account cloud.AWSAccount, check *ports.ConnectionCheck) {
	if org, err := b.probes.org(cfg).DescribeOrganization(ctx, &organizations.DescribeOrganizationInput{}); err == nil && org.Organization != nil {
		check.IsPayer = aws.ToString(org.Organization.MasterAccountId) == string(account.AccountID)
	}
	if account.CURBucket == "" {
		return
	}
	if _, err := b.probes.s3(cfg).HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(account.CURBucket)}); err == nil {
		check.CURAvailable = true
	}
}

// verifyTimeout bounds how long a single scope's probe is allowed to take,
// so one hung call cannot stall the whole Verify pass.
const verifyTimeout = 20 * time.Second
