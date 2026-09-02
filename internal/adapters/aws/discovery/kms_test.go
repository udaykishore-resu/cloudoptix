package discovery

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
)

type fakeKMS struct {
	keys []kmstypes.KeyListEntry
	meta map[string]*kmstypes.KeyMetadata
}

func (f *fakeKMS) ListKeys(context.Context, *kms.ListKeysInput, ...func(*kms.Options)) (*kms.ListKeysOutput, error) {
	return &kms.ListKeysOutput{Keys: f.keys}, nil
}
func (f *fakeKMS) DescribeKey(_ context.Context, in *kms.DescribeKeyInput, _ ...func(*kms.Options)) (*kms.DescribeKeyOutput, error) {
	return &kms.DescribeKeyOutput{KeyMetadata: f.meta[aws.ToString(in.KeyId)]}, nil
}
func (f *fakeKMS) ListResourceTags(context.Context, *kms.ListResourceTagsInput, ...func(*kms.Options)) (*kms.ListResourceTagsOutput, error) {
	return &kms.ListResourceTagsOutput{Tags: []kmstypes.Tag{{TagKey: aws.String("Environment"), TagValue: aws.String("prod")}}}, nil
}

func TestKMSDiscoverer_NormalizesKeyMetadata(t *testing.T) {
	keyID := "1234abcd-12ab-34cd-56ef-1234567890ab"
	f := &fakeKMS{
		keys: []kmstypes.KeyListEntry{{KeyId: aws.String(keyID)}},
		meta: map[string]*kmstypes.KeyMetadata{
			keyID: {
				KeyId: aws.String(keyID), Arn: aws.String("arn:aws:kms:us-east-1:222222222222:key/" + keyID),
				Description: aws.String("data encryption key"), KeyState: kmstypes.KeyStateEnabled,
				KeyManager: kmstypes.KeyManagerTypeCustomer, KeyUsage: kmstypes.KeyUsageTypeEncryptDecrypt,
				Enabled: true,
			},
		},
	}
	d := &KMSDiscoverer{newClient: func(aws.Config) kmsAPI { return f }}
	out, err := d.Discover(context.Background(), discoveryInput())
	require.NoError(t, err)
	require.Len(t, out.Resources, 1)

	key := out.Resources[0]
	assert.Equal(t, cloud.StateAvailable, key.State)
	assert.Equal(t, "CUSTOMER", key.Attr("key_manager", ""))
	assert.Equal(t, "true", key.Attr("enabled", ""))
	assert.Equal(t, "prod", key.Tags["Environment"])
}

func TestKMSState(t *testing.T) {
	assert.Equal(t, cloud.StateAvailable, kmsState(kmstypes.KeyStateEnabled))
	assert.Equal(t, cloud.StateStopped, kmsState(kmstypes.KeyStateDisabled))
	assert.Equal(t, cloud.StateModifying, kmsState(kmstypes.KeyStatePendingDeletion))
}

func TestKMSDiscoverer_RequiredActions(t *testing.T) {
	d := NewKMSDiscoverer()
	assert.Equal(t, "kms", d.Service())
	assert.Contains(t, d.RequiredActions(), "kms:DescribeKey")
}
