package executor

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/optimize"
)

type fakeS3Exec struct {
	rules       []s3types.LifecycleRule
	noLifecycle bool
	noBucket    bool
	uploads     []s3types.MultipartUpload
	calls       map[string]int
}

func newFakeS3Exec() *fakeS3Exec { return &fakeS3Exec{calls: map[string]int{}} }

func (f *fakeS3Exec) GetBucketLifecycleConfiguration(_ context.Context, in *s3.GetBucketLifecycleConfigurationInput, _ ...func(*s3.Options)) (*s3.GetBucketLifecycleConfigurationOutput, error) {
	f.calls["GetBucketLifecycleConfiguration"]++
	if f.noBucket {
		return nil, notFoundErr("NoSuchBucket")
	}
	if f.noLifecycle {
		return nil, notFoundErr("NoSuchLifecycleConfiguration")
	}
	return &s3.GetBucketLifecycleConfigurationOutput{Rules: f.rules}, nil
}

func (f *fakeS3Exec) PutBucketLifecycleConfiguration(_ context.Context, in *s3.PutBucketLifecycleConfigurationInput, _ ...func(*s3.Options)) (*s3.PutBucketLifecycleConfigurationOutput, error) {
	f.calls["PutBucketLifecycleConfiguration"]++
	f.rules = in.LifecycleConfiguration.Rules
	f.noLifecycle = false
	return &s3.PutBucketLifecycleConfigurationOutput{}, nil
}

func (f *fakeS3Exec) DeleteBucketLifecycle(_ context.Context, in *s3.DeleteBucketLifecycleInput, _ ...func(*s3.Options)) (*s3.DeleteBucketLifecycleOutput, error) {
	f.calls["DeleteBucketLifecycle"]++
	f.rules = nil
	f.noLifecycle = true
	return &s3.DeleteBucketLifecycleOutput{}, nil
}

func (f *fakeS3Exec) ListMultipartUploads(_ context.Context, in *s3.ListMultipartUploadsInput, _ ...func(*s3.Options)) (*s3.ListMultipartUploadsOutput, error) {
	f.calls["ListMultipartUploads"]++
	if f.noBucket {
		return nil, notFoundErr("NoSuchBucket")
	}
	return &s3.ListMultipartUploadsOutput{Uploads: f.uploads}, nil
}

func (f *fakeS3Exec) AbortMultipartUpload(_ context.Context, in *s3.AbortMultipartUploadInput, _ ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error) {
	f.calls["AbortMultipartUpload"]++
	var kept []s3types.MultipartUpload
	for _, u := range f.uploads {
		if aws.ToString(u.UploadId) != aws.ToString(in.UploadId) {
			kept = append(kept, u)
		}
	}
	f.uploads = kept
	return &s3.AbortMultipartUploadOutput{}, nil
}

func s3Executor(sp spec[s3API], f *fakeS3Exec) *genericExecutor[s3API] {
	return &genericExecutor[s3API]{spec: sp, newClient: func(any) s3API { return f }}
}

func TestApplyS3Lifecycle_AddsRuleWithoutClobberingExisting(t *testing.T) {
	f := newFakeS3Exec()
	f.rules = []s3types.LifecycleRule{{ID: aws.String("tenant-own-rule"), Status: s3types.ExpirationStatusEnabled}}
	ex := s3Executor(applyS3LifecycleSpec, f)
	res := testResource(cloud.KindS3Bucket, "my-bucket")
	params := map[string]any{"transition_days": 30, "transition_storage_class": "GLACIER"}
	plan, err := ex.Plan(context.Background(), testPlanInput(optimize.ActionApplyS3Lifecycle, res, params))
	require.NoError(t, err)

	_, err = ex.Apply(context.Background(), testSession(), plan, plan.Steps[2])
	require.NoError(t, err)
	require.Len(t, f.rules, 2, "the tenant's own rule must survive alongside the new managed one")

	// Idempotent: re-applying the same params does not call Put again.
	out, err := ex.Apply(context.Background(), testSession(), plan, plan.Steps[2])
	require.NoError(t, err)
	assert.Equal(t, true, out["idempotent"])
	assert.Equal(t, 1, f.calls["PutBucketLifecycleConfiguration"])

	require.NoError(t, ex.Rollback(context.Background(), testSession(), plan, plan.Rollback.Steps[0]))
	require.Len(t, f.rules, 1)
	assert.Equal(t, "tenant-own-rule", aws.ToString(f.rules[0].ID))
}

func TestApplyS3Lifecycle_RollbackDeletesConfigurationThatDidNotExistBefore(t *testing.T) {
	f := newFakeS3Exec()
	f.noLifecycle = true
	ex := s3Executor(applyS3LifecycleSpec, f)
	res := testResource(cloud.KindS3Bucket, "my-bucket")
	plan, err := ex.Plan(context.Background(), testPlanInput(optimize.ActionApplyS3Lifecycle, res, map[string]any{"expiration_days": 90}))
	require.NoError(t, err)

	_, err = ex.Apply(context.Background(), testSession(), plan, plan.Steps[2])
	require.NoError(t, err)
	require.Len(t, f.rules, 1)

	require.NoError(t, ex.Rollback(context.Background(), testSession(), plan, plan.Rollback.Steps[0]))
	assert.Equal(t, 1, f.calls["DeleteBucketLifecycle"])
	assert.Empty(t, f.rules)
}

func TestAbortMultipartUploads_AbortsAllAndIsIdempotent(t *testing.T) {
	f := newFakeS3Exec()
	f.uploads = []s3types.MultipartUpload{{Key: aws.String("a"), UploadId: aws.String("u1")}, {Key: aws.String("b"), UploadId: aws.String("u2")}}
	ex := s3Executor(abortMultipartUploadsSpec, f)
	res := testResource(cloud.KindS3Bucket, "my-bucket")
	plan, err := ex.Plan(context.Background(), testPlanInput(optimize.ActionAbortMultipartUploads, res, nil))
	require.NoError(t, err)
	assert.False(t, plan.Rollback.Feasible)

	out, err := ex.Apply(context.Background(), testSession(), plan, plan.Steps[2])
	require.NoError(t, err)
	assert.Equal(t, 2, out["aborted_count"])
	assert.Empty(t, f.uploads)

	out, err = ex.Apply(context.Background(), testSession(), plan, plan.Steps[2])
	require.NoError(t, err)
	assert.Equal(t, true, out["idempotent"])
}
