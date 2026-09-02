package awserr

import (
	"errors"
	"testing"

	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

func apiErr(code, msg string) error {
	return &smithy.GenericAPIError{Code: code, Message: msg}
}

func TestThrottled(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"throttling exception", apiErr("ThrottlingException", "rate exceeded"), true},
		{"request limit exceeded", apiErr("RequestLimitExceeded", "too fast"), true},
		{"dynamodb capacity", apiErr("ProvisionedThroughputExceededException", "over capacity"), true},
		{"unrelated code", apiErr("ValidationException", "bad input"), false},
		{"plain error", errors.New("boom"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, Throttled(tc.err))
		})
	}
}

func TestAccessDenied(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"access denied", apiErr("AccessDenied", "nope"), true},
		{"access denied exception", apiErr("AccessDeniedException", "nope"), true},
		{"unauthorized operation", apiErr("UnauthorizedOperation", "nope"), true},
		{"unrelated", apiErr("ResourceNotFoundException", "gone"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, AccessDenied(tc.err))
		})
	}
}

func TestDeniedAction(t *testing.T) {
	msg := "User: arn:aws:iam::111111111111:role/cloudoptix-read is not authorized to perform: ec2:DescribeNatGateways on resource: *"
	got := DeniedAction(apiErr("UnauthorizedOperation", msg), "ec2:DescribeInstances")
	assert.Equal(t, "ec2:DescribeNatGateways", got)

	// No action named in the message: falls back to the caller-supplied one.
	got = DeniedAction(apiErr("AccessDenied", "Access Denied"), "s3:ListAllMyBuckets")
	assert.Equal(t, "s3:ListAllMyBuckets", got)

	// Not even an API error: falls back too.
	got = DeniedAction(errors.New("boom"), "kms:ListKeys")
	assert.Equal(t, "kms:ListKeys", got)
}

func TestTranslate(t *testing.T) {
	t.Run("nil passes through", func(t *testing.T) {
		assert.NoError(t, Translate(nil, "ec2", "DescribeInstances", "ec2:DescribeInstances"))
	})

	t.Run("throttle wraps ErrThrottled", func(t *testing.T) {
		err := Translate(apiErr("ThrottlingException", "slow down"), "ec2", "DescribeInstances", "ec2:DescribeInstances")
		require.Error(t, err)
		assert.True(t, errors.Is(err, core.ErrThrottled))
	})

	t.Run("access denied wraps ErrForbidden with the action detail", func(t *testing.T) {
		msg := "is not authorized to perform: rds:DescribeDBInstances on resource"
		err := Translate(apiErr("AccessDenied", msg), "rds", "DescribeDBInstances", "rds:DescribeDBInstances")
		require.Error(t, err)
		assert.True(t, errors.Is(err, core.ErrForbidden))
		var ce *core.Error
		require.True(t, errors.As(err, &ce))
		assert.Equal(t, "rds:DescribeDBInstances", ce.Details["action"])
	})

	t.Run("access denied falls back to the caller's action when unparseable", func(t *testing.T) {
		err := Translate(apiErr("AccessDenied", "Access Denied"), "s3", "ListBuckets", "s3:ListAllMyBuckets")
		require.Error(t, err)
		var ce *core.Error
		require.True(t, errors.As(err, &ce))
		assert.Equal(t, "s3:ListAllMyBuckets", ce.Details["action"])
	})

	t.Run("other errors pass through unwrapped", func(t *testing.T) {
		orig := apiErr("ValidationException", "bad input")
		err := Translate(orig, "ec2", "DescribeInstances", "ec2:DescribeInstances")
		assert.Equal(t, orig, err)
	})
}

func TestServiceUnavailable(t *testing.T) {
	assert.True(t, ServiceUnavailable(apiErr("UnknownOperationException", "operation not supported in this region")))
	assert.False(t, ServiceUnavailable(apiErr("InvalidClientTokenId", "the security token included in the request is invalid")))
	assert.False(t, ServiceUnavailable(apiErr("AccessDenied", "nope")))
}
