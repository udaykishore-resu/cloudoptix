package discovery

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
)

type fakeSecretsManager struct {
	pages [][]smtypes.SecretListEntry
	call  int
}

func (f *fakeSecretsManager) ListSecrets(context.Context, *secretsmanager.ListSecretsInput, ...func(*secretsmanager.Options)) (*secretsmanager.ListSecretsOutput, error) {
	if f.call >= len(f.pages) {
		return &secretsmanager.ListSecretsOutput{}, nil
	}
	page := f.pages[f.call]
	f.call++
	out := &secretsmanager.ListSecretsOutput{SecretList: page}
	if f.call < len(f.pages) {
		out.NextToken = aws.String("more")
	}
	return out, nil
}

func TestSecretsManagerDiscoverer_NormalizesSecretMetadata(t *testing.T) {
	f := &fakeSecretsManager{pages: [][]smtypes.SecretListEntry{{{
		Name: aws.String("prod/db/password"), ARN: aws.String("arn:aws:secretsmanager:us-east-1:222222222222:secret:prod/db/password-Ab12Cd"),
		RotationEnabled: aws.Bool(true), OwningService: aws.String(""), KmsKeyId: aws.String("alias/secrets"),
		Tags: []smtypes.Tag{{Key: aws.String("Environment"), Value: aws.String("prod")}},
	}}}}
	d := &SecretsManagerDiscoverer{newClient: func(aws.Config) secretsManagerAPI { return f }}
	out, err := d.Discover(context.Background(), discoveryInput())
	require.NoError(t, err)
	require.Len(t, out.Resources, 1)

	s := out.Resources[0]
	assert.Equal(t, cloud.KindSecret, s.Kind)
	assert.Equal(t, "true", s.Attr("rotation_enabled", ""))
	assert.Equal(t, cloud.StateAvailable, s.State)
	assert.Equal(t, "prod", s.Tags["Environment"])
}

func TestSecretsManagerDiscoverer_DeletedSecretIsModifyingState(t *testing.T) {
	f := &fakeSecretsManager{pages: [][]smtypes.SecretListEntry{{{
		Name: aws.String("stale-secret"), ARN: aws.String("arn:aws:secretsmanager:us-east-1:222222222222:secret:stale-secret-Ab12Cd"),
		DeletedDate: aws.Time(time.Now()),
	}}}}
	d := &SecretsManagerDiscoverer{newClient: func(aws.Config) secretsManagerAPI { return f }}
	out, err := d.Discover(context.Background(), discoveryInput())
	require.NoError(t, err)
	require.Len(t, out.Resources, 1)
	assert.Equal(t, cloud.StateModifying, out.Resources[0].State)
	assert.Equal(t, "true", out.Resources[0].Attr("scheduled_for_deletion", ""))
}

func TestSecretsManagerDiscoverer_RequiredActions(t *testing.T) {
	d := NewSecretsManagerDiscoverer()
	assert.Equal(t, "secretsmanager", d.Service())
	assert.Contains(t, d.RequiredActions(), "secretsmanager:ListSecrets")
}
