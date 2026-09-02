package middleware

import (
	"context"
	"sync"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// breakerState is the classic three-state circuit breaker.
type breakerState int

const (
	breakerClosed breakerState = iota
	breakerOpen
	breakerHalfOpen
)

// CircuitBreakerConfig tunes when the breaker trips and how long it stays
// open before allowing a trial request through.
type CircuitBreakerConfig struct {
	// FailureThreshold consecutive failures trip the breaker from closed to
	// open.
	FailureThreshold int
	// OpenDuration is how long the breaker stays open before moving to
	// half-open and allowing exactly one trial call through.
	OpenDuration time.Duration
	Clock        func() time.Time
}

// DefaultCircuitBreakerConfig trips after 5 consecutive failures and stays
// open for 30 seconds — long enough that a provider outage does not get
// hammered by retries stacked on top of retries, short enough that a
// transient blip recovers within one user-visible request cycle.
func DefaultCircuitBreakerConfig() CircuitBreakerConfig {
	return CircuitBreakerConfig{FailureThreshold: 5, OpenDuration: 30 * time.Second}
}

// CircuitBreakerProvider wraps a ports.LLMProvider so a struggling provider
// fails fast instead of every caller separately waiting out its own timeout.
// This is what makes internal/adapters/llm/fallback's degrade-on-unhealthy
// behaviour cheap: once the breaker trips, Healthy and Complete both return
// immediately without attempting the network call at all.
type CircuitBreakerProvider struct {
	inner ports.LLMProvider
	cfg   CircuitBreakerConfig

	mu               sync.Mutex
	state            breakerState
	consecutiveFails int
	openedAt         time.Time
}

var _ ports.LLMProvider = (*CircuitBreakerProvider)(nil)

// NewCircuitBreakerProvider wraps inner with cfg.
func NewCircuitBreakerProvider(inner ports.LLMProvider, cfg CircuitBreakerConfig) *CircuitBreakerProvider {
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = DefaultCircuitBreakerConfig().FailureThreshold
	}
	if cfg.OpenDuration <= 0 {
		cfg.OpenDuration = DefaultCircuitBreakerConfig().OpenDuration
	}
	if cfg.Clock == nil {
		cfg.Clock = func() time.Time { return time.Now().UTC() }
	}
	return &CircuitBreakerProvider{inner: inner, cfg: cfg}
}

func (c *CircuitBreakerProvider) Name() string { return c.inner.Name() }

// allow decides whether a call may proceed, transitioning open -> half-open
// once OpenDuration has elapsed. Only one caller is let through per
// half-open window: allow returns (true, true) for the trial call and
// (false, ...) for anyone else who arrives before that trial resolves,
// which is what stops a burst of concurrent requests from all becoming
// trial calls against a provider that is still down.
func (c *CircuitBreakerProvider) allow() (proceed bool, isTrial bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch c.state {
	case breakerClosed:
		return true, false
	case breakerOpen:
		if c.cfg.Clock().Sub(c.openedAt) >= c.cfg.OpenDuration {
			c.state = breakerHalfOpen
			return true, true
		}
		return false, false
	case breakerHalfOpen:
		// A trial is already in flight; everyone else waits for its result.
		return false, false
	}
	return true, false
}

func (c *CircuitBreakerProvider) recordSuccess() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state = breakerClosed
	c.consecutiveFails = 0
}

func (c *CircuitBreakerProvider) recordFailure() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state == breakerHalfOpen {
		// The trial call failed: back to fully open for another full window.
		c.state = breakerOpen
		c.openedAt = c.cfg.Clock()
		return
	}
	c.consecutiveFails++
	if c.consecutiveFails >= c.cfg.FailureThreshold {
		c.state = breakerOpen
		c.openedAt = c.cfg.Clock()
	}
}

func (c *CircuitBreakerProvider) Complete(ctx context.Context, req ports.CompletionRequest) (ports.CompletionResponse, error) {
	proceed, _ := c.allow()
	if !proceed {
		return ports.CompletionResponse{}, core.NewError(core.ErrUnavailable, "llm_circuit_open",
			"circuit breaker open for provider %s: too many consecutive failures", c.inner.Name())
	}
	resp, err := c.inner.Complete(ctx, req)
	if err != nil {
		c.recordFailure()
		return resp, err
	}
	c.recordSuccess()
	return resp, nil
}

func (c *CircuitBreakerProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	proceed, _ := c.allow()
	if !proceed {
		return nil, core.NewError(core.ErrUnavailable, "llm_circuit_open",
			"circuit breaker open for provider %s", c.inner.Name())
	}
	vecs, err := c.inner.Embed(ctx, texts)
	if err != nil {
		c.recordFailure()
		return vecs, err
	}
	c.recordSuccess()
	return vecs, nil
}

// Healthy reports the breaker's own view first: an open breaker is
// unhealthy by definition, without spending a network call to confirm what
// the last several calls already established.
func (c *CircuitBreakerProvider) Healthy(ctx context.Context) bool {
	c.mu.Lock()
	state := c.state
	c.mu.Unlock()
	if state == breakerOpen {
		return false
	}
	return c.inner.Healthy(ctx)
}
