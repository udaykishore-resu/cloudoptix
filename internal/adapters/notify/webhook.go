package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// webhookSecret is the JSON shape a generic webhook channel's SecretRef
// resolves to: both the delivery URL and the signing key are treated as
// secret (see doc.go), so both travel together in one secret rather than
// splitting the URL into spec.NotificationChannel.Target.
type webhookSecret struct {
	URL        string `json:"url"`
	SigningKey string `json:"signing_key"`
}

// WebhookNotifier posts a JSON envelope to an arbitrary customer-owned
// HTTPS endpoint, signed with an HMAC-SHA256 request signature so the
// receiver can verify the request actually came from CloudOptix and was
// not tampered with in transit — the same pattern Stripe, GitHub and most
// other webhook providers use, chosen over mutual TLS because it needs no
// certificate provisioning on the customer's side.
type WebhookNotifier struct {
	Secrets ports.SecretResolver
	Logger  *slog.Logger

	httpClient interface {
		Do(*http.Request) (*http.Response, error)
	}
	now func() time.Time
}

var _ ports.Notifier = (*WebhookNotifier)(nil)

// NewWebhookNotifier builds a generic webhook notifier.
func NewWebhookNotifier(secrets ports.SecretResolver, logger *slog.Logger) *WebhookNotifier {
	if logger == nil {
		logger = slog.Default()
	}
	return &WebhookNotifier{Secrets: secrets, Logger: logger, httpClient: http.DefaultClient, now: time.Now}
}

// Channel identifies this notifier to the dispatcher's channel-type routing.
func (w *WebhookNotifier) Channel() string { return "webhook" }

// webhookEnvelope is the stable JSON body every generic webhook receives —
// the fields a receiver needs to route and render the alert without any
// CloudOptix-internal type.
type webhookEnvelope struct {
	ID        string         `json:"id"`
	TenantID  string         `json:"tenant_id"`
	EventType string         `json:"event_type"`
	Severity  string         `json:"severity"`
	Subject   string         `json:"subject"`
	Body      string         `json:"body"`
	LinkURL   string         `json:"link_url,omitempty"`
	Data      map[string]any `json:"data,omitempty"`
	SentAt    time.Time      `json:"sent_at"`
}

// Send POSTs the envelope with three signature headers: X-CloudOptix-Timestamp
// (the signing time, so a receiver can reject stale/replayed requests),
// X-CloudOptix-Signature (hex HMAC-SHA256 over "<timestamp>.<body>", binding
// the signature to a specific instant rather than the body alone), and
// X-CloudOptix-Event (the event type, for receivers that route without
// parsing JSON first).
func (w *WebhookNotifier) Send(ctx context.Context, n ports.Notification) error {
	if w.Secrets == nil {
		return fmt.Errorf("notify: WebhookNotifier has no SecretResolver configured")
	}
	if n.SecretRef == "" {
		return core.Invalid("notify: webhook notification has no secret_ref pointing at its destination and signing key")
	}
	raw, err := w.Secrets.Resolve(ctx, n.SecretRef)
	if err != nil {
		return fmt.Errorf("notify: resolving webhook secret: %w", err)
	}
	var secret webhookSecret
	if err := json.Unmarshal([]byte(raw), &secret); err != nil {
		return fmt.Errorf("notify: secret %q is not valid webhook JSON: %w", n.SecretRef, err)
	}
	if secret.URL == "" {
		return fmt.Errorf("notify: secret %q carries no destination url", n.SecretRef)
	}

	env := webhookEnvelope{
		ID: n.ID.String(), TenantID: string(n.TenantID), EventType: string(n.EventType),
		Severity: string(n.Severity), Subject: n.Subject, Body: n.Body, LinkURL: n.LinkURL,
		Data: n.Blocks, SentAt: w.now().UTC(),
	}
	body, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("notify: encoding webhook payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, secret.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("notify: building webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CloudOptix-Event", string(n.EventType))

	if secret.SigningKey != "" {
		ts := strconv.FormatInt(w.now().Unix(), 10)
		sig := signWebhookBody(secret.SigningKey, ts, body)
		req.Header.Set("X-CloudOptix-Timestamp", ts)
		req.Header.Set("X-CloudOptix-Signature", sig)
	}

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("notify: webhook request to %s failed: %w", secret.URL, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("notify: webhook %s rejected request: status %d: %s", secret.URL, resp.StatusCode, string(respBody))
	}
	return nil
}

// signWebhookBody computes hex(HMAC-SHA256(key, "<timestamp>.<body>")).
// Exported behavior (via the header format Send writes) rather than the
// function itself, so a receiver's own verification code only needs to
// reproduce this one line, not import anything from CloudOptix.
func signWebhookBody(key, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
