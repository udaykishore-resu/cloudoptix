package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/memstore"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/spec"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

const testTenant = core.TenantID("tenant-notify-test")

var testNow = time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC) // a Monday, 12:00 UTC

func ctxFor(tenant core.TenantID) context.Context {
	return core.WithPrincipal(context.Background(), core.SystemPrincipal(tenant, "test"))
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, nil))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// fakeSecretResolver resolves a fixed set of refs, and errors on any other.
type fakeSecretResolver struct {
	values map[string]string
}

func newFakeSecretResolver(values map[string]string) *fakeSecretResolver {
	return &fakeSecretResolver{values: values}
}

func (f *fakeSecretResolver) Resolve(_ context.Context, ref string) (string, error) {
	v, ok := f.values[ref]
	if !ok {
		return "", fmt.Errorf("fakeSecretResolver: no value configured for ref %q", ref)
	}
	return v, nil
}

// fakeNotifier records every notification it was asked to send and returns
// a caller-configured error (or nil) each time, so a test can drive
// SendPending's retry/terminal-failure branching precisely.
type fakeNotifier struct {
	mu        sync.Mutex
	channel   string
	sent      []ports.Notification
	nextErr   error // consulted then cleared on every Send call, unless errAlways is set
	errAlways error
}

func newFakeNotifier(channel string) *fakeNotifier { return &fakeNotifier{channel: channel} }

func (f *fakeNotifier) Channel() string { return f.channel }

func (f *fakeNotifier) Send(_ context.Context, n ports.Notification) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, n)
	if f.errAlways != nil {
		return f.errAlways
	}
	err := f.nextErr
	f.nextErr = nil
	return err
}

func (f *fakeNotifier) sentCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

// retryableErr and permanentErr let tests choose which branch of
// SendPending's retry logic a failure takes, mirroring how automation's own
// tests distinguish core.ErrThrottled-shaped failures from ordinary ones.
var errRetryableTest = fmt.Errorf("transient: %w", core.ErrThrottled)
var errPermanentTest = errors.New("permanently rejected")

// testStore builds a fresh in-memory Repositories bundle and a Dispatcher
// wired against it, with the given notifiers registered by channel type.
func testStore(t *testing.T) (ports.Repositories, *Dispatcher, map[string]*fakeNotifier) {
	t.Helper()
	store := memstore.New()
	repos := store.Repositories()

	notifiers := map[string]*fakeNotifier{
		"email":   newFakeNotifier("email"),
		"slack":   newFakeNotifier("slack"),
		"webhook": newFakeNotifier("webhook"),
	}
	portNotifiers := map[string]ports.Notifier{}
	for k, v := range notifiers {
		portNotifiers[k] = v
	}

	dp := NewDispatcher(Deps{
		Specs: repos.Specs, Notifications: repos.Notifications, Notifiers: portNotifiers,
		Clock: core.FixedClock{T: testNow}, Logger: discardLogger(),
	})
	return repos, dp, notifiers
}

func seedSpec(t *testing.T, repos ports.Repositories, sp spec.Spec) {
	t.Helper()
	v := spec.Version{
		ID: core.NewID("sv"), TenantID: testTenant, SpecID: core.NewID("spec"), Version: 1,
		Status: spec.StatusApproved, Spec: sp, CreatedAt: testNow,
	}
	if err := repos.Specs.SaveDraft(ctxFor(testTenant), v); err != nil {
		t.Fatalf("seeding spec: %v", err)
	}
}
