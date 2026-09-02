package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// CacheConfig sizes CachingProvider's behaviour.
type CacheConfig struct {
	// TTLDuration is how long a cached response is served before the
	// underlying provider is called again. Zero disables expiry (entries
	// live for the process lifetime, bounded only by MaxEntries).
	TTLDuration time.Duration
	// MaxEntries bounds memory use; the oldest entry is evicted once the
	// cache is full. Zero means DefaultCacheConfig's default applies.
	MaxEntries int
	Clock      func() time.Time
}

// DefaultCacheConfig caches for five minutes and holds at most 500 entries —
// enough to absorb a user re-asking the same onboarding question or a
// dashboard re-rendering the same copilot answer within a session, without
// growing unbounded.
func DefaultCacheConfig() CacheConfig {
	return CacheConfig{TTLDuration: 5 * time.Minute, MaxEntries: 500}
}

type cacheEntry struct {
	resp     ports.CompletionResponse
	storedAt time.Time
}

// CachingProvider wraps a ports.LLMProvider with an in-process cache keyed on
// the exact request content (tenant, system prompt, messages, tools, schema).
// Only requests with Temperature <= 0 are cached — a nonzero temperature is
// an explicit request for varied output, and caching it would silently
// defeat that. This trades a little cost/latency for identical repeated
// prompts (a common shape in onboarding, where "show me what you know" or a
// re-rendered summary asks the same question twice) without ever returning a
// stale answer for a request that asked to vary.
//
// The cache sits after SanitizingProvider in Chain so its key reflects the
// sanitized request that actually left the process, and before the network
// call so a hit costs no round trip at all.
type CachingProvider struct {
	inner ports.LLMProvider
	cfg   CacheConfig

	mu      sync.Mutex
	entries map[string]cacheEntry
	order   []string // insertion order, for FIFO eviction
}

var _ ports.LLMProvider = (*CachingProvider)(nil)

// NewCachingProvider wraps inner with cfg.
func NewCachingProvider(inner ports.LLMProvider, cfg CacheConfig) *CachingProvider {
	if cfg.MaxEntries <= 0 {
		cfg.MaxEntries = DefaultCacheConfig().MaxEntries
	}
	if cfg.Clock == nil {
		cfg.Clock = func() time.Time { return time.Now().UTC() }
	}
	return &CachingProvider{inner: inner, cfg: cfg, entries: map[string]cacheEntry{}}
}

func (c *CachingProvider) Name() string { return c.inner.Name() }

func (c *CachingProvider) Complete(ctx context.Context, req ports.CompletionRequest) (ports.CompletionResponse, error) {
	if req.Temperature > 0 {
		return c.inner.Complete(ctx, req)
	}
	key := cacheKey(req)

	c.mu.Lock()
	if e, ok := c.entries[key]; ok {
		if c.cfg.TTLDuration <= 0 || c.cfg.Clock().Sub(e.storedAt) < c.cfg.TTLDuration {
			c.mu.Unlock()
			return e.resp, nil
		}
		// Expired: fall through and re-fetch, cleaning up the stale entry.
		delete(c.entries, key)
	}
	c.mu.Unlock()

	resp, err := c.inner.Complete(ctx, req)
	if err != nil {
		return resp, err
	}

	c.mu.Lock()
	if _, exists := c.entries[key]; !exists {
		if len(c.order) >= c.cfg.MaxEntries {
			oldest := c.order[0]
			c.order = c.order[1:]
			delete(c.entries, oldest)
		}
		c.order = append(c.order, key)
	}
	c.entries[key] = cacheEntry{resp: resp, storedAt: c.cfg.Clock()}
	c.mu.Unlock()

	return resp, nil
}

// cacheKey hashes the parts of a request that determine its answer. Using a
// SHA-256 hex digest rather than the raw JSON keeps map keys a fixed, small
// size regardless of prompt length.
func cacheKey(req ports.CompletionRequest) string {
	type keyable struct {
		Tenant      string                 `json:"tenant"`
		Purpose     string                 `json:"purpose"`
		System      string                 `json:"system"`
		Messages    []ports.Message        `json:"messages"`
		Tools       []ports.ToolDefinition `json:"tools"`
		Schema      map[string]any         `json:"schema"`
		MaxTokens   int                    `json:"max_tokens"`
		Temperature float64                `json:"temperature"`
	}
	k := keyable{
		Tenant: string(req.TenantID), Purpose: req.Purpose, System: req.System,
		Messages: req.Messages, Tools: req.Tools, Schema: req.ResponseSchema,
		MaxTokens: req.MaxTokens, Temperature: req.Temperature,
	}
	b, err := json.Marshal(k)
	if err != nil {
		// Marshalling a plain struct of strings/slices/maps cannot fail in
		// practice; fall back to a key that simply never hits so caching
		// degrades to a pass-through rather than panicking.
		return "unmarshalable"
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (c *CachingProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return c.inner.Embed(ctx, texts)
}

func (c *CachingProvider) Healthy(ctx context.Context) bool { return c.inner.Healthy(ctx) }
