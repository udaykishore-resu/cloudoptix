package middleware

import (
	"context"
	"sync"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// RateLimitConfig sizes the per-tenant limits RateLimitProvider enforces.
// Defaults apply to every tenant unless Override returns non-zero values for
// a specific one — the platform ships one sane default (see
// DefaultRateLimitConfig) and lets a caller wire tenancy.QuotasFor's
// MaxCopilotTokensPerDay in through Override without this package importing
// the tenancy package itself.
type RateLimitConfig struct {
	// RequestsPerMinute bounds call rate per tenant via a token bucket.
	RequestsPerMinute int
	// DailyTokenQuota bounds total input+output tokens per tenant per UTC
	// calendar day.
	DailyTokenQuota int64
	// Override, when non-nil, returns per-tenant limits; a zero return value
	// for either field falls back to the corresponding default above.
	Override func(tenant core.TenantID) (requestsPerMinute int, dailyTokenQuota int64)
	// Clock is injectable for deterministic tests; defaults to time.Now.
	Clock func() time.Time
}

// DefaultRateLimitConfig is the platform default: 60 requests/minute and 2M
// tokens/day per tenant, a generous ceiling in normal operation whose real
// job is to contain a runaway loop or a compromised credential, not to
// throttle legitimate use.
func DefaultRateLimitConfig() RateLimitConfig {
	return RateLimitConfig{RequestsPerMinute: 60, DailyTokenQuota: 2_000_000}
}

type tenantLimitState struct {
	mu sync.Mutex

	bucketInitialized bool
	tokens            float64 // request-rate token bucket
	lastRefill        time.Time

	dayKey        string
	tokensUsedDay int64
}

// RateLimitProvider wraps a ports.LLMProvider with per-tenant request-rate
// limiting (a token bucket) and a daily token quota. The quota check runs
// before the call (rejecting once today's consumption is already at or
// above quota) and is updated after a successful call with the tokens it
// actually used — the same soft-quota shape most production rate limiters
// use, which can let one in-flight call slightly overshoot the ceiling but
// never lets a tenant run unboundedly over it.
type RateLimitProvider struct {
	inner ports.LLMProvider
	cfg   RateLimitConfig

	mu     sync.Mutex
	states map[core.TenantID]*tenantLimitState
}

var _ ports.LLMProvider = (*RateLimitProvider)(nil)

// NewRateLimitProvider wraps inner with cfg's limits.
func NewRateLimitProvider(inner ports.LLMProvider, cfg RateLimitConfig) *RateLimitProvider {
	if cfg.RequestsPerMinute <= 0 {
		cfg.RequestsPerMinute = DefaultRateLimitConfig().RequestsPerMinute
	}
	if cfg.DailyTokenQuota <= 0 {
		cfg.DailyTokenQuota = DefaultRateLimitConfig().DailyTokenQuota
	}
	if cfg.Clock == nil {
		cfg.Clock = func() time.Time { return time.Now().UTC() }
	}
	return &RateLimitProvider{inner: inner, cfg: cfg, states: map[core.TenantID]*tenantLimitState{}}
}

func (r *RateLimitProvider) Name() string { return r.inner.Name() }

func (r *RateLimitProvider) limitsFor(tenant core.TenantID) (int, int64) {
	rpm, quota := r.cfg.RequestsPerMinute, r.cfg.DailyTokenQuota
	if r.cfg.Override != nil {
		if orpm, oquota := r.cfg.Override(tenant); orpm > 0 || oquota > 0 {
			if orpm > 0 {
				rpm = orpm
			}
			if oquota > 0 {
				quota = oquota
			}
		}
	}
	return rpm, quota
}

func (r *RateLimitProvider) stateFor(tenant core.TenantID) *tenantLimitState {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.states[tenant]
	if !ok {
		s = &tenantLimitState{}
		r.states[tenant] = s
	}
	return s
}

func (r *RateLimitProvider) Complete(ctx context.Context, req ports.CompletionRequest) (ports.CompletionResponse, error) {
	if req.TenantID.IsZero() {
		// System/platform calls carry no tenant scope (e.g. seeding the
		// platform knowledge corpus) and are not subject to a per-tenant
		// budget that, by definition, does not apply to them.
		return r.inner.Complete(ctx, req)
	}

	rpm, quota := r.limitsFor(req.TenantID)
	state := r.stateFor(req.TenantID)
	now := r.cfg.Clock()

	state.mu.Lock()
	refillBucket(state, rpm, now)
	if state.tokens < 1 {
		state.mu.Unlock()
		return ports.CompletionResponse{}, core.NewError(core.ErrThrottled, "llm_rate_limited",
			"tenant %s exceeded %d requests/minute to the LLM provider", req.TenantID, rpm).
			WithDetail("requests_per_minute", rpm)
	}
	state.tokens--

	rolloverDay(state, now)
	if state.tokensUsedDay >= quota {
		state.mu.Unlock()
		return ports.CompletionResponse{}, core.NewError(core.ErrThrottled, "llm_quota_exceeded",
			"tenant %s exhausted its daily LLM token quota of %d", req.TenantID, quota).
			WithDetail("daily_token_quota", quota).WithDetail("tokens_used_today", state.tokensUsedDay)
	}
	state.mu.Unlock()

	resp, err := r.inner.Complete(ctx, req)
	if err == nil {
		state.mu.Lock()
		state.tokensUsedDay += int64(resp.InputTokens + resp.OutputTokens)
		state.mu.Unlock()
	}
	return resp, err
}

func refillBucket(s *tenantLimitState, rpm int, now time.Time) {
	if !s.bucketInitialized {
		s.tokens = float64(rpm)
		s.lastRefill = now
		s.bucketInitialized = true
		return
	}
	elapsed := now.Sub(s.lastRefill).Seconds()
	if elapsed <= 0 {
		return
	}
	ratePerSecond := float64(rpm) / 60.0
	s.tokens += elapsed * ratePerSecond
	if s.tokens > float64(rpm) {
		s.tokens = float64(rpm)
	}
	s.lastRefill = now
}

func rolloverDay(s *tenantLimitState, now time.Time) {
	key := now.Format("2006-01-02")
	if s.dayKey != key {
		s.dayKey = key
		s.tokensUsedDay = 0
	}
}

func (r *RateLimitProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return r.inner.Embed(ctx, texts)
}

func (r *RateLimitProvider) Healthy(ctx context.Context) bool { return r.inner.Healthy(ctx) }
