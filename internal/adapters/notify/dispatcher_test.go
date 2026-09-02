package notify

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/spec"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

func specWithChannels(channels []spec.NotificationChannel, subs map[string][]string, quietHours string) spec.Spec {
	var sp spec.Spec
	sp.Notifications = spec.Notifications{Channels: channels, Subscriptions: subs, QuietHoursUTC: quietHours}
	return sp
}

func TestDispatch_RefusesEventWithNoTenant(t *testing.T) {
	_, dp, _ := testStore(t)
	err := dp.Dispatch(ctxFor(testTenant), ports.Event{Type: ports.EventCostAnomalyDetected})
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrInvalidInput)
}

func TestDispatch_NoSubscriptionForEventTypeEnqueuesNothing(t *testing.T) {
	repos, dp, _ := testStore(t)
	seedSpec(t, repos, specWithChannels(
		[]spec.NotificationChannel{{Name: "ops-email", Type: "email", Target: "ops@example.com"}},
		map[string][]string{"cloudoptix.cost.anomaly_detected": {"ops-email"}},
		"",
	))

	require.NoError(t, dp.Dispatch(ctxFor(testTenant), ports.Event{
		Type: ports.EventSpecApproved, TenantID: testTenant,
	}))

	page, err := repos.Notifications.List(ctxFor(testTenant), testTenant, ports.ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, page.Items, "an event type with no subscription must enqueue nothing")
}

func TestDispatch_EnqueuesOneNotificationPerSubscribedChannel(t *testing.T) {
	repos, dp, _ := testStore(t)
	seedSpec(t, repos, specWithChannels(
		[]spec.NotificationChannel{
			{Name: "ops-email", Type: "email", Target: "ops@example.com"},
			{Name: "ops-slack", Type: "slack", Target: "#alerts", SecretRef: "secret://slack-webhook"},
		},
		map[string][]string{"cloudoptix.cost.anomaly_detected": {"ops-email", "ops-slack"}},
		"",
	))

	require.NoError(t, dp.Dispatch(ctxFor(testTenant), ports.Event{
		Type: ports.EventCostAnomalyDetected, TenantID: testTenant, SubjectID: core.NewID("res"),
		Payload: map[string]any{"resource_name": "web-1", "delta_amount": "$120.00", "delta_pct": 40.0},
	}))

	page, err := repos.Notifications.List(ctxFor(testTenant), testTenant, ports.ListOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)

	byChannel := map[string]ports.Notification{}
	for _, n := range page.Items {
		byChannel[n.Channel] = n
	}
	require.Contains(t, byChannel, "email")
	require.Contains(t, byChannel, "slack")
	assert.Contains(t, byChannel["email"].Subject, "web-1")
	assert.Equal(t, "ops@example.com", byChannel["email"].Target)
	assert.Equal(t, "secret://slack-webhook", byChannel["slack"].SecretRef)
	assert.Equal(t, core.SeverityHigh, byChannel["email"].Severity, "cost anomaly's default severity is HIGH")
}

func TestDispatch_UnknownChannelNameIsSkippedNotFatal(t *testing.T) {
	repos, dp, _ := testStore(t)
	seedSpec(t, repos, specWithChannels(
		[]spec.NotificationChannel{{Name: "ops-email", Type: "email", Target: "ops@example.com"}},
		map[string][]string{"cloudoptix.cost.anomaly_detected": {"does-not-exist"}},
		"",
	))
	err := dp.Dispatch(ctxFor(testTenant), ports.Event{Type: ports.EventCostAnomalyDetected, TenantID: testTenant})
	require.NoError(t, err)

	page, _ := repos.Notifications.List(ctxFor(testTenant), testTenant, ports.ListOptions{})
	assert.Empty(t, page.Items)
}

func TestDispatch_SeverityFilterExcludesChannelWithNoMatchingSeverity(t *testing.T) {
	repos, dp, _ := testStore(t)
	seedSpec(t, repos, specWithChannels(
		[]spec.NotificationChannel{
			{Name: "critical-only", Type: "email", Target: "oncall@example.com", Severities: []string{"CRITICAL"}},
		},
		map[string][]string{"cloudoptix.recommendation.created": {"critical-only"}},
		"",
	))
	// A new-recommendation event renders at INFO severity by default.
	require.NoError(t, dp.Dispatch(ctxFor(testTenant), ports.Event{
		Type: ports.EventRecommendationCreated, TenantID: testTenant,
	}))

	page, _ := repos.Notifications.List(ctxFor(testTenant), testTenant, ports.ListOptions{})
	assert.Empty(t, page.Items, "a channel scoped to CRITICAL must not receive an INFO-severity event")
}

func TestDispatch_SeverityFilterAllowsMatchingSeverity(t *testing.T) {
	repos, dp, _ := testStore(t)
	seedSpec(t, repos, specWithChannels(
		[]spec.NotificationChannel{
			{Name: "regressions", Type: "email", Target: "oncall@example.com", Severities: []string{"CRITICAL", "HIGH"}},
		},
		map[string][]string{"cloudoptix.cost_regression.detected": {"regressions"}},
		"",
	))
	require.NoError(t, dp.Dispatch(ctxFor(testTenant), ports.Event{
		Type: ports.EventCostRegressionDetected, TenantID: testTenant,
	}))

	page, _ := repos.Notifications.List(ctxFor(testTenant), testTenant, ports.ListOptions{})
	require.Len(t, page.Items, 1, "cost regression renders CRITICAL, which is in the channel's allow-list")
}

// --- Quiet hours -----------------------------------------------------------

func TestDispatch_QuietHoursSuppressesNonCriticalAlert(t *testing.T) {
	repos, _, _ := testStore(t)
	seedSpec(t, repos, specWithChannels(
		[]spec.NotificationChannel{{Name: "ops-email", Type: "email", Target: "ops@example.com"}},
		map[string][]string{"cloudoptix.recommendation.created": {"ops-email"}}, // renders INFO
		"22:00-06:00",
	))
	store2 := repos // reuse repos, but build a Dispatcher pinned to a time inside the quiet window
	dpQuiet := NewDispatcher(Deps{
		Specs: store2.Specs, Notifications: store2.Notifications, Notifiers: map[string]ports.Notifier{},
		Clock: core.FixedClock{T: time.Date(2026, 8, 31, 23, 30, 0, 0, time.UTC)}, Logger: discardLogger(),
	})

	require.NoError(t, dpQuiet.Dispatch(ctxFor(testTenant), ports.Event{Type: ports.EventRecommendationCreated, TenantID: testTenant}))

	page, _ := repos.Notifications.List(ctxFor(testTenant), testTenant, ports.ListOptions{})
	assert.Empty(t, page.Items, "an INFO-severity alert during quiet hours must be suppressed")
}

func TestDispatch_QuietHoursNeverSuppressesCritical(t *testing.T) {
	repos, _, _ := testStore(t)
	seedSpec(t, repos, specWithChannels(
		[]spec.NotificationChannel{{Name: "ops-email", Type: "email", Target: "ops@example.com"}},
		map[string][]string{"cloudoptix.cost_regression.detected": {"ops-email"}}, // renders CRITICAL
		"22:00-06:00",
	))
	dpQuiet := NewDispatcher(Deps{
		Specs: repos.Specs, Notifications: repos.Notifications, Notifiers: map[string]ports.Notifier{},
		Clock: core.FixedClock{T: time.Date(2026, 8, 31, 23, 30, 0, 0, time.UTC)}, Logger: discardLogger(),
	})

	require.NoError(t, dpQuiet.Dispatch(ctxFor(testTenant), ports.Event{Type: ports.EventCostRegressionDetected, TenantID: testTenant}))

	page, _ := repos.Notifications.List(ctxFor(testTenant), testTenant, ports.ListOptions{})
	assert.Len(t, page.Items, 1, "a CRITICAL alert must always be delivered, quiet hours or not")
}

func TestDispatch_OutsideQuietHoursDeliversNormally(t *testing.T) {
	repos, _, _ := testStore(t)
	seedSpec(t, repos, specWithChannels(
		[]spec.NotificationChannel{{Name: "ops-email", Type: "email", Target: "ops@example.com"}},
		map[string][]string{"cloudoptix.recommendation.created": {"ops-email"}},
		"22:00-06:00",
	))
	// testNow is 12:00 UTC — well outside 22:00-06:00.
	dp := NewDispatcher(Deps{
		Specs: repos.Specs, Notifications: repos.Notifications, Notifiers: map[string]ports.Notifier{},
		Clock: core.FixedClock{T: testNow}, Logger: discardLogger(),
	})
	require.NoError(t, dp.Dispatch(ctxFor(testTenant), ports.Event{Type: ports.EventRecommendationCreated, TenantID: testTenant}))

	page, _ := repos.Notifications.List(ctxFor(testTenant), testTenant, ports.ListOptions{})
	assert.Len(t, page.Items, 1)
}

func TestInQuietHours_WrapsMidnightCorrectly(t *testing.T) {
	cases := []struct {
		name string
		now  time.Time
		want bool
	}{
		{"well inside, late evening", time.Date(2026, 1, 1, 23, 0, 0, 0, time.UTC), true},
		{"well inside, early morning", time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC), true},
		{"exactly at start", time.Date(2026, 1, 1, 22, 0, 0, 0, time.UTC), true},
		{"exactly at end (exclusive)", time.Date(2026, 1, 1, 6, 0, 0, 0, time.UTC), false},
		{"well outside, midday", time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := inQuietHours("22:00-06:00", tc.now)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestInQuietHours_EmptySpecIsNeverQuiet(t *testing.T) {
	got, err := inQuietHours("", testNow)
	require.NoError(t, err)
	assert.False(t, got)
}

func TestInQuietHours_UnparsableSpecReturnsError(t *testing.T) {
	_, err := inQuietHours("not-a-range", testNow)
	assert.Error(t, err)
}

// --- Deduplication -----------------------------------------------------------

func TestDispatch_DedupesRepeatedAlertWithinWindow(t *testing.T) {
	repos, _, _ := testStore(t)
	sp := specWithChannels(
		[]spec.NotificationChannel{{Name: "ops-email", Type: "email", Target: "ops@example.com"}},
		map[string][]string{"cloudoptix.cost.anomaly_detected": {"ops-email"}},
		"",
	)
	seedSpec(t, repos, sp)
	subject := core.NewID("res")

	// A single Dispatcher instance carries the dedup cache across calls —
	// this is the property the test exercises, so it must not be rebuilt
	// per-call the way the quiet-hours tests above rebuild it to pin a
	// different clock.
	dp := NewDispatcher(Deps{
		Specs: repos.Specs, Notifications: repos.Notifications, Notifiers: map[string]ports.Notifier{},
		Clock: core.FixedClock{T: testNow}, Logger: discardLogger(),
	})

	evt := ports.Event{Type: ports.EventCostAnomalyDetected, TenantID: testTenant, SubjectID: subject}
	require.NoError(t, dp.Dispatch(ctxFor(testTenant), evt))
	require.NoError(t, dp.Dispatch(ctxFor(testTenant), evt)) // same tenant/type/subject/channel, moments later

	page, _ := repos.Notifications.List(ctxFor(testTenant), testTenant, ports.ListOptions{})
	assert.Len(t, page.Items, 1, "a second identical alert within the dedup window must not enqueue a second notification")
}

func TestDispatch_DoesNotDedupeAfterWindowExpires(t *testing.T) {
	repos, _, _ := testStore(t)
	seedSpec(t, repos, specWithChannels(
		[]spec.NotificationChannel{{Name: "ops-email", Type: "email", Target: "ops@example.com"}},
		map[string][]string{"cloudoptix.cost.anomaly_detected": {"ops-email"}},
		"",
	))
	subject := core.NewID("res")
	clock := &mutableClock{t: testNow}
	dp := NewDispatcher(Deps{
		Specs: repos.Specs, Notifications: repos.Notifications, Notifiers: map[string]ports.Notifier{},
		Clock: clock, Logger: discardLogger(),
	})

	evt := ports.Event{Type: ports.EventCostAnomalyDetected, TenantID: testTenant, SubjectID: subject}
	require.NoError(t, dp.Dispatch(ctxFor(testTenant), evt))
	clock.t = clock.t.Add(dedupWindow + time.Minute)
	require.NoError(t, dp.Dispatch(ctxFor(testTenant), evt))

	page, _ := repos.Notifications.List(ctxFor(testTenant), testTenant, ports.ListOptions{})
	assert.Len(t, page.Items, 2, "an alert repeating after the dedup window has elapsed must be delivered again")
}

func TestDispatch_DifferentSubjectsAreNotDeduped(t *testing.T) {
	repos, _, _ := testStore(t)
	seedSpec(t, repos, specWithChannels(
		[]spec.NotificationChannel{{Name: "ops-email", Type: "email", Target: "ops@example.com"}},
		map[string][]string{"cloudoptix.cost.anomaly_detected": {"ops-email"}},
		"",
	))
	dp := NewDispatcher(Deps{
		Specs: repos.Specs, Notifications: repos.Notifications, Notifiers: map[string]ports.Notifier{},
		Clock: core.FixedClock{T: testNow}, Logger: discardLogger(),
	})

	require.NoError(t, dp.Dispatch(ctxFor(testTenant), ports.Event{Type: ports.EventCostAnomalyDetected, TenantID: testTenant, SubjectID: core.NewID("res")}))
	require.NoError(t, dp.Dispatch(ctxFor(testTenant), ports.Event{Type: ports.EventCostAnomalyDetected, TenantID: testTenant, SubjectID: core.NewID("res")}))

	page, _ := repos.Notifications.List(ctxFor(testTenant), testTenant, ports.ListOptions{})
	assert.Len(t, page.Items, 2, "two distinct subjects must each be delivered, not treated as duplicates of each other")
}

// mutableClock lets a test advance time between calls without rebuilding the
// Dispatcher (and therefore without resetting its dedup cache), unlike
// core.FixedClock which is immutable by convention elsewhere in this
// codebase.
type mutableClock struct{ t time.Time }

func (c *mutableClock) Now() time.Time { return c.t }

// --- SendPending -----------------------------------------------------------

func TestSendPending_SuccessfulSendMarksSent(t *testing.T) {
	repos, dp, notifiers := testStore(t)
	require.NoError(t, repos.Notifications.Enqueue(ctxFor(testTenant), ports.Notification{
		TenantID: testTenant, Channel: "email", Target: "ops@example.com", Subject: "s", Body: "b", CreatedAt: testNow,
	}))

	sent, failed, err := dp.SendPending(ctxFor(testTenant), "worker-1", 10)
	require.NoError(t, err)
	assert.Equal(t, 1, sent)
	assert.Equal(t, 0, failed)
	assert.Equal(t, 1, notifiers["email"].sentCount())

	page, _ := repos.Notifications.List(ctxFor(testTenant), testTenant, ports.ListOptions{})
	require.Len(t, page.Items, 1)
	assert.NotNil(t, page.Items[0].SentAt)
}

func TestSendPending_RetryableFailureStaysClaimableForNextSweep(t *testing.T) {
	repos, dp, notifiers := testStore(t)
	notifiers["email"].errAlways = errRetryableTest
	require.NoError(t, repos.Notifications.Enqueue(ctxFor(testTenant), ports.Notification{
		TenantID: testTenant, Channel: "email", Target: "ops@example.com", Subject: "s", Body: "b", CreatedAt: testNow,
	}))

	sent, failed, err := dp.SendPending(ctxFor(testTenant), "worker-1", 10)
	require.NoError(t, err)
	assert.Equal(t, 0, sent)
	assert.Equal(t, 1, failed)

	page, _ := repos.Notifications.List(ctxFor(testTenant), testTenant, ports.ListOptions{})
	require.Len(t, page.Items, 1)
	assert.Nil(t, page.Items[0].SentAt)
	assert.Empty(t, page.Items[0].Error, "a retryable failure must not be marked terminally failed, so the next sweep can reclaim it")

	// Prove it really is reclaimed: a second sweep attempts it again.
	notifiers["email"].errAlways = nil
	sent, failed, err = dp.SendPending(ctxFor(testTenant), "worker-1", 10)
	require.NoError(t, err)
	assert.Equal(t, 1, sent)
	assert.Equal(t, 0, failed)
	assert.Equal(t, 2, notifiers["email"].sentCount(), "the notification must have been retried, not abandoned")
}

func TestSendPending_NonRetryableFailureIsMarkedTerminal(t *testing.T) {
	repos, dp, notifiers := testStore(t)
	notifiers["email"].errAlways = errPermanentTest
	require.NoError(t, repos.Notifications.Enqueue(ctxFor(testTenant), ports.Notification{
		TenantID: testTenant, Channel: "email", Target: "ops@example.com", Subject: "s", Body: "b", CreatedAt: testNow,
	}))

	sent, failed, err := dp.SendPending(ctxFor(testTenant), "worker-1", 10)
	require.NoError(t, err)
	assert.Equal(t, 0, sent)
	assert.Equal(t, 1, failed)

	page, _ := repos.Notifications.List(ctxFor(testTenant), testTenant, ports.ListOptions{})
	require.Len(t, page.Items, 1)
	assert.Contains(t, page.Items[0].Error, "permanently rejected")

	// A non-retryable failure must not be reclaimed by a later sweep.
	sent, failed, err = dp.SendPending(ctxFor(testTenant), "worker-1", 10)
	require.NoError(t, err)
	assert.Equal(t, 0, sent)
	assert.Equal(t, 0, failed)
	assert.Equal(t, 1, notifiers["email"].sentCount(), "a terminally failed notification must never be retried")
}

func TestSendPending_UnknownChannelTypeIsMarkedTerminal(t *testing.T) {
	repos, dp, _ := testStore(t)
	require.NoError(t, repos.Notifications.Enqueue(ctxFor(testTenant), ports.Notification{
		TenantID: testTenant, Channel: "carrier-pigeon", Target: "loft-1", Subject: "s", Body: "b", CreatedAt: testNow,
	}))

	sent, failed, err := dp.SendPending(ctxFor(testTenant), "worker-1", 10)
	require.NoError(t, err)
	assert.Equal(t, 0, sent)
	assert.Equal(t, 1, failed)

	page, _ := repos.Notifications.List(ctxFor(testTenant), testTenant, ports.ListOptions{})
	require.Len(t, page.Items, 1)
	assert.Contains(t, page.Items[0].Error, "no notifier registered")
}
