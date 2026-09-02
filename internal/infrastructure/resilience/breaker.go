package resilience

import (
	"context"
	"errors"
	"sync"
	"time"
)

// State is a circuit breaker's current mode.
type State int

const (
	// StateClosed passes every call through and counts consecutive failures.
	StateClosed State = iota
	// StateOpen rejects every call immediately without invoking the
	// downstream dependency at all, which is the whole point: a failing
	// dependency under retry pressure from every caller only gets worse.
	StateOpen
	// StateHalfOpen allows a bounded number of probe calls through to test
	// whether the dependency has recovered, without yet trusting it with full
	// traffic.
	StateHalfOpen
)

// String renders the state for logs and metrics labels.
func (s State) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half_open"
	default:
		return "unknown"
	}
}

// ErrBreakerOpen is returned by Execute/Allow when the breaker is open (or
// half-open with no probe slots free).
var ErrBreakerOpen = errors.New("resilience: circuit breaker open")

// BreakerConfig configures a Breaker.
type BreakerConfig struct {
	// FailureThreshold is the number of consecutive failures in the Closed
	// state that trips the breaker to Open.
	FailureThreshold int
	// SuccessThreshold is the number of consecutive successful probes in
	// HalfOpen required to close the breaker again.
	SuccessThreshold int
	// OpenDuration is how long the breaker stays Open before allowing a probe
	// through in HalfOpen.
	OpenDuration time.Duration
	// HalfOpenMaxInFlight bounds how many concurrent probes HalfOpen allows.
	// A single-probe breaker (the default when this is <=0) is the safest
	// choice for a dependency that is expensive to hammer while recovering.
	HalfOpenMaxInFlight int
	// OnStateChange, if set, is invoked (outside the breaker's lock) whenever
	// the state transitions. Wired to a metrics counter and a log line by the
	// telemetry package's instrumentation helpers.
	OnStateChange func(from, to State)
	// Now is injectable for deterministic tests.
	Now func() time.Time
}

func (c BreakerConfig) withDefaults() BreakerConfig {
	if c.FailureThreshold <= 0 {
		c.FailureThreshold = 5
	}
	if c.SuccessThreshold <= 0 {
		c.SuccessThreshold = 2
	}
	if c.OpenDuration <= 0 {
		c.OpenDuration = 30 * time.Second
	}
	if c.HalfOpenMaxInFlight <= 0 {
		c.HalfOpenMaxInFlight = 1
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// Breaker is a concurrency-safe circuit breaker over an arbitrary dependency.
// Wrap it around one logical dependency (one AWS service, one LLM provider) —
// wrapping a whole adapter that talks to twenty AWS services behind one
// breaker means one throttled service trips the breaker for the other
// nineteen.
type Breaker struct {
	cfg BreakerConfig

	mu               sync.Mutex
	state            State
	consecutiveFails int
	consecutiveOK    int
	openedAt         time.Time
	halfOpenInFlight int
}

// NewBreaker constructs a Breaker in the Closed state.
func NewBreaker(cfg BreakerConfig) *Breaker {
	return &Breaker{cfg: cfg.withDefaults(), state: StateClosed}
}

// State reports the current state.
func (b *Breaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

// Allow reports whether a call may proceed right now. On success it returns a
// done function the caller MUST invoke exactly once with the call's outcome;
// on rejection it returns ErrBreakerOpen and a nil done function.
//
// Splitting Allow/done (rather than a single Execute(fn)) lets a caller wrap
// a call that itself needs the context for cancellation, streaming, or a
// timeout — Execute is provided below as sugar over this for the common case.
func (b *Breaker) Allow() (done func(success bool), err error) {
	b.mu.Lock()
	now := b.cfg.Now()
	var changed bool
	var from, to State

	switch b.state {
	case StateOpen:
		if now.Sub(b.openedAt) < b.cfg.OpenDuration {
			b.mu.Unlock()
			return nil, ErrBreakerOpen
		}
		// The open window has elapsed: admit one probe by moving to HalfOpen.
		from, to, changed = b.transition(StateHalfOpen)
		fallthrough
	case StateHalfOpen:
		if b.halfOpenInFlight >= b.cfg.HalfOpenMaxInFlight {
			b.mu.Unlock()
			if changed {
				b.notify(from, to)
			}
			return nil, ErrBreakerOpen
		}
		b.halfOpenInFlight++
	case StateClosed:
		// falls through to admit
	}
	b.mu.Unlock()
	if changed {
		b.notify(from, to)
	}

	return func(success bool) { b.report(success) }, nil
}

func (b *Breaker) report(success bool) {
	b.mu.Lock()
	var changed bool
	var from, to State

	switch b.state {
	case StateHalfOpen:
		b.halfOpenInFlight--
		if success {
			b.consecutiveOK++
			if b.consecutiveOK >= b.cfg.SuccessThreshold {
				from, to, changed = b.transition(StateClosed)
			}
		} else {
			// Any probe failure in HalfOpen means the dependency has not
			// recovered; go straight back to Open rather than counting
			// toward FailureThreshold again.
			from, to, changed = b.transition(StateOpen)
		}
	case StateClosed:
		if success {
			b.consecutiveFails = 0
		} else {
			b.consecutiveFails++
			if b.consecutiveFails >= b.cfg.FailureThreshold {
				from, to, changed = b.transition(StateOpen)
			}
		}
	case StateOpen:
		// A report arriving after the breaker already reopened (a slow probe
		// racing a fresh Open->HalfOpen transition) is stale; ignore it.
	}
	b.mu.Unlock()
	if changed {
		b.notify(from, to)
	}
}

// transition must be called with mu held. It returns the edge so the caller
// can notify OnStateChange after releasing the lock — a callback that blocks
// (writing a metric, appending a log line) must never stall the call path
// that every other goroutine using this breaker is waiting on.
func (b *Breaker) transition(to State) (from, newState State, changed bool) {
	from = b.state
	if from == to {
		return from, to, false
	}
	b.state = to
	switch to {
	case StateOpen:
		b.openedAt = b.cfg.Now()
		b.consecutiveOK = 0
		b.halfOpenInFlight = 0
	case StateHalfOpen:
		b.consecutiveOK = 0
		b.halfOpenInFlight = 0
	case StateClosed:
		b.consecutiveFails = 0
		b.consecutiveOK = 0
	}
	return from, to, true
}

func (b *Breaker) notify(from, to State) {
	if b.cfg.OnStateChange != nil {
		b.cfg.OnStateChange(from, to)
	}
}

// Execute is sugar over Allow/done for the common case of wrapping a single
// function call.
func (b *Breaker) Execute(ctx context.Context, fn func(ctx context.Context) error) error {
	done, err := b.Allow()
	if err != nil {
		return err
	}
	callErr := fn(ctx)
	done(callErr == nil)
	return callErr
}
