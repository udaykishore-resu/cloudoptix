package auth

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sync"
	"time"
)

// jsonWebKey is one entry of a JSON Web Key Set (RFC 7517), covering the RSA
// and EC key types every OIDC provider CloudOptix targets (Auth0, Okta,
// Cognito, Azure AD, Google) actually publishes. Ed25519 is intentionally
// unsupported — no target provider issues it for OIDC signing today, and
// adding a key type nothing exercises is a bigger risk (an untested parse
// path) than the theoretical completeness gap.
type jsonWebKey struct {
	Kty string `json:"kty"` // "RSA" | "EC"
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`

	// RSA fields
	N string `json:"n,omitempty"`
	E string `json:"e,omitempty"`

	// EC fields
	Crv string `json:"crv,omitempty"`
	X   string `json:"x,omitempty"`
	Y   string `json:"y,omitempty"`
}

type jwkSet struct {
	Keys []jsonWebKey `json:"keys"`
}

func (k jsonWebKey) publicKey() (crypto.PublicKey, error) {
	switch k.Kty {
	case "RSA":
		nBytes, err := b64url(k.N)
		if err != nil {
			return nil, fmt.Errorf("auth: jwk %s: decoding n: %w", k.Kid, err)
		}
		eBytes, err := b64url(k.E)
		if err != nil {
			return nil, fmt.Errorf("auth: jwk %s: decoding e: %w", k.Kid, err)
		}
		n := new(big.Int).SetBytes(nBytes)
		e := new(big.Int).SetBytes(eBytes)
		return &rsa.PublicKey{N: n, E: int(e.Int64())}, nil
	case "EC":
		var curve elliptic.Curve
		switch k.Crv {
		case "P-256":
			curve = elliptic.P256()
		case "P-384":
			curve = elliptic.P384()
		case "P-521":
			curve = elliptic.P521()
		default:
			return nil, fmt.Errorf("auth: jwk %s: unsupported EC curve %q", k.Kid, k.Crv)
		}
		xBytes, err := b64url(k.X)
		if err != nil {
			return nil, fmt.Errorf("auth: jwk %s: decoding x: %w", k.Kid, err)
		}
		yBytes, err := b64url(k.Y)
		if err != nil {
			return nil, fmt.Errorf("auth: jwk %s: decoding y: %w", k.Kid, err)
		}
		return &ecdsa.PublicKey{
			Curve: curve,
			X:     new(big.Int).SetBytes(xBytes),
			Y:     new(big.Int).SetBytes(yBytes),
		}, nil
	default:
		return nil, fmt.Errorf("auth: jwk %s: unsupported key type %q", k.Kid, k.Kty)
	}
}

func b64url(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }

// JWKSCache fetches and caches an identity provider's signing keys, keyed by
// kid, and refreshes on a TTL as well as on demand when a token presents a
// kid the cache does not recognise — which is what makes key rotation
// transparent: the provider can roll its signing key at any time, and the
// first token signed with the new key triggers exactly one refetch rather
// than a validation failure.
type JWKSCache struct {
	url        string
	httpClient *http.Client
	ttl        time.Duration

	mu          sync.RWMutex
	keys        map[string]crypto.PublicKey
	fetchedAt   time.Time
	lastErr     error
	refreshOnce singleflight // collapses concurrent refreshes triggered by a burst of tokens with the same unknown kid
}

// NewJWKSCache builds a cache pointed at a JWKS URL (typically the
// jwks_uri from OIDC discovery — see oidc.go).
func NewJWKSCache(jwksURL string, ttl time.Duration, httpClient *http.Client) *JWKSCache {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	return &JWKSCache{url: jwksURL, httpClient: httpClient, ttl: ttl, keys: map[string]crypto.PublicKey{}}
}

// Key returns the public key for kid, fetching (or refetching, if the TTL
// has elapsed or kid is unknown) as needed.
func (c *JWKSCache) Key(ctx context.Context, kid string) (crypto.PublicKey, error) {
	c.mu.RLock()
	key, ok := c.keys[kid]
	stale := time.Since(c.fetchedAt) > c.ttl
	c.mu.RUnlock()
	if ok && !stale {
		return key, nil
	}

	if err := c.refresh(ctx); err != nil {
		// A refresh failure with a still-usable (if stale) cached key is not
		// fatal — better to keep validating tokens against a slightly old
		// key set than to reject every request because the IdP had one bad
		// second. Only surface the error if we have nothing at all for kid.
		c.mu.RLock()
		key, ok = c.keys[kid]
		c.mu.RUnlock()
		if !ok {
			return nil, fmt.Errorf("auth: fetching JWKS: %w", err)
		}
		return key, nil
	}

	c.mu.RLock()
	defer c.mu.RUnlock()
	key, ok = c.keys[kid]
	if !ok {
		return nil, fmt.Errorf("auth: no signing key found for kid %q (fetched %d keys)", kid, len(c.keys))
	}
	return key, nil
}

func (c *JWKSCache) refresh(ctx context.Context) error {
	return c.refreshOnce.Do(func() error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
		if err != nil {
			return err
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			c.recordErr(err)
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			err := fmt.Errorf("auth: JWKS endpoint %s returned %d", c.url, resp.StatusCode)
			c.recordErr(err)
			return err
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MiB is generous for a key set
		if err != nil {
			c.recordErr(err)
			return err
		}
		var set jwkSet
		if err := json.Unmarshal(body, &set); err != nil {
			c.recordErr(err)
			return err
		}
		keys := make(map[string]crypto.PublicKey, len(set.Keys))
		for _, k := range set.Keys {
			if k.Use != "" && k.Use != "sig" {
				continue // encryption keys are never valid signing keys
			}
			pub, err := k.publicKey()
			if err != nil {
				continue // one malformed key must not take down the whole set
			}
			keys[k.Kid] = pub
		}
		c.mu.Lock()
		c.keys = keys
		c.fetchedAt = time.Now()
		c.lastErr = nil
		c.mu.Unlock()
		return nil
	})
}

func (c *JWKSCache) recordErr(err error) {
	c.mu.Lock()
	c.lastErr = err
	c.mu.Unlock()
}

// LastError reports the most recent refresh failure, for the readiness check
// to surface ("JWKS unreachable") without treating a transient DNS blip as
// pod-fatal.
func (c *JWKSCache) LastError() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastErr
}

// KeyCount reports how many keys are currently cached, for diagnostics.
func (c *JWKSCache) KeyCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.keys)
}

// singleflight collapses concurrent calls to Do into one execution, so a
// burst of requests that all miss the cache at once (e.g. right after a key
// rotation) triggers exactly one HTTP fetch instead of one per request.
// golang.org/x/sync/singleflight is not on the approved dependency list, so
// this is the minimal stdlib-only equivalent for the one call site here.
type singleflight struct {
	mu  sync.Mutex
	wg  *sync.WaitGroup
	err error
}

func (s *singleflight) Do(fn func() error) error {
	s.mu.Lock()
	if s.wg != nil {
		wg := s.wg
		s.mu.Unlock()
		wg.Wait()
		s.mu.Lock()
		err := s.err
		s.mu.Unlock()
		return err
	}
	wg := &sync.WaitGroup{}
	wg.Add(1)
	s.wg = wg
	s.mu.Unlock()

	err := fn()

	s.mu.Lock()
	s.err = err
	s.wg = nil
	s.mu.Unlock()
	wg.Done()
	return err
}
