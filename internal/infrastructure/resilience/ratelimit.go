package resilience

import (
	"context"
	"sync"
	"time"
)

// TokenBucket is a concurrency-safe token-bucket rate limiter. It is used
// both server-side (per-tenant API rate limiting in the transport middleware)
// and client-side (capping the rate of outbound AWS/LLM calls so CloudOptix
// itself never becomes the source of the throttling its retry logic then has
// to absorb).
//
// A token bucket rather than a fixed window is deliberate: a fixed window
// (e.g. "1000 requests per minute, reset on the minute") allows a caller to
// burst 2000 requests across a window boundary — 1000 in the last second of
// one window and 1000 in the first second of the next. The bucket smooths
// that out by refilling continuously.
type TokenBucket struct {
	mu           sync.Mutex
	capacity     float64
	tokens       float64
	refillPerSec float64
	last         time.Time
	now          func() time.Time
}

// NewTokenBucket builds a bucket that refills at ratePerSecond and allows
// bursts up to burst tokens. The bucket starts full, so the first burst of
// traffic after startup is not artificially throttled.
func NewTokenBucket(ratePerSecond float64, burst int) *TokenBucket {
	if burst < 1 {
		burst = 1
	}
	return &TokenBucket{
		capacity:     float64(burst),
		tokens:       float64(burst),
		refillPerSec: ratePerSecond,
		last:         time.Now(),
		now:          time.Now,
	}
}

func (b *TokenBucket) refill() {
	now := b.now()
	elapsed := now.Sub(b.last).Seconds()
	if elapsed <= 0 {
		return
	}
	b.tokens += elapsed * b.refillPerSec
	if b.tokens > b.capacity {
		b.tokens = b.capacity
	}
	b.last = now
}

// Allow consumes one token if available and reports whether it did. It never
// blocks, which is what the HTTP middleware needs — a rate-limited request
// gets an immediate 429, not a stalled connection.
func (b *TokenBucket) Allow() bool { return b.AllowN(1) }

// AllowN consumes n tokens atomically — either all n are taken or none are.
func (b *TokenBucket) AllowN(n int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refill()
	need := float64(n)
	if b.tokens < need {
		return false
	}
	b.tokens -= need
	return true
}

// Remaining reports the current token count, rounded down, for surfacing in
// rate-limit response headers (X-RateLimit-Remaining).
func (b *TokenBucket) Remaining() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refill()
	return int(b.tokens)
}

// RetryAfter estimates how long until n tokens will be available, for the
// Retry-After header on a 429.
func (b *TokenBucket) RetryAfter(n int) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refill()
	need := float64(n) - b.tokens
	if need <= 0 || b.refillPerSec <= 0 {
		return 0
	}
	return time.Duration(need / b.refillPerSec * float64(time.Second))
}

// Wait blocks until a token is available or ctx is done. It is what the
// outbound AWS/LLM client-side limiter uses — an outbound call can afford to
// wait a beat; an inbound HTTP request cannot.
func (b *TokenBucket) Wait(ctx context.Context) error {
	for {
		if b.Allow() {
			return nil
		}
		wait := b.RetryAfter(1)
		if wait <= 0 {
			wait = time.Millisecond
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// KeyedLimiter manages one TokenBucket per key (per tenant, per AWS account,
// per LLM purpose) so that one noisy caller cannot starve the token budget of
// every other caller sharing a single process-wide limiter.
type KeyedLimiter struct {
	mu      sync.Mutex
	buckets map[string]*TokenBucket
	rate    float64
	burst   int
}

// NewKeyedLimiter builds a limiter that lazily creates a rate/burst bucket
// per distinct key on first use.
func NewKeyedLimiter(ratePerSecond float64, burst int) *KeyedLimiter {
	return &KeyedLimiter{buckets: make(map[string]*TokenBucket), rate: ratePerSecond, burst: burst}
}

// Bucket returns the bucket for key, creating it on first access.
func (k *KeyedLimiter) Bucket(key string) *TokenBucket {
	k.mu.Lock()
	defer k.mu.Unlock()
	b, ok := k.buckets[key]
	if !ok {
		b = NewTokenBucket(k.rate, k.burst)
		k.buckets[key] = b
	}
	return b
}

// Allow is sugar for Bucket(key).Allow().
func (k *KeyedLimiter) Allow(key string) bool { return k.Bucket(key).Allow() }
