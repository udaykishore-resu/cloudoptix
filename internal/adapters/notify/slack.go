package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// SlackNotifier posts to a Slack incoming webhook. The webhook URL itself is
// the credential — anyone holding it can post as the configured app — so it
// is resolved per-notification from n.SecretRef via SecretResolver rather
// than ever appearing in a tenant's specification (see doc.go).
type SlackNotifier struct {
	Secrets ports.SecretResolver
	Logger  *slog.Logger

	httpClient interface {
		Do(*http.Request) (*http.Response, error)
	}
}

var _ ports.Notifier = (*SlackNotifier)(nil)

// NewSlackNotifier builds a Slack notifier.
func NewSlackNotifier(secrets ports.SecretResolver, logger *slog.Logger) *SlackNotifier {
	if logger == nil {
		logger = slog.Default()
	}
	return &SlackNotifier{Secrets: secrets, Logger: logger, httpClient: http.DefaultClient}
}

// Channel identifies this notifier to the dispatcher's channel-type routing.
func (s *SlackNotifier) Channel() string { return "slack" }

// Send posts n as a Block Kit message. When n.Blocks already carries a
// "blocks" key (a pre-rendered []any of Block Kit block objects — see
// render.go) that is used verbatim; otherwise a minimal single-section
// block is built from Subject/Body/Severity so a caller that only filled in
// the plain-text fields still gets a well-formed message.
func (s *SlackNotifier) Send(ctx context.Context, n ports.Notification) error {
	if s.Secrets == nil {
		return fmt.Errorf("notify: SlackNotifier has no SecretResolver configured")
	}
	if n.SecretRef == "" {
		return core.Invalid("notify: Slack notification has no secret_ref pointing at an incoming webhook URL")
	}
	webhookURL, err := s.Secrets.Resolve(ctx, n.SecretRef)
	if err != nil {
		return fmt.Errorf("notify: resolving Slack webhook URL: %w", err)
	}
	if webhookURL == "" {
		return fmt.Errorf("notify: secret %q resolved to an empty Slack webhook URL", n.SecretRef)
	}

	payload := map[string]any{"text": n.Subject + "\n" + n.Body}
	if blocks, ok := n.Blocks["blocks"]; ok {
		payload["blocks"] = blocks
	} else {
		payload["blocks"] = defaultSlackBlocks(n)
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("notify: encoding Slack payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("notify: building Slack request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("notify: Slack webhook request failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("notify: Slack webhook rejected message: status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// severityEmoji gives each severity a distinct, at-a-glance marker in a
// Slack header — a channel scrolling past dozens of alerts a day needs to
// triage by eye before reading a word.
func severityEmoji(sev core.Severity) string {
	switch sev {
	case core.SeverityCritical:
		return "🚨"
	case core.SeverityHigh:
		return "⚠️"
	case core.SeverityMedium:
		return "🔶"
	case core.SeverityLow:
		return "ℹ️"
	default:
		return "•"
	}
}

// defaultSlackBlocks builds a real Block Kit block list: a header, a
// section with the body as markdown, and — when LinkURL is set — a button
// action linking back into the platform. This is the payload shape Slack's
// Block Kit Builder validates, not a text-only fallback dressed up as
// blocks.
func defaultSlackBlocks(n ports.Notification) []map[string]any {
	blocks := []map[string]any{
		{
			"type": "header",
			"text": map[string]any{
				"type": "plain_text", "text": fmt.Sprintf("%s %s", severityEmoji(n.Severity), n.Subject), "emoji": true,
			},
		},
		{
			"type": "section",
			"text": map[string]any{"type": "mrkdwn", "text": n.Body},
		},
		{
			"type": "context",
			"elements": []map[string]any{
				{"type": "mrkdwn", "text": fmt.Sprintf("*Severity:* %s  |  *Event:* %s", n.Severity, n.EventType)},
			},
		},
	}
	if n.LinkURL != "" {
		blocks = append(blocks, map[string]any{
			"type": "actions",
			"elements": []map[string]any{
				{
					"type":  "button",
					"text":  map[string]any{"type": "plain_text", "text": "View in CloudOptix", "emoji": true},
					"url":   n.LinkURL,
					"style": "primary",
				},
			},
		})
	}
	return blocks
}
