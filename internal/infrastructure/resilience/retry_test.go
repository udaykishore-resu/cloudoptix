package resilience

import (
	"context"
	"errors"
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBackoff_Bounds(t *testing.T) {
	base := 100 * time.Millisecond
	max := 2 * time.Second

	cases := []struct {
		name    string
		attempt int
		want    time.Duration
	}{
		{"attempt 0 is exactly base", 0, 100 * time.Millisecond},
		{"attempt 1 doubles", 1, 200 * time.Millisecond},
		{"attempt 2 doubles again", 2, 400 * time.Millisecond},
		{"attempt 3 doubles again", 3, 800 * time.Millisecond},
		{"attempt 4 is still under max", 4, 1600 * time.Millisecond},
		{"attempt 5 would exceed max and is clamped", 5, max},
		{"very large attempt does not overflow and clamps to max", 62, max},
		{"negative attempt treated as zero", -5, 100 * time.Millisecond},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Backoff(tc.attempt, base, max)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestBackoff_NeverExceedsMax(t *testing.T) {
	base := 50 * time.Millisecond
	max := time.Second
	for attempt := 0; attempt < 100; attempt++ {
		d := Backoff(attempt, base, max)
		assert.LessOrEqualf(t, d, max, "attempt %d produced %v > max %v", attempt, d, max)
		assert.GreaterOrEqual(t, d, time.Duration(0))
	}
}

func TestFullJitter_WithinWindow(t *testing.T) {
	rnd := rand.New(rand.NewSource(1))
	d := 500 * time.Millisecond
	for i := 0; i < 1000; i++ {
		got := FullJitter(d, rnd)
		assert.GreaterOrEqual(t, got, time.Duration(0))
		assert.LessOrEqual(t, got, d)
	}
}

func TestFullJitter_ZeroDelay(t *testing.T) {
	assert.Equal(t, time.Duration(0), FullJitter(0, nil))
}

func TestFullJitter_Distributes(t *testing.T) {
	// A regression guard against "jitter" that is actually a constant: over
	// many draws from a wide window we expect to see values both in the
	// bottom and top third, not clustered at one end (which full jitter,
	// unlike "equal jitter", should never do).
	rnd := rand.New(rand.NewSource(42))
	d := 900 * time.Millisecond
	var sawLow, sawHigh bool
	for i := 0; i < 500; i++ {
		got := FullJitter(d, rnd)
		if got < d/3 {
			sawLow = true
		}
		if got > d*2/3 {
			sawHigh = true
		}
	}
	assert.True(t, sawLow, "expected some draws in the bottom third of the window")
	assert.True(t, sawHigh, "expected some draws in the top third of the window")
}

func TestDo_SucceedsWithoutRetry(t *testing.T) {
	calls := 0
	err := Do(context.Background(), DefaultRetryPolicy(), func(ctx context.Context) error {
		calls++
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 1, calls)
}

func TestDo_RetriesRetryableThenSucceeds(t *testing.T) {
	sentinel := errors.New("throttled")
	calls := 0
	policy := RetryPolicy{
		MaxAttempts: 5,
		BaseDelay:   time.Millisecond,
		MaxDelay:    5 * time.Millisecond,
		Classify:    func(err error) bool { return errors.Is(err, sentinel) },
	}
	err := Do(context.Background(), policy, func(ctx context.Context) error {
		calls++
		if calls < 3 {
			return sentinel
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 3, calls)
}

func TestDo_NonRetryableFailsImmediately(t *testing.T) {
	sentinel := errors.New("invalid input")
	calls := 0
	policy := RetryPolicy{
		MaxAttempts: 5,
		BaseDelay:   time.Millisecond,
		MaxDelay:    5 * time.Millisecond,
		Classify:    func(err error) bool { return false },
	}
	err := Do(context.Background(), policy, func(ctx context.Context) error {
		calls++
		return sentinel
	})
	require.ErrorIs(t, err, sentinel)
	assert.Equal(t, 1, calls)
}

func TestDo_ExhaustsAttempts(t *testing.T) {
	sentinel := errors.New("still throttled")
	calls := 0
	policy := RetryPolicy{
		MaxAttempts: 3,
		BaseDelay:   time.Millisecond,
		MaxDelay:    5 * time.Millisecond,
		Classify:    func(err error) bool { return true },
	}
	err := Do(context.Background(), policy, func(ctx context.Context) error {
		calls++
		return sentinel
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRetriesExhausted)
	assert.ErrorIs(t, err, sentinel)
	assert.Equal(t, 3, calls)
}

func TestDo_ContextCancelledStopsRetrying(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	policy := RetryPolicy{
		MaxAttempts: 10,
		BaseDelay:   50 * time.Millisecond,
		MaxDelay:    time.Second,
		Classify:    func(err error) bool { return true },
	}
	err := Do(ctx, policy, func(ctx context.Context) error {
		calls++
		if calls == 1 {
			cancel()
		}
		return errors.New("nope")
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, calls)
}
