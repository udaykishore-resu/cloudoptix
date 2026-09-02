package notify

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	v4signer "github.com/aws/aws-sdk-go-v2/aws/signer/v4"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// sesSigningService is the SigV4 signing name for the SESv2 API. It differs
// from the endpoint's own "email" prefix — AWS services routinely sign
// under a name distinct from their endpoint hostname (S3 is the best-known
// example) — this is SES v2's documented signing name.
const sesSigningService = "ses"

// SESNotifier sends email through Amazon SES's v2 HTTPS API
// (POST /v2/email/outbound-emails), signed by hand with SigV4 via
// aws-sdk-go-v2's signer/v4 subpackage. There is no dedicated SES client
// package vendored in this module, and pulling one in is outside this
// package's remit (no new dependencies) — the v2 API is a small enough
// surface (one JSON POST) that hand-signing it is the honest alternative to
// either adding a dependency or silently degrading to SMTP-only email.
type SESNotifier struct {
	Region      string
	Credentials awssdk.CredentialsProvider
	From        string
	Logger      *slog.Logger

	// endpoint and httpClient are overridden in tests to avoid a real
	// network call.
	endpoint   string
	httpClient interface {
		Do(*http.Request) (*http.Response, error)
	}
	signer *v4signer.Signer
}

var _ ports.Notifier = (*SESNotifier)(nil)

// NewSESNotifier builds a notifier from an AWS config, matching the pattern
// NewEventBridgePublisher and NewSQSSubscriber already use elsewhere in
// this codebase (config loaded once by the caller, e.g. via
// aws-sdk-go-v2/config.LoadDefaultConfig against CloudOptix's own platform
// account — SES sends under CloudOptix's own verified sending identity).
func NewSESNotifier(cfg awssdk.Config, from string, logger *slog.Logger) *SESNotifier {
	if logger == nil {
		logger = slog.Default()
	}
	return &SESNotifier{
		Region: cfg.Region, Credentials: cfg.Credentials, From: from, Logger: logger,
		endpoint:   fmt.Sprintf("https://email.%s.amazonaws.com/v2/email/outbound-emails", cfg.Region),
		httpClient: http.DefaultClient,
		signer:     v4signer.NewSigner(),
	}
}

// Channel identifies this notifier to the dispatcher's channel-type routing.
func (s *SESNotifier) Channel() string { return "email" }

// sesSendEmailRequest mirrors the small slice of the SESv2 SendEmail
// request body this package actually populates: a simple text/HTML message
// to one recipient. SES's real schema has many more optional fields
// (templates, configuration sets, tags); none of those are needed for a
// platform alert email.
type sesSendEmailRequest struct {
	FromEmailAddress string     `json:"FromEmailAddress"`
	Destination      sesDest    `json:"Destination"`
	Content          sesContent `json:"Content"`
	ReplyToAddresses []string   `json:"ReplyToAddresses,omitempty"`
}

type sesDest struct {
	ToAddresses []string `json:"ToAddresses"`
}

type sesContent struct {
	Simple sesSimple `json:"Simple"`
}

type sesSimple struct {
	Subject sesText `json:"Subject"`
	Body    sesBody `json:"Body"`
}

type sesBody struct {
	Text *sesText `json:"Text,omitempty"`
	Html *sesText `json:"Html,omitempty"`
}

type sesText struct {
	Data    string `json:"Data"`
	Charset string `json:"Charset"`
}

// Send delivers one notification via the SESv2 SendEmail API. n.SecretRef
// is deliberately not consulted here — see doc.go — SES sending uses the
// AWS credentials this notifier was constructed with, not a per-tenant
// secret.
func (s *SESNotifier) Send(ctx context.Context, n ports.Notification) error {
	if n.Target == "" {
		return core.Invalid("notify: SES notification has no target email address")
	}
	body := sesSendEmailRequest{
		FromEmailAddress: s.From,
		Destination:      sesDest{ToAddresses: []string{n.Target}},
		Content: sesContent{Simple: sesSimple{
			Subject: sesText{Data: n.Subject, Charset: "UTF-8"},
			Body:    sesBody{Text: &sesText{Data: n.Body, Charset: "UTF-8"}},
		}},
	}
	if html := htmlBodyOf(n); html != "" {
		body.Content.Simple.Body.Html = &sesText{Data: html, Charset: "UTF-8"}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("notify: encoding SES request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("notify: building SES request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	if err := s.sign(ctx, req, payload); err != nil {
		return fmt.Errorf("notify: signing SES request: %w", err)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("notify: SES request to %s failed: %w", n.Target, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("notify: SES rejected email to %s: status %d: %s", n.Target, resp.StatusCode, string(respBody))
	}
	return nil
}

func (s *SESNotifier) sign(ctx context.Context, req *http.Request, payload []byte) error {
	if s.Credentials == nil {
		return fmt.Errorf("notify: SESNotifier has no credentials provider configured")
	}
	creds, err := s.Credentials.Retrieve(ctx)
	if err != nil {
		return fmt.Errorf("notify: retrieving AWS credentials: %w", err)
	}
	sum := sha256.Sum256(payload)
	payloadHash := hex.EncodeToString(sum[:])
	return s.signer.SignHTTP(ctx, creds, req, payloadHash, sesSigningService, s.Region, time.Now())
}
