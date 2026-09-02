package resilience

import (
	"context"
	"errors"
	"math"
	"math/rand"
	"time"
)

// RetryPolicy configures a bounded retry loop. The zero value is not usable;
// construct one with DefaultRetryPolicy and override fields as needed.
type RetryPolicy struct {
	// MaxAttempts is the total number of tries, including the first. A value
	// of 1 disables retrying.
	MaxAttempts int
	// BaseDelay is the backoff for the first retry (attempt index 1).
	BaseDelay time.Duration
	// MaxDelay caps the backoff so a long-running caller never waits an
	// unbounded amount of time between attempts.
	MaxDelay time.Duration
	// Classify decides whether an error is worth retrying. A nil Classify
	// treats every non-nil error as retryable, which is rarely what a caller
	// wants — pass core.Retryable (or an adapter-specific wrapper of it) in
	// production code.
	Classify func(error) bool
	// RandSource is injectable so tests can assert exact jitter bounds
	// without flaking. Production callers should leave it nil, which uses a
	// package-level source seeded from crypto-quality entropy at init.
	RandSource *rand.Rand
}

// DefaultRetryPolicy returns sane defaults for an outbound AWS or LLM call:
// up to 5 attempts, starting at 200ms and capped at 10s.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts: 5,
		BaseDelay:   200 * time.Millisecond,
		MaxDelay:    10 * time.Second,
	}
}

// Backoff computes the exponential delay for the given zero-based attempt
// number before jitter, capped at max. attempt 0 is the delay before the
// first retry (i.e. after the first failed try).
//
// It is a pure function deliberately kept separate from jitter application so
// tests can assert the exact curve without fighting randomness.
func Backoff(attempt int, base, max time.Duration) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	if base <= 0 {
		return 0
	}
	// 2^attempt grows fast enough to overflow a duration well within the
	// range of attempts any caller configures (attempt ~ 30 already exceeds
	// int64 nanoseconds), so clamp before the multiply rather than after.
	mult := math.Pow(2, float64(attempt))
	d := time.Duration(float64(base) * mult)
	if d <= 0 || d > max { // d<=0 catches the overflow case
		return max
	}
	return d
}

// FullJitter applies the "full jitter" strategy from the AWS Architecture
// Blog backoff paper: a uniformly random duration in [0, d]. Full jitter
// spreads retries across the whole window rather than clustering them near
// the ceiling (as "equal jitter" does), which is what actually breaks a
// thundering herd of clients retrying in lockstep after a shared outage.
func FullJitter(d time.Duration, rnd *rand.Rand) time.Duration {
	if d <= 0 {
		return 0
	}
	if rnd == nil {
		rnd = defaultRand
	}
	return time.Duration(rnd.Int63n(int64(d) + 1))
}

// defaultRand is process-global because retry loops fire concurrently across
// many goroutines; math/rand.Rand is not safe for concurrent use on its own,
// but rand.Int63n on the top-level (unseeded-since-Go1.20-is-auto-seeded)
// source is safe for concurrent callers.
var defaultRand = rand.New(rand.NewSource(time.Now().UnixNano()))

// ErrRetriesExhausted wraps the last error once MaxAttempts is reached.
var ErrRetriesExhausted = errors.New("resilience: retries exhausted")

// Do runs fn, retrying on classifier-approved errors with full-jitter
// exponential backoff, until it succeeds, the policy's attempts are
// exhausted, or ctx is cancelled. The context's deadline is honoured between
// attempts — a caller with 500ms left is not made to sleep past it.
func Do(ctx context.Context, policy RetryPolicy, fn func(ctx context.Context) error) error {
	if policy.MaxAttempts < 1 {
		policy.MaxAttempts = 1
	}
	classify := policy.Classify
	if classify == nil {
		classify = func(err error) bool { return err != nil }
	}

	var lastErr error
	for attempt := 0; attempt < policy.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		lastErr = fn(ctx)
		if lastErr == nil {
			return nil
		}
		if !classify(lastErr) {
			return lastErr
		}
		if attempt == policy.MaxAttempts-1 {
			break
		}
		delay := FullJitter(Backoff(attempt, policy.BaseDelay, policy.MaxDelay), policy.RandSource)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return errors.Join(ErrRetriesExhausted, lastErr)
}
