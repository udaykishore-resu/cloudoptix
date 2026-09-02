// This file implements apply_s3_lifecycle and abort_multipart_uploads.
//
// apply_s3_lifecycle manages exactly one rule within a bucket's lifecycle
// configuration, identified by params["rule_id"] (default
// "cloudoptix-lifecycle-policy"), because PutBucketLifecycleConfiguration
// replaces the *entire* configuration — every mutate and restore call here
// round-trips the bucket's full existing rule list (captured as JSON in
// current["rules_json"]/before["rules_json"]) so that a tenant's own,
// unrelated lifecycle rules are never clobbered by this action managing its
// one rule. The parameter contract deliberately covers the common
// recommendation shape (transition to a cheaper storage class after N days,
// expire after N days, abort stale multipart uploads after N days) rather
// than accepting an arbitrary rule document, in exchange for a much simpler
// and more auditable Parameters shape on the approval screen.
package executor

import (
	"context"
	"encoding/json"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/aws/awserr"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
)

type s3API interface {
	GetBucketLifecycleConfiguration(ctx context.Context, in *s3.GetBucketLifecycleConfigurationInput, optFns ...func(*s3.Options)) (*s3.GetBucketLifecycleConfigurationOutput, error)
	PutBucketLifecycleConfiguration(ctx context.Context, in *s3.PutBucketLifecycleConfigurationInput, optFns ...func(*s3.Options)) (*s3.PutBucketLifecycleConfigurationOutput, error)
	DeleteBucketLifecycle(ctx context.Context, in *s3.DeleteBucketLifecycleInput, optFns ...func(*s3.Options)) (*s3.DeleteBucketLifecycleOutput, error)
	ListMultipartUploads(ctx context.Context, in *s3.ListMultipartUploadsInput, optFns ...func(*s3.Options)) (*s3.ListMultipartUploadsOutput, error)
	AbortMultipartUpload(ctx context.Context, in *s3.AbortMultipartUploadInput, optFns ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error)
}

func newS3Client(cfg any) s3API { return s3.NewFromConfig(cfg.(aws.Config)) }

func isS3NoSuchBucket(err error) bool {
	apiErr, ok := awserr.APIErrorOf(err)
	return ok && apiErr.ErrorCode() == "NoSuchBucket"
}

func isS3NoSuchLifecycleConfig(err error) bool {
	apiErr, ok := awserr.APIErrorOf(err)
	return ok && apiErr.ErrorCode() == "NoSuchLifecycleConfiguration"
}

const defaultLifecycleRuleID = "cloudoptix-lifecycle-policy"

// ---- apply_s3_lifecycle -----------------------------------------------

func captureLifecycle(ctx context.Context, c s3API, bucket string, _ map[string]any, _ core.Region) (map[string]any, bool, error) {
	out, err := c.GetBucketLifecycleConfiguration(ctx, &s3.GetBucketLifecycleConfigurationInput{Bucket: aws.String(bucket)})
	if err != nil {
		if isS3NoSuchBucket(err) {
			return nil, false, nil
		}
		if isS3NoSuchLifecycleConfig(err) {
			return map[string]any{"rules_json": "[]"}, true, nil
		}
		return nil, false, awserr.Translate(err, "s3", "GetBucketLifecycleConfiguration", "s3:GetLifecycleConfiguration")
	}
	b, err := json.Marshal(out.Rules)
	if err != nil {
		return nil, false, core.Invalid("apply_s3_lifecycle: could not encode existing rules for %s: %v", bucket, err)
	}
	return map[string]any{"rules_json": string(b)}, true, nil
}

func lifecycleRuleID(params map[string]any) string {
	if id, ok := paramStr(params, "rule_id"); ok && id != "" {
		return id
	}
	return defaultLifecycleRuleID
}

func decodeLifecycleRules(rulesJSON string) []s3types.LifecycleRule {
	var rules []s3types.LifecycleRule
	if rulesJSON == "" {
		return nil
	}
	_ = json.Unmarshal([]byte(rulesJSON), &rules) // malformed json -> empty rule set, never a hard failure here
	return rules
}

func buildManagedLifecycleRule(params map[string]any) s3types.LifecycleRule {
	prefix, _ := paramStr(params, "prefix")
	rule := s3types.LifecycleRule{
		ID: aws.String(lifecycleRuleID(params)), Status: s3types.ExpirationStatusEnabled,
		Filter: &s3types.LifecycleRuleFilter{Prefix: aws.String(prefix)},
	}
	// The presence of transition_days, not a positive value, is what requests
	// a transition. Zero is a meaningful and common day count: a transition
	// into INTELLIGENT_TIERING is meant to happen immediately, because that
	// class does its own access monitoring and a delay only means paying
	// Standard rates while waiting for the tier that decides tiers. (The
	// 30-day minimum that does exist applies to STANDARD_IA and ONEZONE_IA,
	// and is enforced by S3 itself, which rejects a shorter one.)
	if days, ok := paramInt(params, "transition_days"); ok && days >= 0 {
		class, _ := paramStr(params, "transition_storage_class")
		if class == "" {
			class = string(s3types.TransitionStorageClassGlacier)
		}
		rule.Transitions = []s3types.Transition{{Days: aws.Int32(int32(days)), StorageClass: s3types.TransitionStorageClass(class)}}
	}
	if days, ok := paramInt(params, "expiration_days"); ok && days > 0 {
		rule.Expiration = &s3types.LifecycleExpiration{Days: aws.Int32(int32(days))}
	}
	// Non-current version expiry is a distinct lifecycle clause from
	// Expiration: Expiration deletes the current object, NoncurrentVersion-
	// Expiration deletes the history a versioned bucket keeps behind it. They
	// are not interchangeable, which is why the s3-noncurrent-versions rule
	// needs its own key here rather than being folded into expiration_days —
	// pointing that rule at expiration_days would have deleted customers'
	// live objects.
	if days, ok := paramInt(params, "noncurrent_expiration_days"); ok && days > 0 {
		rule.NoncurrentVersionExpiration = &s3types.NoncurrentVersionExpiration{
			NoncurrentDays: aws.Int32(int32(days)),
		}
	}
	if days, ok := paramInt(params, "abort_incomplete_multipart_days"); ok && days > 0 {
		rule.AbortIncompleteMultipartUpload = &s3types.AbortIncompleteMultipartUpload{DaysAfterInitiation: aws.Int32(int32(days))}
	}
	return rule
}

func upsertRule(rules []s3types.LifecycleRule, rule s3types.LifecycleRule) []s3types.LifecycleRule {
	for i, r := range rules {
		if aws.ToString(r.ID) == aws.ToString(rule.ID) {
			rules[i] = rule
			return rules
		}
	}
	return append(rules, rule)
}

func findRule(rules []s3types.LifecycleRule, id string) (s3types.LifecycleRule, bool) {
	for _, r := range rules {
		if aws.ToString(r.ID) == id {
			return r, true
		}
	}
	return s3types.LifecycleRule{}, false
}

func rulesEqual(a, b s3types.LifecycleRule) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

var applyS3LifecycleSpec = spec[s3API]{
	action: optimize.ActionApplyS3Lifecycle, kind: cloud.KindS3Bucket,
	awsAction: "s3:PutBucketLifecycleConfiguration", titleFmt: "apply a lifecycle policy to bucket %s",
	requiredActions:  []string{"s3:GetLifecycleConfiguration", "s3:PutLifecycleConfiguration"},
	rollbackFeasible: true, dataLossRisk: core.RiskLow,
	captureState: captureLifecycle,
	isApplied: func(current, params map[string]any) bool {
		rulesJSON, _ := current["rules_json"].(string)
		existing, ok := findRule(decodeLifecycleRules(rulesJSON), lifecycleRuleID(params))
		if !ok {
			return false
		}
		return rulesEqual(existing, buildManagedLifecycleRule(params))
	},
	mutate: func(ctx context.Context, c s3API, bucket string, params map[string]any, _ core.Region) (map[string]any, error) {
		current, exists, err := captureLifecycle(ctx, c, bucket, nil, "")
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, core.NotFound(string(cloud.KindS3Bucket), bucket)
		}
		rulesJSON, _ := current["rules_json"].(string)
		merged := upsertRule(decodeLifecycleRules(rulesJSON), buildManagedLifecycleRule(params))
		_, err = c.PutBucketLifecycleConfiguration(ctx, &s3.PutBucketLifecycleConfigurationInput{
			Bucket: aws.String(bucket), LifecycleConfiguration: &s3types.BucketLifecycleConfiguration{Rules: merged},
		})
		if err != nil {
			return nil, awserr.Translate(err, "s3", "PutBucketLifecycleConfiguration", "s3:PutLifecycleConfiguration")
		}
		b, _ := json.Marshal(merged)
		return map[string]any{"rules_json": string(b)}, nil
	},
	restore: func(ctx context.Context, c s3API, bucket string, before map[string]any, _ core.Region) error {
		rulesJSON, _ := before["rules_json"].(string)
		rules := decodeLifecycleRules(rulesJSON)
		if len(rules) == 0 {
			_, err := c.DeleteBucketLifecycle(ctx, &s3.DeleteBucketLifecycleInput{Bucket: aws.String(bucket)})
			if err != nil {
				return awserr.Translate(err, "s3", "DeleteBucketLifecycle", "s3:PutLifecycleConfiguration")
			}
			return nil
		}
		_, err := c.PutBucketLifecycleConfiguration(ctx, &s3.PutBucketLifecycleConfigurationInput{
			Bucket: aws.String(bucket), LifecycleConfiguration: &s3types.BucketLifecycleConfiguration{Rules: rules},
		})
		if err != nil {
			return awserr.Translate(err, "s3", "PutBucketLifecycleConfiguration", "s3:PutLifecycleConfiguration")
		}
		return nil
	},
}

// ---- abort_multipart_uploads -------------------------------------------

// listStaleUploads returns the bucket's in-progress multipart uploads that
// are older than olderThanDays, which is the only set this action is ever
// allowed to abort.
//
// The age filter is not decoration. ListMultipartUploads returns every
// upload currently in flight, including one a customer's own job started
// thirty seconds ago and is still writing parts to; aborting that one
// destroys work in progress and fails their job. The recommendation has
// always said "abort uploads older than N days" — this is where that N stops
// being prose on an approval screen and starts being a filter. An upload
// with no Initiated timestamp is left alone rather than assumed stale.
//
// olderThanDays <= 0 means no age filter, which is the caller explicitly
// asking for every upload.
func listStaleUploads(ctx context.Context, c s3API, bucket, prefix string, olderThanDays int, now time.Time) ([]s3types.MultipartUpload, error) {
	var all []s3types.MultipartUpload
	in := &s3.ListMultipartUploadsInput{Bucket: aws.String(bucket)}
	if prefix != "" {
		in.Prefix = aws.String(prefix)
	}
	cutoff := now.Add(-time.Duration(olderThanDays) * 24 * time.Hour)
	keep := func(u s3types.MultipartUpload) bool {
		if olderThanDays <= 0 {
			return true
		}
		if u.Initiated == nil {
			return false
		}
		return u.Initiated.Before(cutoff)
	}
	for {
		out, err := c.ListMultipartUploads(ctx, in)
		if err != nil {
			return nil, err
		}
		for _, u := range out.Uploads {
			if keep(u) {
				all = append(all, u)
			}
		}
		if out.IsTruncated == nil || !*out.IsTruncated {
			return all, nil
		}
		in.KeyMarker = out.NextKeyMarker
		in.UploadIdMarker = out.NextUploadIdMarker
	}
}

// uploadAgeCutoffDays reads the recommendation's staleness threshold. It has
// no default: a missing threshold means the plan never said how old is old
// enough, and aborting every in-flight upload is not a safe reading of
// silence.
func uploadAgeCutoffDays(params map[string]any) int {
	days, _ := paramInt(params, "older_than_days")
	return days
}

var abortMultipartUploadsSpec = spec[s3API]{
	action: optimize.ActionAbortMultipartUploads, kind: cloud.KindS3Bucket,
	awsAction: "s3:AbortMultipartUpload", titleFmt: "abort stale in-progress multipart uploads in %s",
	requiredActions: []string{"s3:ListBucketMultipartUploads", "s3:AbortMultipartUpload"},
	// Once a part is discarded there is no API to bring it back — a fresh
	// upload of the same key starts from nothing, not from where the
	// aborted one left off.
	rollbackFeasible: false, infeasibleReason: "aborted multipart upload parts cannot be resumed or recovered",
	dataLossRisk: core.RiskMedium,
	captureState: func(ctx context.Context, c s3API, bucket string, params map[string]any, _ core.Region) (map[string]any, bool, error) {
		prefix, _ := paramStr(params, "prefix")
		uploads, err := listStaleUploads(ctx, c, bucket, prefix, uploadAgeCutoffDays(params), time.Now().UTC())
		if err != nil {
			if isS3NoSuchBucket(err) {
				return nil, false, nil
			}
			return nil, false, awserr.Translate(err, "s3", "ListMultipartUploads", "s3:ListBucketMultipartUploads")
		}
		return map[string]any{"upload_count": len(uploads)}, true, nil
	},
	isApplied: func(current, _ map[string]any) bool {
		n, _ := current["upload_count"].(int)
		return n == 0
	},
	mutate: func(ctx context.Context, c s3API, bucket string, params map[string]any, _ core.Region) (map[string]any, error) {
		prefix, _ := paramStr(params, "prefix")
		uploads, err := listStaleUploads(ctx, c, bucket, prefix, uploadAgeCutoffDays(params), time.Now().UTC())
		if err != nil {
			return nil, awserr.Translate(err, "s3", "ListMultipartUploads", "s3:ListBucketMultipartUploads")
		}
		aborted := 0
		for _, u := range uploads {
			_, err := c.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
				Bucket: aws.String(bucket), Key: u.Key, UploadId: u.UploadId,
			})
			if err != nil {
				return nil, awserr.Translate(err, "s3", "AbortMultipartUpload", "s3:AbortMultipartUpload")
			}
			aborted++
		}
		return map[string]any{"upload_count": 0, "aborted_count": aborted}, nil
	},
}
