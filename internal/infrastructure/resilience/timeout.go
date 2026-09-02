package resilience

import (
	"context"
	"time"
)

// WithBudget returns a context whose deadline is min(ctx's existing deadline,
// now+budget). It never extends a deadline the caller already has — an
// AWS discoverer given 30 remaining seconds by the request that triggered it
// cannot hand its CloudWatch calls a fresh 60 seconds; that just moves the
// point where the client gives up from "this call" to "the whole request,
// later and more confusingly".
//
// This is the deadline-propagation primitive the AWS and LLM adapters use
// before every outbound call: WithBudget(ctx, perCallTimeout) rather than
// context.WithTimeout(ctx, perCallTimeout) directly, so a chain of N
// sequential calls each with their own timeout cannot collectively overrun
// the caller's actual budget.
func WithBudget(ctx context.Context, budget time.Duration) (context.Context, context.CancelFunc) {
	proposed := time.Now().Add(budget)
	if existing, ok := ctx.Deadline(); ok && existing.Before(proposed) {
		// The caller's deadline is already tighter than what we'd propose;
		// context.WithDeadline still returns a fresh cancel func, which is
		// what every caller needs regardless.
		return context.WithDeadline(ctx, existing)
	}
	return context.WithDeadline(ctx, proposed)
}

// Remaining reports how much time is left on ctx's deadline, or ok=false when
// the context carries no deadline at all. Used by handlers deciding whether
// there is enough budget left to attempt one more retry rather than fail fast
// with a clearer error than "context deadline exceeded" one layer down.
func Remaining(ctx context.Context) (d time.Duration, ok bool) {
	deadline, has := ctx.Deadline()
	if !has {
		return 0, false
	}
	return time.Until(deadline), true
}

// Timeout runs fn in the current goroutine with ctx bounded by d (via
// WithBudget), returning fn's error or ctx.Err() if the deadline is hit
// first. Because fn is not cancellation-aware on its own in general (a
// blocking library call has no way to observe context cancellation), Timeout
// runs fn in a separate goroutine and returns as soon as either finishes —
// the caller gets a timely result, at the cost of the abandoned goroutine
// running to completion in the background. Preferring a real context-aware
// call (an *http.Request built with ctx, an AWS SDK call, which are both
// cancellation-aware) over this wrapper is always better when available;
// Timeout exists for the handful of call sites that have no such option.
func Timeout(ctx context.Context, d time.Duration, fn func(ctx context.Context) error) error {
	cctx, cancel := WithBudget(ctx, d)
	defer cancel()

	result := make(chan error, 1)
	go func() {
		result <- fn(cctx)
	}()

	select {
	case err := <-result:
		return err
	case <-cctx.Done():
		return cctx.Err()
	}
}
