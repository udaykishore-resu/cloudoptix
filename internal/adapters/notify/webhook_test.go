package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

func TestWebhookNotifier_Channel(t *testing.T) {
	assert.Equal(t, "webhook", NewWebhookNotifier(nil, nil).Channel())
}

func TestWebhookNotifier_Send_RefusesWithNoSecretRef(t *testing.T) {
	n := NewWebhookNotifier(newFakeSecretResolver(nil), discardLogger())
	err := n.Send(context.Background(), ports.Notification{Subject: "s", Body: "b"})
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrInvalidInput)
}

func TestWebhookNotifier_Send_SignsRequestAndSendsEnvelope(t *testing.T) {
	const signingKey = "top-secret-signing-key"
	var gotBody []byte
	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	secretJSON, err := json.Marshal(map[string]string{"url": srv.URL + "/hook", "signing_key": signingKey})
	require.NoError(t, err)
	secrets := newFakeSecretResolver(map[string]string{"secret://wh": string(secretJSON)})

	fixedNow := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	n := NewWebhookNotifier(secrets, discardLogger())
	n.httpClient = srv.Client()
	n.now = func() time.Time { return fixedNow }

	err = n.Send(context.Background(), ports.Notification{
		ID: core.NewID("ntf"), TenantID: testTenant, SecretRef: "secret://wh",
		Subject: "s", Body: "b", EventType: ports.EventCostAnomalyDetected, Severity: core.SeverityHigh,
	})
	require.NoError(t, err)

	ts := gotHeaders.Get("X-CloudOptix-Timestamp")
	require.NotEmpty(t, ts)
	sig := gotHeaders.Get("X-CloudOptix-Signature")
	require.NotEmpty(t, sig)
	assert.Equal(t, string(ports.EventCostAnomalyDetected), gotHeaders.Get("X-CloudOptix-Event"))

	mac := hmac.New(sha256.New, []byte(signingKey))
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(gotBody)
	want := hex.EncodeToString(mac.Sum(nil))
	assert.Equal(t, want, sig, "the receiver must be able to reproduce the exact same signature from timestamp+body")

	var env map[string]any
	require.NoError(t, json.Unmarshal(gotBody, &env))
	assert.Equal(t, string(testTenant), env["tenant_id"])
	assert.Equal(t, "s", env["subject"])
}

func TestWebhookNotifier_Send_TamperedBodyFailsSignatureVerification(t *testing.T) {
	// This documents the receiver-side guarantee HMAC signing exists to
	// provide: recomputing the signature over a body the attacker altered
	// after signing must not match what was sent.
	body := []byte(`{"subject":"original"}`)
	ts := "1700000000"
	sig := signWebhookBody("key", ts, body)

	tampered := []byte(`{"subject":"tampered"}`)
	recomputed := signWebhookBody("key", ts, tampered)
	assert.NotEqual(t, sig, recomputed)
}

func TestWebhookNotifier_Send_NoSigningKeyOmitsSignatureHeaders(t *testing.T) {
	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	secretJSON, _ := json.Marshal(map[string]string{"url": srv.URL})
	secrets := newFakeSecretResolver(map[string]string{"secret://wh": string(secretJSON)})
	n := NewWebhookNotifier(secrets, discardLogger())
	n.httpClient = srv.Client()

	require.NoError(t, n.Send(context.Background(), ports.Notification{SecretRef: "secret://wh", Subject: "s", Body: "b"}))
	assert.Empty(t, gotHeaders.Get("X-CloudOptix-Signature"))
}

func TestWebhookNotifier_Send_NonSuccessStatusIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("boom"))
	}))
	defer srv.Close()

	secretJSON, _ := json.Marshal(map[string]string{"url": srv.URL})
	secrets := newFakeSecretResolver(map[string]string{"secret://wh": string(secretJSON)})
	n := NewWebhookNotifier(secrets, discardLogger())
	n.httpClient = srv.Client()

	err := n.Send(context.Background(), ports.Notification{SecretRef: "secret://wh", Subject: "s", Body: "b"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}

func TestWebhookNotifier_Send_MalformedSecretJSONIsAnError(t *testing.T) {
	secrets := newFakeSecretResolver(map[string]string{"secret://wh": "not json"})
	n := NewWebhookNotifier(secrets, discardLogger())
	err := n.Send(context.Background(), ports.Notification{SecretRef: "secret://wh", Subject: "s", Body: "b"})
	require.Error(t, err)
}
