package auth

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

func TestStaticAPIKeyStore_NeverStoresPlaintext(t *testing.T) {
	principal := core.Principal{Subject: "svc:ci", TenantID: "tenant-a", Machine: true}
	store := NewStaticAPIKeyStore(map[string]APIKeyPrincipal{
		"plaintext-key-value": {Principal: principal, Label: "ci-integration"},
	})
	for hash := range store.keys {
		assert.NotEqual(t, "plaintext-key-value", hash)
	}
}

func TestValidateAPIKey_Succeeds(t *testing.T) {
	principal := core.Principal{Subject: "svc:ci", TenantID: "tenant-a", Machine: true}
	store := NewStaticAPIKeyStore(map[string]APIKeyPrincipal{
		"correct-key": {Principal: principal, Label: "ci-integration"},
	})
	p, err := ValidateAPIKey(context.Background(), store, "correct-key", time.Hour)
	require.NoError(t, err)
	assert.Equal(t, core.TenantID("tenant-a"), p.TenantID)
	assert.False(t, p.ExpiresAt.IsZero())
}

func TestValidateAPIKey_RejectsUnknownKey(t *testing.T) {
	store := NewStaticAPIKeyStore(map[string]APIKeyPrincipal{
		"correct-key": {Principal: core.Principal{TenantID: "tenant-a"}},
	})
	_, err := ValidateAPIKey(context.Background(), store, "wrong-key", time.Hour)
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrUnauthenticated)
}

func TestHashAPIKey_Deterministic(t *testing.T) {
	assert.Equal(t, HashAPIKey("abc"), HashAPIKey("abc"))
	assert.NotEqual(t, HashAPIKey("abc"), HashAPIKey("abd"))
}
