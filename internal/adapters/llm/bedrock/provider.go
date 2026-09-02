// Package bedrock implements ports.LLMProvider over Amazon Bedrock's
// InvokeModel HTTPS API, targeting Bedrock-hosted Anthropic Claude models.
// Bedrock speaks the same Messages-API JSON shape as the direct Anthropic API
// (see internal/adapters/llm/anthropicwire, shared with the anthropic
// package) with two differences: the model is named in the URL path rather
// than a body field, and authentication is SigV4 request signing against
// temporary or long-lived AWS credentials rather than an API key header.
//
// SigV4 signing here uses github.com/aws/aws-sdk-go-v2/aws/signer/v4 — the
// AWS SDK's own signer implementation. That package is already present in
// go.sum as part of the aws-sdk-go-v2 module (this module already pulls in
// several aws-sdk-go-v2 service clients as indirect dependencies for AWS
// discovery and execution elsewhere in CloudOptix), so importing it directly
// adds no new module dependency — go.mod's require block already names
// github.com/aws/aws-sdk-go-v2 and github.com/aws/aws-sdk-go-v2/credentials
// at the exact versions this package builds against.
// service/bedrockruntime is not vendored in this build, which is why this
// package calls the InvokeModel HTTPS endpoint directly with net/http rather
// than through a generated client — the request/response shape for
// InvokeModel is a stable, documented raw HTTPS contract independent of the
// generated SDK client.
//
// Traceability: REQ-AI-002, REQ-AI-006, SPEC-AI-001.
package bedrock

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/credentials"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/llm/anthropicwire"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

const (
	anthropicBedrockVersion = "bedrock-2023-05-31"
	defaultModelID          = "anthropic.claude-3-5-sonnet-20241022-v2:0"
	defaultEmbedModelID     = "amazon.titan-embed-text-v1"
	defaultRegion           = "us-east-1"
	defaultTimeout          = 60 * time.Second
	defaultMaxRetries       = 4
	defaultRetryBase        = 500 * time.Millisecond
	defaultRetryMax         = 8 * time.Second
	serviceName             = "bedrock"
)

// CredentialsProvider is the narrow slice of ports.AWSSession this package
// needs: enough to sign a request, without depending on ports.AWSSession's
// AWS-SDK-shaped Config method (whose concrete return type this package would
// otherwise have to type-assert). A caller that already holds a
// ports.AWSSession/AWSCredentialBroker for CloudOptix's customer-account
// access can supply one that also implements this by wrapping
// aws.Config.Credentials; ConfigFromEnv builds one from the ambient AWS
// credential chain for CloudOptix's own platform Bedrock access (which is a
// CloudOptix-owned account, not a customer's — customer infrastructure access
// always goes through ports.AWSCredentialBroker, never through this path).
type CredentialsProvider interface {
	Retrieve(ctx context.Context) (awssdk.Credentials, error)
}

// Config holds everything the provider needs.
type Config struct {
	Region      string
	ModelID     string
	EmbedModel  string
	Credentials CredentialsProvider
	Timeout     time.Duration
	MaxRetries  int
	// Endpoint overrides the computed bedrock-runtime endpoint, for testing.
	Endpoint string
}

// ConfigFromEnv reads AWS_REGION/AWS_DEFAULT_REGION, BEDROCK_MODEL_ID,
// BEDROCK_EMBED_MODEL_ID and BEDROCK_TIMEOUT_SECONDS, and builds a
// CredentialsProvider from the ambient AWS environment-variable credential
// chain (AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY/AWS_SESSION_TOKEN). It
// returns ok=false when no access key is present, mirroring
// anthropic.ConfigFromEnv's "absent means not constructible" contract.
func ConfigFromEnv() (Config, bool) {
	accessKey := os.Getenv("AWS_ACCESS_KEY_ID")
	if accessKey == "" {
		return Config{}, false
	}
	secretKey := os.Getenv("AWS_SECRET_ACCESS_KEY")
	sessionToken := os.Getenv("AWS_SESSION_TOKEN")

	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = os.Getenv("AWS_DEFAULT_REGION")
	}
	if region == "" {
		region = defaultRegion
	}
	cfg := Config{
		Region:      region,
		ModelID:     envOr("BEDROCK_MODEL_ID", defaultModelID),
		EmbedModel:  envOr("BEDROCK_EMBED_MODEL_ID", defaultEmbedModelID),
		Credentials: credentials.NewStaticCredentialsProvider(accessKey, secretKey, sessionToken),
		Timeout:     defaultTimeout,
		MaxRetries:  defaultMaxRetries,
	}
	if s := os.Getenv("BEDROCK_TIMEOUT_SECONDS"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			cfg.Timeout = time.Duration(n) * time.Second
		}
	}
	return cfg, true
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Provider implements ports.LLMProvider over Bedrock's InvokeModel API.
type Provider struct {
	cfg    Config
	client *http.Client
	signer *v4.Signer
}

var _ ports.LLMProvider = (*Provider)(nil)

// New builds a Provider. httpClient may be nil.
func New(cfg Config, httpClient *http.Client) *Provider {
	if cfg.Region == "" {
		cfg.Region = defaultRegion
	}
	if cfg.ModelID == "" {
		cfg.ModelID = defaultModelID
	}
	if cfg.EmbedModel == "" {
		cfg.EmbedModel = defaultEmbedModelID
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: cfg.Timeout}
	}
	return &Provider{cfg: cfg, client: httpClient, signer: v4.NewSigner()}
}

// Name satisfies ports.LLMProvider.
func (p *Provider) Name() string { return "bedrock:" + p.cfg.ModelID }

func (p *Provider) endpoint(modelID string) string {
	base := p.cfg.Endpoint
	if base == "" {
		base = fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com", p.cfg.Region)
	}
	return fmt.Sprintf("%s/model/%s/invoke", base, url.PathEscape(modelID))
}

// Complete satisfies ports.LLMProvider.
func (p *Provider) Complete(ctx context.Context, req ports.CompletionRequest) (ports.CompletionResponse, error) {
	wireReq := anthropicwire.BuildRequest(req)
	wireReq.AnthropicVersion = anthropicBedrockVersion
	// Bedrock names the model in the URL, not the body.
	wireReq.Model = ""

	body, err := json.Marshal(wireReq)
	if err != nil {
		return ports.CompletionResponse{}, core.Invalid("bedrock: encoding request: %v", err)
	}

	maxRetries := p.cfg.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			delay := anthropicwire.Backoff(attempt-1, defaultRetryBase, defaultRetryMax)
			select {
			case <-ctx.Done():
				return ports.CompletionResponse{}, ctx.Err()
			case <-time.After(delay):
			}
		}
		start := time.Now()
		wireResp, retryable, err := p.invoke(ctx, p.cfg.ModelID, body)
		latency := time.Since(start).Milliseconds()
		if err == nil {
			return anthropicwire.ParseResponse(wireResp, p.cfg.ModelID, latency), nil
		}
		lastErr = err
		if !retryable || attempt == maxRetries {
			break
		}
	}
	return ports.CompletionResponse{}, lastErr
}

// invoke performs one SigV4-signed InvokeModel HTTPS round trip.
func (p *Provider) invoke(ctx context.Context, modelID string, body []byte) (anthropicwire.Response, bool, error) {
	signedReq, err := p.signedRequest(ctx, modelID, body)
	if err != nil {
		return anthropicwire.Response{}, false, err
	}

	resp, err := p.client.Do(signedReq)
	if err != nil {
		return anthropicwire.Response{}, true, core.NewError(core.ErrUnavailable, "bedrock_transport",
			"bedrock: request failed: %v", err).Wrap(err)
	}
	defer resp.Body.Close()

	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return anthropicwire.Response{}, true, readErr
	}

	if resp.StatusCode != http.StatusOK {
		retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		msg := anthropicwire.ParseErrorBody(respBody)
		sentinel := core.ErrUnavailable
		switch {
		case resp.StatusCode == http.StatusTooManyRequests:
			sentinel = core.ErrThrottled
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			sentinel = core.ErrForbidden
		case resp.StatusCode >= 400 && resp.StatusCode < 500:
			sentinel = core.ErrInvalidInput
		}
		return anthropicwire.Response{}, retryable,
			core.NewError(sentinel, "bedrock_api_error", "bedrock: %s (status %d)", msg, resp.StatusCode)
	}

	var wireResp anthropicwire.Response
	if err := json.Unmarshal(respBody, &wireResp); err != nil {
		return anthropicwire.Response{}, false, fmt.Errorf("bedrock: decoding response: %w", err)
	}
	return wireResp, false, nil
}

// signedRequest builds an http.Request for body and signs it with SigV4
// against the "bedrock" service in p.cfg.Region, using p.cfg.Credentials.
func (p *Provider) signedRequest(ctx context.Context, modelID string, body []byte) (*http.Request, error) {
	if p.cfg.Credentials == nil {
		return nil, core.Invalid("bedrock: no credentials configured")
	}
	creds, err := p.cfg.Credentials.Retrieve(ctx)
	if err != nil {
		return nil, core.NewError(core.ErrForbidden, "bedrock_credentials",
			"bedrock: retrieving credentials: %v", err).Wrap(err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint(modelID), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("accept", "application/json")

	sum := sha256.Sum256(body)
	payloadHash := hex.EncodeToString(sum[:])

	if err := p.signer.SignHTTP(ctx, creds, httpReq, payloadHash, serviceName, p.cfg.Region, time.Now().UTC()); err != nil {
		return nil, core.NewError(core.ErrUnavailable, "bedrock_sigv4",
			"bedrock: signing request: %v", err).Wrap(err)
	}
	return httpReq, nil
}

// titanEmbedRequest and titanEmbedResponse are the InvokeModel body shapes
// for Amazon's Titan Text Embeddings model, the default embedding model this
// provider targets. Titan's InvokeModel contract is a simple
// {"inputText": "..."} -> {"embedding": [...]} shape, unrelated to the
// Anthropic message format used for Complete.
type titanEmbedRequest struct {
	InputText string `json:"inputText"`
}

type titanEmbedResponse struct {
	Embedding           []float32 `json:"embedding"`
	InputTextTokenCount int       `json:"inputTextTokenCount"`
}

// Embed satisfies ports.LLMProvider by calling the configured Titan
// embeddings model once per input text (Titan's InvokeModel contract accepts
// exactly one input string per call; there is no batched embeddings
// endpoint on Bedrock for this model family). A failure on any single text
// aborts the batch and returns the error, which is what tells
// internal/adapters/rag.Store to fall back to its deterministic hashing
// embedder for that call.
func (p *Provider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		body, err := json.Marshal(titanEmbedRequest{InputText: t})
		if err != nil {
			return nil, err
		}
		signedReq, err := p.signedRequest(ctx, p.cfg.EmbedModel, body)
		if err != nil {
			return nil, err
		}
		resp, err := p.client.Do(signedReq)
		if err != nil {
			return nil, core.NewError(core.ErrUnavailable, "bedrock_embed_transport", "bedrock: embedding request failed: %v", err).Wrap(err)
		}
		respBody, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if resp.StatusCode != http.StatusOK {
			return nil, core.NewError(core.ErrUnavailable, "bedrock_embed_api_error",
				"bedrock: embedding call failed: %s (status %d)", anthropicwire.ParseErrorBody(respBody), resp.StatusCode)
		}
		var er titanEmbedResponse
		if err := json.Unmarshal(respBody, &er); err != nil {
			return nil, fmt.Errorf("bedrock: decoding embedding response: %w", err)
		}
		out[i] = er.Embedding
	}
	return out, nil
}

// Healthy satisfies ports.LLMProvider with a minimal InvokeModel call.
func (p *Provider) Healthy(ctx context.Context) bool {
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req := ports.CompletionRequest{
		Purpose:   "health_check",
		Messages:  []ports.Message{{Role: ports.RoleUser, Content: "ping"}},
		MaxTokens: 1,
	}
	_, err := p.Complete(probeCtx, req)
	return err == nil
}
