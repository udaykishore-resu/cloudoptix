package notify

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/smtp"
	"strings"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// smtpCredentials is the JSON shape SMTPNotifier expects a resolved
// n.SecretRef to unmarshal into, when a tenant configures their own relay
// rather than using the platform default. Host/Port let a tenant point at
// their own mail relay entirely (a common ask in regulated environments
// that require outbound mail to transit their own infrastructure).
type smtpCredentials struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// SMTPNotifier sends email over SMTP with STARTTLS/PLAIN auth via the
// standard library's net/smtp. It is the default email channel — no AWS
// dependency — and the one a self-hosted deployment reaches for.
type SMTPNotifier struct {
	// Host/Port/Username/Password are the platform default relay, used when
	// a notification's SecretRef is empty. A tenant-specific relay
	// (resolved from SecretRef as smtpCredentials JSON) overrides all four
	// per-send.
	Host     string
	Port     int
	Username string
	Password string
	From     string

	Secrets ports.SecretResolver
	Logger  *slog.Logger

	// dial is overridden in tests to avoid a real network connection.
	dial func(addr string) (smtpClient, error)
}

// smtpClient is the subset of *smtp.Client this package calls, so a test can
// substitute a fake without a real SMTP server. *smtp.Client itself
// satisfies this interface, but production code never needs to say so
// explicitly since smtp.SendMail (used in Send's real path) already drives
// the whole protocol internally.
type smtpClient interface {
	Hello(localName string) error
	StartTLS(config *tls.Config) error
	Extension(ext string) (bool, string)
	Auth(a smtp.Auth) error
	Mail(from string) error
	Rcpt(to string) error
	Data() (io.WriteCloser, error)
	Quit() error
}

var _ ports.Notifier = (*SMTPNotifier)(nil)

// NewSMTPNotifier builds a notifier around the platform's default relay.
func NewSMTPNotifier(host string, port int, username, password, from string, secrets ports.SecretResolver, logger *slog.Logger) *SMTPNotifier {
	if logger == nil {
		logger = slog.Default()
	}
	return &SMTPNotifier{
		Host: host, Port: port, Username: username, Password: password, From: from,
		Secrets: secrets, Logger: logger,
	}
}

// Channel identifies this notifier to the dispatcher's channel-type routing.
func (s *SMTPNotifier) Channel() string { return "email" }

// Send delivers one notification to n.Target, which must be an email
// address. Body is sent as the plain-text part; when Blocks carries an
// "html" string key that is sent too, as a simple multipart/alternative
// message — most alerting content is plain text, so HTML is opportunistic,
// not required.
func (s *SMTPNotifier) Send(ctx context.Context, n ports.Notification) error {
	if n.Target == "" {
		return core.Invalid("notify: SMTP notification has no target email address")
	}
	host, port, user, pass, err := s.credentialsFor(ctx, n)
	if err != nil {
		return fmt.Errorf("notify: resolving SMTP credentials: %w", err)
	}
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))

	msg := buildRFC822(s.From, n.Target, n.Subject, n.Body, htmlBodyOf(n))

	send := smtp.SendMail
	if s.dial != nil {
		return s.sendViaFake(addr, host, user, pass, n.Target, msg)
	}
	var auth smtp.Auth
	if user != "" {
		auth = smtp.PlainAuth("", user, pass, host)
	}
	if err := send(addr, auth, s.From, []string{n.Target}, msg); err != nil {
		return fmt.Errorf("notify: SMTP send to %s failed: %w", n.Target, err)
	}
	return nil
}

// sendViaFake drives the injected smtpClient (test-only path) through the
// same protocol steps net/smtp.SendMail performs, so a test exercises this
// package's own message construction and error handling rather than a
// reimplementation of the SMTP state machine.
func (s *SMTPNotifier) sendViaFake(addr, host, user, pass, to string, msg []byte) error {
	c, err := s.dial(addr)
	if err != nil {
		return fmt.Errorf("notify: connecting to SMTP relay %s: %w", addr, err)
	}
	defer c.Quit()
	if err := c.Hello("localhost"); err != nil {
		return err
	}
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
			return err
		}
	}
	if user != "" {
		if err := c.Auth(smtp.PlainAuth("", user, pass, host)); err != nil {
			return fmt.Errorf("notify: SMTP auth failed: %w", err)
		}
	}
	if err := c.Mail(s.From); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	return w.Close()
}

func (s *SMTPNotifier) credentialsFor(ctx context.Context, n ports.Notification) (host string, port int, user, pass string, err error) {
	if n.SecretRef == "" {
		return s.Host, s.Port, s.Username, s.Password, nil
	}
	if s.Secrets == nil {
		return "", 0, "", "", fmt.Errorf("notify: notification references secret %q but no SecretResolver is configured", n.SecretRef)
	}
	raw, err := s.Secrets.Resolve(ctx, n.SecretRef)
	if err != nil {
		return "", 0, "", "", err
	}
	var creds smtpCredentials
	if jerr := json.Unmarshal([]byte(raw), &creds); jerr != nil {
		return "", 0, "", "", fmt.Errorf("notify: secret %q is not valid SMTP credential JSON: %w", n.SecretRef, jerr)
	}
	if creds.Host == "" {
		creds.Host = s.Host
	}
	if creds.Port == 0 {
		creds.Port = s.Port
	}
	return creds.Host, creds.Port, creds.Username, creds.Password, nil
}

// buildRFC822 assembles a minimal, valid RFC 822 message. It deliberately
// does not pull in a MIME library: a plain-text alert with an optional HTML
// alternative does not need one, and every field here is either
// platform-controlled or already escaped by the caller's template renderer.
func buildRFC822(from, to, subject, textBody, htmlBody string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	fmt.Fprintf(&b, "MIME-Version: 1.0\r\n")
	if htmlBody == "" {
		fmt.Fprintf(&b, "Content-Type: text/plain; charset=UTF-8\r\n\r\n")
		b.WriteString(textBody)
		return []byte(b.String())
	}
	const boundary = "cloudoptix-boundary"
	fmt.Fprintf(&b, "Content-Type: multipart/alternative; boundary=%q\r\n\r\n", boundary)
	fmt.Fprintf(&b, "--%s\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s\r\n", boundary, textBody)
	fmt.Fprintf(&b, "--%s\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n%s\r\n", boundary, htmlBody)
	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return []byte(b.String())
}

func htmlBodyOf(n ports.Notification) string {
	if v, ok := n.Blocks["html"]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
