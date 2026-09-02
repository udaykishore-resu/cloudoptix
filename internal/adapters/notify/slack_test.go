package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

func TestSlackNotifier_Channel(t *testing.T) {
	assert.Equal(t, "slack", NewSlackNotifier(nil, nil).Channel())
}

func TestSlackNotifier_Send_RefusesWithNoSecretRef(t *testing.T) {
	n := NewSlackNotifier(newFakeSecretResolver(nil), discardLogger())
	err := n.Send(context.Background(), ports.Notification{Target: "#alerts", Subject: "s", Body: "b"})
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrInvalidInput)
}

func TestSlackNotifier_Send_PostsBlockKitPayloadWithResolvedWebhookURL(t *testing.T) {
	var gotBody map[string]any
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	secrets := newFakeSecretResolver(map[string]string{"secret://slack": srv.URL + "/services/T00/B00/XXX"})
	n := NewSlackNotifier(secrets, discardLogger())
	n.httpClient = srv.Client()

	err := n.Send(context.Background(), ports.Notification{
		SecretRef: "secret://slack", Subject: "Cost anomaly", Body: "Something changed",
		Severity: core.SeverityHigh, EventType: ports.EventCostAnomalyDetected, LinkURL: "https://app.example.com/x",
	})
	require.NoError(t, err)
	assert.Equal(t, "/services/T00/B00/XXX", gotPath)
	require.Contains(t, gotBody, "blocks")
	blocks, ok := gotBody["blocks"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, blocks)

	header, ok := blocks[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "header", header["type"])

	// The last block should be the action button, since LinkURL was set.
	last, ok := blocks[len(blocks)-1].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "actions", last["type"])
}

func TestSlackNotifier_Send_UsesCallerSuppliedBlocksVerbatim(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	secrets := newFakeSecretResolver(map[string]string{"secret://slack": srv.URL})
	n := NewSlackNotifier(secrets, discardLogger())
	n.httpClient = srv.Client()

	customBlocks := []any{map[string]any{"type": "divider"}}
	err := n.Send(context.Background(), ports.Notification{
		SecretRef: "secret://slack", Subject: "s", Body: "b",
		Blocks: map[string]any{"blocks": customBlocks},
	})
	require.NoError(t, err)
	blocks, ok := gotBody["blocks"].([]any)
	require.True(t, ok)
	require.Len(t, blocks, 1)
	first, _ := blocks[0].(map[string]any)
	assert.Equal(t, "divider", first["type"])
}

func TestSlackNotifier_Send_NonOKStatusIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("invalid_payload"))
	}))
	defer srv.Close()

	secrets := newFakeSecretResolver(map[string]string{"secret://slack": srv.URL})
	n := NewSlackNotifier(secrets, discardLogger())
	n.httpClient = srv.Client()

	err := n.Send(context.Background(), ports.Notification{SecretRef: "secret://slack", Subject: "s", Body: "b"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid_payload")
}

func TestSlackNotifier_Send_SecretResolutionErrorPropagates(t *testing.T) {
	n := NewSlackNotifier(newFakeSecretResolver(nil), discardLogger())
	err := n.Send(context.Background(), ports.Notification{SecretRef: "secret://missing", Subject: "s", Body: "b"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no value configured")
}
