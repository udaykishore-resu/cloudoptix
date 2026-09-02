// This file discovers Secrets Manager secrets. Unlike most of this
// package's services, ListSecrets returns everything needed — ARN, name,
// tags, rotation status — in one paginated call, so there is no N+1 here:
// every secret's metadata comes off the same page it was listed on. The
// secret's own value is never read (secretsmanager:GetSecretValue is not in
// RequiredActions and this file never calls it); a discoverer that could
// read secret material would be a far bigger blast radius than a discoverer
// needs.
package discovery

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

type secretsManagerAPI interface {
	ListSecrets(ctx context.Context, in *secretsmanager.ListSecretsInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.ListSecretsOutput, error)
}

type SecretsManagerDiscoverer struct {
	newClient func(aws.Config) secretsManagerAPI
}

var _ ports.ResourceDiscoverer = (*SecretsManagerDiscoverer)(nil)

func NewSecretsManagerDiscoverer() *SecretsManagerDiscoverer {
	return &SecretsManagerDiscoverer{newClient: func(cfg aws.Config) secretsManagerAPI { return secretsmanager.NewFromConfig(cfg) }}
}

func (d *SecretsManagerDiscoverer) Service() string     { return "secretsmanager" }
func (d *SecretsManagerDiscoverer) Kinds() []cloud.Kind { return []cloud.Kind{cloud.KindSecret} }
func (d *SecretsManagerDiscoverer) RequiredActions() []string {
	return []string{"secretsmanager:ListSecrets"}
}

func (d *SecretsManagerDiscoverer) Discover(ctx context.Context, in ports.DiscoveryInput) (ports.DiscoveryOutput, error) {
	cfg, err := configFor(in)
	if err != nil {
		return ports.DiscoveryOutput{}, err
	}
	client := d.newClient(cfg)
	ctx, cancel := ctxWithDefaultTimeout(ctx)
	defer cancel()

	b := newBuilder(in)
	p := secretsmanager.NewListSecretsPaginator(client, &secretsmanager.ListSecretsInput{})
	for p.HasMorePages() {
		b.countCall()
		page, err := p.NextPage(ctx)
		if err != nil {
			if skipUnavailable(err) {
				b.warnf("secretsmanager: not available in region %s: %v", in.Region, err)
				return b.out, nil
			}
			return b.out, b.wrap(err, "secretsmanager", "ListSecrets", "secretsmanager:ListSecrets")
		}
		for _, s := range page.SecretList {
			addSecret(b, in, s)
		}
	}
	return b.out, nil
}

func addSecret(b *builder, in ports.DiscoveryInput, s smtypes.SecretListEntry) {
	nativeID := aws.ToString(s.Name)
	pairs := make([][2]string, 0, len(s.Tags))
	for _, t := range s.Tags {
		pairs = append(pairs, [2]string{aws.ToString(t.Key), aws.ToString(t.Value)})
	}
	state := cloud.StateAvailable
	if s.DeletedDate != nil {
		state = cloud.StateModifying // scheduled for deletion, within the recovery window
	}
	b.add(resourceSpec{
		Kind: cloud.KindSecret, NativeID: nativeID, ARN: core.ARN(aws.ToString(s.ARN)),
		Name: nativeID, Region: in.Region, State: state,
		Purchase: cloud.PurchaseUnknown, Tags: tagsFromKV(pairs),
		Attributes: attrs("rotation_enabled", boolStr(aws.ToBool(s.RotationEnabled)),
			"owning_service", aws.ToString(s.OwningService), "kms_key_id", aws.ToString(s.KmsKeyId),
			"scheduled_for_deletion", boolStr(s.DeletedDate != nil)),
		CreatedAt: aws.ToTime(s.CreatedDate), DiscoveredBy: "aws.secretsmanager",
	})
}
