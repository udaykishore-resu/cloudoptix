package events

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

func testBus(t *testing.T) *InProcess {
	t.Helper()
	bus := New(WithWorkers(2), WithRetry(3, time.Millisecond, 10*time.Millisecond))
	t.Cleanup(bus.Close)
	return bus
}

func TestPublish_RefusesEventWithNoTenant(t *testing.T) {
	bus := testBus(t)
	err := bus.Publish(context.Background(), ports.Event{Type: ports.EventCostUpdated})
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrInvalidInput)
}

func TestPublish_DeliversAtLeastOnceAndPreservesTenantScoping(t *testing.T) {
	bus := testBus(t)
	var got ports.Event
	var mu sync.Mutex
	done := make(chan struct{})
	require.NoError(t, bus.Subscribe(context.Background(), []ports.EventType{ports.EventCostUpdated}, func(_ context.Context, e ports.Event) error {
		mu.Lock()
		got = e
		mu.Unlock()
		close(done)
		return nil
	}))

	require.NoError(t, bus.Publish(context.Background(), ports.Event{
		Type: ports.EventCostUpdated, TenantID: "tenant-42", SubjectID: core.NewID("res"),
	}))

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler was never invoked")
	}
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, core.TenantID("tenant-42"), got.TenantID)
	assert.False(t, got.ID == "", "Publish must assign an id when the caller did not supply one")
}

func TestSubscribe_TypeFilterOnlyDeliversMatchingEvents(t *testing.T) {
	bus := testBus(t)
	var calls int32
	require.NoError(t, bus.Subscribe(context.Background(), []ports.EventType{ports.EventCostUpdated}, func(context.Context, ports.Event) error {
		atomic.AddInt32(&calls, 1)
		return nil
	}))

	require.NoError(t, bus.Publish(context.Background(), ports.Event{Type: ports.EventSpecApproved, TenantID: "t1"}))
	bus.Wait()
	assert.Equal(t, int32(0), atomic.LoadInt32(&calls), "a subscriber must not be invoked for a type it did not subscribe to")
}

func TestDeliver_RetriesOnFailureThenSucceeds(t *testing.T) {
	bus := testBus(t)
	var attempts int32
	require.NoError(t, bus.Subscribe(context.Background(), nil, func(context.Context, ports.Event) error {
		n := atomic.AddInt32(&attempts, 1)
		if n < 3 {
			return errors.New("transient failure")
		}
		return nil
	}))
	require.NoError(t, bus.Publish(context.Background(), ports.Event{Type: ports.EventCostUpdated, TenantID: "t1"}))
	bus.Wait()
	assert.Equal(t, int32(3), atomic.LoadInt32(&attempts))
	assert.Empty(t, bus.DeadLetters(), "an eventually-successful handler must not be dead-lettered")
}

func TestDeliver_ExhaustedRetriesGoToDeadLetter(t *testing.T) {
	bus := testBus(t)
	var attempts int32
	require.NoError(t, bus.Subscribe(context.Background(), nil, func(context.Context, ports.Event) error {
		atomic.AddInt32(&attempts, 1)
		return errors.New("permanent failure")
	}))
	require.NoError(t, bus.Publish(context.Background(), ports.Event{Type: ports.EventCostUpdated, TenantID: "t1", ID: "evt-fixed"}))
	bus.Wait()

	assert.Equal(t, int32(3), atomic.LoadInt32(&attempts), "maxAttempts is 3 in this test bus")
	dl := bus.DeadLetters()
	require.Len(t, dl, 1)
	assert.Equal(t, "evt-fixed", dl[0].Event.ID)
	assert.Equal(t, 3, dl[0].Attempts)
	assert.Contains(t, dl[0].Err, "permanent failure")
}

func TestSubscribe_IndependentSubscribersDoNotAffectEachOther(t *testing.T) {
	bus := testBus(t)
	var okCalls, failCalls int32
	require.NoError(t, bus.Subscribe(context.Background(), nil, func(context.Context, ports.Event) error {
		atomic.AddInt32(&okCalls, 1)
		return nil
	}))
	require.NoError(t, bus.Subscribe(context.Background(), nil, func(context.Context, ports.Event) error {
		atomic.AddInt32(&failCalls, 1)
		return errors.New("this subscriber always fails")
	}))
	require.NoError(t, bus.Publish(context.Background(), ports.Event{Type: ports.EventCostUpdated, TenantID: "t1"}))
	bus.Wait()

	assert.Equal(t, int32(1), atomic.LoadInt32(&okCalls), "the healthy subscriber must be delivered to exactly once")
	assert.Equal(t, int32(3), atomic.LoadInt32(&failCalls), "the failing subscriber's own retries must not touch the healthy one")
	assert.Len(t, bus.DeadLetters(), 1)
}
