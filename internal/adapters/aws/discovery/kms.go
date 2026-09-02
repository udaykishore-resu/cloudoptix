// This file discovers KMS keys. ListKeys returns bare key ids/ARNs, so each
// key costs one DescribeKey call plus one ListResourceTags call — the same
// N+1 pattern used elsewhere in this package for services with no bulk
// describe. AWS-managed keys (KeyManager == AWS) are included like any
// other key: they cost real money in some regions/usages and a cost or
// security engine deciding whether to care about one is a downstream
// concern, not this discoverer's to pre-filter.
package discovery

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

type kmsAPI interface {
	ListKeys(ctx context.Context, in *kms.ListKeysInput, optFns ...func(*kms.Options)) (*kms.ListKeysOutput, error)
	DescribeKey(ctx context.Context, in *kms.DescribeKeyInput, optFns ...func(*kms.Options)) (*kms.DescribeKeyOutput, error)
	ListResourceTags(ctx context.Context, in *kms.ListResourceTagsInput, optFns ...func(*kms.Options)) (*kms.ListResourceTagsOutput, error)
}

type KMSDiscoverer struct {
	newClient func(aws.Config) kmsAPI
}

var _ ports.ResourceDiscoverer = (*KMSDiscoverer)(nil)

func NewKMSDiscoverer() *KMSDiscoverer {
	return &KMSDiscoverer{newClient: func(cfg aws.Config) kmsAPI { return kms.NewFromConfig(cfg) }}
}

func (d *KMSDiscoverer) Service() string     { return "kms" }
func (d *KMSDiscoverer) Kinds() []cloud.Kind { return []cloud.Kind{cloud.KindKMSKey} }
func (d *KMSDiscoverer) RequiredActions() []string {
	return []string{"kms:ListKeys", "kms:DescribeKey", "kms:ListResourceTags"}
}

func (d *KMSDiscoverer) Discover(ctx context.Context, in ports.DiscoveryInput) (ports.DiscoveryOutput, error) {
	cfg, err := configFor(in)
	if err != nil {
		return ports.DiscoveryOutput{}, err
	}
	client := d.newClient(cfg)
	ctx, cancel := ctxWithDefaultTimeout(ctx)
	defer cancel()

	b := newBuilder(in)
	p := kms.NewListKeysPaginator(client, &kms.ListKeysInput{})
	for p.HasMorePages() {
		b.countCall()
		page, err := p.NextPage(ctx)
		if err != nil {
			if skipUnavailable(err) {
				b.warnf("kms: not available in region %s: %v", in.Region, err)
				return b.out, nil
			}
			return b.out, b.wrap(err, "kms", "ListKeys", "kms:ListKeys")
		}
		for _, k := range page.Keys {
			d.addKey(ctx, b, client, in, aws.ToString(k.KeyId))
		}
	}
	return b.out, nil
}

func (d *KMSDiscoverer) addKey(ctx context.Context, b *builder, client kmsAPI, in ports.DiscoveryInput, keyID string) {
	b.countCall()
	desc, err := client.DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: aws.String(keyID)})
	if err != nil {
		b.warnf("kms: could not describe key %s: %v", keyID, err)
		return
	}
	m := desc.KeyMetadata
	if m == nil {
		return
	}

	tags := core.Tags{}
	b.countCall()
	if tagResp, err := client.ListResourceTags(ctx, &kms.ListResourceTagsInput{KeyId: aws.String(keyID)}); err == nil {
		pairs := make([][2]string, 0, len(tagResp.Tags))
		for _, t := range tagResp.Tags {
			pairs = append(pairs, [2]string{aws.ToString(t.TagKey), aws.ToString(t.TagValue)})
		}
		tags = tagsFromKV(pairs)
	}

	b.add(resourceSpec{
		Kind: cloud.KindKMSKey, NativeID: keyID, ARN: core.ARN(aws.ToString(m.Arn)),
		Name: aws.ToString(m.Description), Region: in.Region, State: kmsState(m.KeyState),
		Purchase: cloud.PurchaseUnknown, Tags: tags,
		Attributes: attrs("key_manager", string(m.KeyManager), "key_usage", string(m.KeyUsage),
			"key_spec", string(m.KeySpec), "origin", string(m.Origin),
			"multi_region", boolStr(aws.ToBool(m.MultiRegion)), "enabled", boolStr(m.Enabled)),
		CreatedAt: aws.ToTime(m.CreationDate), DiscoveredBy: "aws.kms",
	})
}

// kmsState maps KMS's own KeyState vocabulary (which mapState's generic
// switch does not cover — "PendingDeletion", "PendingImport", etc. are KMS
// specific) onto cloud.State.
func kmsState(s kmstypes.KeyState) cloud.State {
	switch s {
	case kmstypes.KeyStateEnabled:
		return cloud.StateAvailable
	case kmstypes.KeyStateDisabled:
		return cloud.StateStopped
	case kmstypes.KeyStatePendingDeletion, kmstypes.KeyStatePendingReplicaDeletion:
		return cloud.StateModifying
	case kmstypes.KeyStatePendingImport:
		return cloud.StatePending
	case kmstypes.KeyStateUnavailable:
		return cloud.StateFailed
	default:
		return cloud.StateUnknown
	}
}
