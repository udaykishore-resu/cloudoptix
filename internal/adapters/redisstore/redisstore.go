package redisstore

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// Config configures the Redis client. It mirrors config.RedisConfig
// field-for-field rather than importing it, keeping this adapter free of any
// dependency on the configuration package — the same discipline
// internal/infrastructure/server follows.
type Config struct {
	Addrs        []string
	Password     string
	DB           int
	DialTimeout  time.Duration
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	PoolSize     int
	TLSEnabled   bool
	// KeyPrefix namespaces every key this process writes, so one Redis can
	// safely back a dev and a staging deployment without either seeing the
	// other's cached graphs. Defaults to "cloudoptix".
	KeyPrefix string
}

// Client owns the Redis connection and hands out the two ports it implements.
type Client struct {
	rdb    redis.UniversalClient
	prefix string
}

// Open builds a Client. It does not connect eagerly (go-redis dials lazily);
// call Ping to fail startup on an unreachable Redis rather than discovering
// it on the first cache read.
func Open(cfg Config) (*Client, error) {
	if len(cfg.Addrs) == 0 {
		return nil, fmt.Errorf("redisstore: at least one address is required")
	}
	prefix := cfg.KeyPrefix
	if prefix == "" {
		prefix = "cloudoptix"
	}
	opts := &redis.UniversalOptions{
		Addrs:        cfg.Addrs,
		Password:     cfg.Password,
		DB:           cfg.DB,
		DialTimeout:  cfg.DialTimeout,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		PoolSize:     cfg.PoolSize,
	}
	if cfg.TLSEnabled {
		// TLS 1.2 is the floor rather than 1.3 because managed Redis
		// offerings (ElastiCache in-transit encryption among them) still
		// negotiate 1.2; refusing it here would mean TLS could not be
		// enabled at all against the very deployments that need it.
		opts.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	return &Client{rdb: redis.NewUniversalClient(opts), prefix: prefix}, nil
}

// Ping verifies the connection.
func (c *Client) Ping(ctx context.Context) error {
	if err := c.rdb.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redisstore: ping: %w", err)
	}
	return nil
}

// Close releases the connection pool.
func (c *Client) Close() error { return c.rdb.Close() }

// Cache returns the ports.Cache implementation.
func (c *Client) Cache() ports.Cache { return &cacheAdapter{c: c} }

// Locker returns the ports.Locker implementation.
func (c *Client) Locker() ports.Locker { return &lockerAdapter{c: c} }

// key builds a tenant-scoped cache key. The tenant segment is inside the
// key rather than a caller-supplied prefix, which is what makes it
// impossible for a caller to construct a key that reads another tenant's
// entry — the contract ports.Cache states.
func (c *Client) key(tenant core.TenantID, k string) string {
	return c.prefix + ":cache:" + string(tenant) + ":" + k
}

func (c *Client) lockKey(k string) string { return c.prefix + ":lock:" + k }

// --- cache ---------------------------------------------------------------

type cacheAdapter struct{ c *Client }

var _ ports.Cache = (*cacheAdapter)(nil)

// Get decodes the stored value into dest, reporting whether an entry
// existed. A decode failure is treated as a miss with an error: the entry is
// unusable either way, and returning found=false lets the caller recompute
// rather than propagate a failure the next write would fix.
func (a *cacheAdapter) Get(ctx context.Context, tenant core.TenantID, key string, dest any) (bool, error) {
	raw, err := a.c.rdb.Get(ctx, a.c.key(tenant, key)).Bytes()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("redisstore: cache get %q: %w", key, err)
	}
	if dest == nil {
		return true, nil
	}
	if err := json.Unmarshal(raw, dest); err != nil {
		return false, fmt.Errorf("redisstore: cache decode %q: %w", key, err)
	}
	return true, nil
}

// Set stores value with a TTL. A non-positive TTL is rejected rather than
// stored forever: every cache entry in CloudOptix is derived data that must
// eventually be recomputed, and an entry with no expiry is a stale graph
// waiting to be served after a discovery run has already superseded it.
func (a *cacheAdapter) Set(ctx context.Context, tenant core.TenantID, key string, value any, ttl time.Duration) error {
	if ttl <= 0 {
		return fmt.Errorf("redisstore: cache set %q requires a positive ttl", key)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("redisstore: cache encode %q: %w", key, err)
	}
	if err := a.c.rdb.Set(ctx, a.c.key(tenant, key), raw, ttl).Err(); err != nil {
		return fmt.Errorf("redisstore: cache set %q: %w", key, err)
	}
	return nil
}

// Delete removes one entry.
func (a *cacheAdapter) Delete(ctx context.Context, tenant core.TenantID, key string) error {
	if err := a.c.rdb.Del(ctx, a.c.key(tenant, key)).Err(); err != nil {
		return fmt.Errorf("redisstore: cache delete %q: %w", key, err)
	}
	return nil
}

// InvalidatePrefix clears a whole family of derived data. It uses SCAN with
// UNLINK rather than KEYS with DEL: KEYS blocks the server for the whole
// keyspace scan, and DEL on a large batch blocks it again while freeing
// memory. Invalidation runs right after discovery, when the platform is
// already busy — the one moment a blocking command is least affordable.
func (a *cacheAdapter) InvalidatePrefix(ctx context.Context, tenant core.TenantID, prefix string) error {
	match := a.c.key(tenant, prefix) + "*"
	var cursor uint64
	for {
		keys, next, err := a.c.rdb.Scan(ctx, cursor, match, 256).Result()
		if err != nil {
			return fmt.Errorf("redisstore: cache scan %q: %w", prefix, err)
		}
		if len(keys) > 0 {
			if err := a.c.rdb.Unlink(ctx, keys...).Err(); err != nil {
				return fmt.Errorf("redisstore: cache unlink %q: %w", prefix, err)
			}
		}
		cursor = next
		if cursor == 0 {
			return nil
		}
	}
}

// --- locker --------------------------------------------------------------

type lockerAdapter struct{ c *Client }

var _ ports.Locker = (*lockerAdapter)(nil)

// releaseScript deletes the lock only if the caller still holds it. See the
// package doc for why a bare DEL is wrong here.
var releaseScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("DEL", KEYS[1])
end
return 0
`)

// Acquire takes the lock with SET NX PX, returning core.ErrConflict when it
// is already held. The returned release closure is safe to call more than
// once and after the lease has expired.
func (a *lockerAdapter) Acquire(ctx context.Context, key string, ttl time.Duration) (func(), error) {
	if strings.TrimSpace(key) == "" {
		return nil, core.Invalid("lock key is required")
	}
	if ttl <= 0 {
		ttl = time.Minute
	}
	token, err := newToken()
	if err != nil {
		return nil, err
	}
	full := a.c.lockKey(key)
	ok, err := a.c.rdb.SetNX(ctx, full, token, ttl).Result()
	if err != nil {
		return nil, core.NewError(core.ErrUnavailable, "lock_backend_unavailable",
			"could not reach the lock backend for %q", key).Wrap(err)
	}
	if !ok {
		return nil, core.Conflict("lock %q is held by another worker", key)
	}
	released := false
	return func() {
		if released {
			return
		}
		released = true
		// A fresh context: release must still run when the caller's context
		// was cancelled, which is the normal case on shutdown.
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = releaseScript.Run(rctx, a.c.rdb, []string{full}, token).Err()
	}, nil
}

func newToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("redisstore: entropy source failed: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
