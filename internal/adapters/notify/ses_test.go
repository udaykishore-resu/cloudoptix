package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	v4signer "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

func staticCreds(id, secret string) awssdk.CredentialsProvider {
	return awssdk.CredentialsProviderFunc(func(context.Context) (awssdk.Credentials, error) {
		return awssdk.Credentials{AccessKeyID: id, SecretAccessKey: secret, Source: "test"}, nil
	})
}

func newTestSESNotifier(endpoint string, httpClient *http.Client) *SESNotifier {
	return &SESNotifier{
		Region: "us-east-1", Credentials: staticCreds("AKIATEST", "secret"), From: "alerts@cloudoptix.example",
		Logger: discardLogger(), endpoint: endpoint, httpClient: httpClient, signer: v4signer.NewSigner(),
	}
}

func TestSESNotifier_Channel(t *testing.T) {
	assert.Equal(t, "email", (&SESNotifier{}).Channel())
}

func TestSESNotifier_Send_RefusesEmptyTarget(t *testing.T) {
	n := newTestSESNotifier("https://example.invalid", http.DefaultClient)
	err := n.Send(context.Background(), ports.Notification{Subject: "s", Body: "b"})
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrInvalidInput)
}

func TestSESNotifier_Send_BuildsCorrectRequestBodyAndSignsIt(t *testing.T) {
	var gotBody map[string]any
	var gotAuth, gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		assert.Equal(t, "/v2/email/outbound-emails", r.URL.Path)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"MessageId":"abc123"}`))
	}))
	defer srv.Close()

	n := newTestSESNotifier(srv.URL+"/v2/email/outbound-emails", srv.Client())
	err := n.Send(context.Background(), ports.Notification{
		Target: "customer@example.com", Subject: "Cost anomaly detected", Body: "Something changed.",
		Blocks: map[string]any{"html": "<p>Something changed.</p>"},
	})
	require.NoError(t, err)

	assert.Equal(t, "application/json", gotContentType)
	require.NotEmpty(t, gotAuth, "the request must carry a SigV4 Authorization header")
	assert.Contains(t, gotAuth, "AWS4-HMAC-SHA256")
	assert.Contains(t, gotAuth, "AKIATEST")

	require.Contains(t, gotBody, "FromEmailAddress")
	assert.Equal(t, "alerts@cloudoptix.example", gotBody["FromEmailAddress"])
	dest, ok := gotBody["Destination"].(map[string]any)
	require.True(t, ok)
	to, ok := dest["ToAddresses"].([]any)
	require.True(t, ok)
	require.Len(t, to, 1)
	assert.Equal(t, "customer@example.com", to[0])

	content, ok := gotBody["Content"].(map[string]any)
	require.True(t, ok)
	simple, ok := content["Simple"].(map[string]any)
	require.True(t, ok)
	subject, ok := simple["Subject"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "Cost anomaly detected", subject["Data"])
	body, ok := simple["Body"].(map[string]any)
	require.True(t, ok)
	require.Contains(t, body, "Html", "an html Blocks entry must produce an Html body part")
}

func TestSESNotifier_Send_RejectionStatusIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"message":"Email address is not verified"}`))
	}))
	defer srv.Close()

	n := newTestSESNotifier(srv.URL, srv.Client())
	err := n.Send(context.Background(), ports.Notification{Target: "customer@example.com", Subject: "s", Body: "b"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not verified")
}

func TestSESNotifier_Send_NoCredentialsProviderIsAnError(t *testing.T) {
	n := &SESNotifier{Region: "us-east-1", From: "a@b.com", Logger: discardLogger(), endpoint: "https://example.invalid", httpClient: http.DefaultClient, signer: v4signer.NewSigner()}
	err := n.Send(context.Background(), ports.Notification{Target: "customer@example.com", Subject: "s", Body: "b"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "credentials")
}
