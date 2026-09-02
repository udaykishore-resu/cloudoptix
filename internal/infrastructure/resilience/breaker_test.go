package resilience

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeClock lets the breaker tests advance time deterministically instead of
// sleeping, which is what keeps this suite fast and non-flaky.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Unix(0, 0)} }
func (f *fakeClock) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}
func (f *fakeClock) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = f.t.Add(d)
}

func TestBreaker_StartsClosed(t *testing.T) {
	b := NewBreaker(BreakerConfig{})
	assert.Equal(t, StateClosed, b.State())
}

func TestBreaker_OpensAfterConsecutiveFailures(t *testing.T) {
	var transitions []string
	b := NewBreaker(BreakerConfig{
		FailureThreshold: 3,
		OnStateChange:    func(from, to State) { transitions = append(transitions, from.String()+"->"+to.String()) },
	})

	for i := 0; i < 3; i++ {
		done, err := b.Allow()
		require.NoError(t, err)
		done(false)
	}

	assert.Equal(t, StateOpen, b.State())
	assert.Equal(t, []string{"closed->open"}, transitions)
}

func TestBreaker_ClosedStateResetsOnSuccess(t *testing.T) {
	b := NewBreaker(BreakerConfig{FailureThreshold: 3})

	// Two failures, then a success, then two more failures: the breaker must
	// not open, because the success reset the consecutive-failure counter.
	fail(t, b)
	fail(t, b)
	succeed(t, b)
	fail(t, b)
	fail(t, b)

	assert.Equal(t, StateClosed, b.State())
}

func TestBreaker_RejectsCallsWhileOpen(t *testing.T) {
	clock := newFakeClock()
	b := NewBreaker(BreakerConfig{
		FailureThreshold: 1,
		OpenDuration:     time.Minute,
		Now:              clock.now,
	})
	fail(t, b)
	require.Equal(t, StateOpen, b.State())

	_, err := b.Allow()
	assert.ErrorIs(t, err, ErrBreakerOpen)
}

func TestBreaker_TransitionsToHalfOpenAfterOpenDuration(t *testing.T) {
	clock := newFakeClock()
	b := NewBreaker(BreakerConfig{
		FailureThreshold: 1,
		OpenDuration:     10 * time.Second,
		Now:              clock.now,
	})
	fail(t, b)
	require.Equal(t, StateOpen, b.State())

	clock.advance(9 * time.Second)
	_, err := b.Allow()
	assert.ErrorIs(t, err, ErrBreakerOpen, "should still be open before OpenDuration elapses")

	clock.advance(2 * time.Second) // total 11s > 10s OpenDuration
	done, err := b.Allow()
	require.NoError(t, err, "should admit exactly one probe once OpenDuration has elapsed")
	assert.Equal(t, StateHalfOpen, b.State())
	done(true)
}

func TestBreaker_HalfOpenClosesAfterSuccessThreshold(t *testing.T) {
	clock := newFakeClock()
	b := NewBreaker(BreakerConfig{
		FailureThreshold:    1,
		SuccessThreshold:    2,
		OpenDuration:        time.Second,
		HalfOpenMaxInFlight: 1,
		Now:                 clock.now,
	})
	fail(t, b) // -> open
	clock.advance(2 * time.Second)

	succeed(t, b) // probe 1 -> still half-open
	assert.Equal(t, StateHalfOpen, b.State())

	succeed(t, b) // probe 2 -> closes
	assert.Equal(t, StateClosed, b.State())
}

func TestBreaker_HalfOpenFailureReopens(t *testing.T) {
	clock := newFakeClock()
	b := NewBreaker(BreakerConfig{
		FailureThreshold: 1,
		SuccessThreshold: 2,
		OpenDuration:     time.Second,
		Now:              clock.now,
	})
	fail(t, b) // -> open
	clock.advance(2 * time.Second)

	fail(t, b) // probe fails -> back to open immediately, not after a second threshold
	assert.Equal(t, StateOpen, b.State())

	// And it must wait a fresh OpenDuration from the reopen, not the
	// original open time.
	_, err := b.Allow()
	assert.ErrorIs(t, err, ErrBreakerOpen)
}

func TestBreaker_HalfOpenLimitsInFlightProbes(t *testing.T) {
	clock := newFakeClock()
	b := NewBreaker(BreakerConfig{
		FailureThreshold:    1,
		HalfOpenMaxInFlight: 1,
		OpenDuration:        time.Second,
		Now:                 clock.now,
	})
	fail(t, b) // -> open
	clock.advance(2 * time.Second)

	done1, err := b.Allow()
	require.NoError(t, err)
	assert.Equal(t, StateHalfOpen, b.State())

	// A second concurrent probe must be rejected: HalfOpenMaxInFlight is 1.
	_, err = b.Allow()
	assert.ErrorIs(t, err, ErrBreakerOpen)

	done1(true)
}

func TestBreaker_Execute(t *testing.T) {
	b := NewBreaker(BreakerConfig{FailureThreshold: 1})
	err := b.Execute(context.Background(), func(ctx context.Context) error { return nil })
	require.NoError(t, err)
	assert.Equal(t, StateClosed, b.State())

	sentinel := errors.New("boom")
	err = b.Execute(context.Background(), func(ctx context.Context) error { return sentinel })
	assert.ErrorIs(t, err, sentinel)
	assert.Equal(t, StateOpen, b.State())

	// Execute must not even invoke fn while open.
	called := false
	err = b.Execute(context.Background(), func(ctx context.Context) error { called = true; return nil })
	assert.ErrorIs(t, err, ErrBreakerOpen)
	assert.False(t, called)
}

func TestBreaker_ConcurrentUse(t *testing.T) {
	b := NewBreaker(BreakerConfig{FailureThreshold: 1000000}) // effectively never opens under this test
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			done, err := b.Allow()
			if err != nil {
				return
			}
			done(i%2 == 0)
		}(i)
	}
	wg.Wait()
	// No assertion beyond "the race detector found nothing and nothing
	// panicked" — this test exists to be run with -race in CI.
}

func fail(t *testing.T, b *Breaker) {
	t.Helper()
	done, err := b.Allow()
	require.NoError(t, err)
	done(false)
}

func succeed(t *testing.T, b *Breaker) {
	t.Helper()
	done, err := b.Allow()
	require.NoError(t, err)
	done(true)
}
