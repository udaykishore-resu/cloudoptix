package notify

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/smtp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// fakeSMTPClient implements smtpClient entirely in memory, recording the
// protocol steps SMTPNotifier.sendViaFake drives it through.
type fakeSMTPClient struct {
	helloCalled    bool
	startTLSCalled bool
	authCalled     bool
	mailFrom       string
	rcptTo         []string
	written        strings.Builder
	quitCalled     bool

	hasStartTLS bool
	authErr     error
	rcptErr     error
}

func (f *fakeSMTPClient) Hello(string) error { f.helloCalled = true; return nil }
func (f *fakeSMTPClient) StartTLS(*tls.Config) error {
	f.startTLSCalled = true
	return nil
}
func (f *fakeSMTPClient) Extension(ext string) (bool, string) {
	if ext == "STARTTLS" && f.hasStartTLS {
		return true, ""
	}
	return false, ""
}
func (f *fakeSMTPClient) Auth(a smtp.Auth) error {
	f.authCalled = true
	return f.authErr
}
func (f *fakeSMTPClient) Mail(from string) error { f.mailFrom = from; return nil }
func (f *fakeSMTPClient) Rcpt(to string) error {
	if f.rcptErr != nil {
		return f.rcptErr
	}
	f.rcptTo = append(f.rcptTo, to)
	return nil
}
func (f *fakeSMTPClient) Data() (io.WriteCloser, error) { return &nopWriteCloser{&f.written}, nil }
func (f *fakeSMTPClient) Quit() error                   { f.quitCalled = true; return nil }

type nopWriteCloser struct{ w io.Writer }

func (n *nopWriteCloser) Write(p []byte) (int, error) { return n.w.Write(p) }
func (n *nopWriteCloser) Close() error                { return nil }

func testSMTPNotifier(fake *fakeSMTPClient) *SMTPNotifier {
	return &SMTPNotifier{
		Host: "relay.example.com", Port: 587, Username: "platform", Password: "platform-pass",
		From: "alerts@cloudoptix.example", Logger: discardLogger(),
		dial: func(string) (smtpClient, error) { return fake, nil },
	}
}

func TestSMTPNotifier_Channel(t *testing.T) {
	assert.Equal(t, "email", (&SMTPNotifier{}).Channel())
}

func TestSMTPNotifier_Send_RefusesEmptyTarget(t *testing.T) {
	n := testSMTPNotifier(&fakeSMTPClient{})
	err := n.Send(context.Background(), ports.Notification{Subject: "s", Body: "b"})
	require.Error(t, err)
}

func TestSMTPNotifier_Send_DrivesFullProtocolAndWritesMessage(t *testing.T) {
	fake := &fakeSMTPClient{hasStartTLS: true}
	n := testSMTPNotifier(fake)

	err := n.Send(context.Background(), ports.Notification{
		Target: "customer@example.com", Subject: "Test alert", Body: "Body text here",
	})
	require.NoError(t, err)

	assert.True(t, fake.helloCalled)
	assert.True(t, fake.startTLSCalled, "STARTTLS must be used when the relay advertises it")
	assert.True(t, fake.authCalled, "platform default credentials are configured, so auth must be attempted")
	assert.Equal(t, "alerts@cloudoptix.example", fake.mailFrom)
	require.Equal(t, []string{"customer@example.com"}, fake.rcptTo)
	assert.True(t, fake.quitCalled)

	msg := fake.written.String()
	assert.Contains(t, msg, "Subject: Test alert")
	assert.Contains(t, msg, "Body text here")
	assert.Contains(t, msg, "From: alerts@cloudoptix.example")
	assert.Contains(t, msg, "To: customer@example.com")
}

func TestSMTPNotifier_Send_IncludesHTMLAlternativeWhenBlocksCarriesHTML(t *testing.T) {
	fake := &fakeSMTPClient{}
	n := testSMTPNotifier(fake)

	err := n.Send(context.Background(), ports.Notification{
		Target: "customer@example.com", Subject: "s", Body: "plain body",
		Blocks: map[string]any{"html": "<p>rich body</p>"},
	})
	require.NoError(t, err)
	msg := fake.written.String()
	assert.Contains(t, msg, "multipart/alternative")
	assert.Contains(t, msg, "plain body")
	assert.Contains(t, msg, "<p>rich body</p>")
}

func TestSMTPNotifier_Send_SkipsAuthWhenNoCredentialsConfigured(t *testing.T) {
	fake := &fakeSMTPClient{}
	n := &SMTPNotifier{
		Host: "relay.example.com", Port: 25, From: "alerts@cloudoptix.example", Logger: discardLogger(),
		dial: func(string) (smtpClient, error) { return fake, nil },
	}
	require.NoError(t, n.Send(context.Background(), ports.Notification{Target: "c@example.com", Subject: "s", Body: "b"}))
	assert.False(t, fake.authCalled, "no username configured means no auth should be attempted")
}

func TestSMTPNotifier_Send_AuthFailurePropagates(t *testing.T) {
	fake := &fakeSMTPClient{authErr: errors.New("bad credentials")}
	n := testSMTPNotifier(fake)
	err := n.Send(context.Background(), ports.Notification{Target: "c@example.com", Subject: "s", Body: "b"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bad credentials")
}

func TestSMTPNotifier_Send_PerTenantCredentialsOverridePlatformDefault(t *testing.T) {
	fake := &fakeSMTPClient{}
	secrets := newFakeSecretResolver(map[string]string{
		"secret://tenant-relay": `{"host":"tenant-relay.example.com","port":2525,"username":"tenant-user","password":"tenant-pass"}`,
	})
	n := testSMTPNotifier(fake)
	n.Secrets = secrets

	require.NoError(t, n.Send(context.Background(), ports.Notification{
		Target: "c@example.com", Subject: "s", Body: "b", SecretRef: "secret://tenant-relay",
	}))
	assert.True(t, fake.authCalled)
}

func TestSMTPNotifier_Send_UnresolvableSecretRefIsAnError(t *testing.T) {
	fake := &fakeSMTPClient{}
	n := testSMTPNotifier(fake)
	n.Secrets = newFakeSecretResolver(nil)
	err := n.Send(context.Background(), ports.Notification{Target: "c@example.com", Subject: "s", Body: "b", SecretRef: "secret://missing"})
	require.Error(t, err)
}
