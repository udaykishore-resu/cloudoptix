package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

func x509MarshalPKIXPublicKeyForTest(t *testing.T, pub *rsa.PublicKey) []byte {
	t.Helper()
	b, err := x509.MarshalPKIXPublicKey(pub)
	require.NoError(t, err)
	return b
}

func roleOf(r string) core.Role { return core.Role(r) }

// testKeyServer serves a JWKS containing the given RSA/EC public keys under
// their kid, standing in for an identity provider's jwks_uri.
func testKeyServer(t *testing.T, keys ...jsonWebKey) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwkSet{Keys: keys})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func rsaJWK(t *testing.T, pub *rsa.PublicKey, kid string) jsonWebKey {
	t.Helper()
	return jsonWebKey{
		Kty: "RSA", Kid: kid, Use: "sig", Alg: "RS256",
		N: base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		E: base64.RawURLEncoding.EncodeToString(big3(pub.E)),
	}
}

func big3(e int) []byte {
	// Minimal big-endian encoding of a small int (the RSA public exponent,
	// almost always 65537), matching how a real JWKS encodes "e".
	b := []byte{byte(e >> 16), byte(e >> 8), byte(e)}
	i := 0
	for i < len(b)-1 && b[i] == 0 {
		i++
	}
	return b[i:]
}

func mustGenRSA(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return key
}

func mustGenEC(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	return key
}

func signRS256(t *testing.T, key *rsa.PrivateKey, kid string, claims jwt.Claims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = kid
	s, err := tok.SignedString(key)
	require.NoError(t, err)
	return s
}

func baseClaims(now time.Time, issuer, audience, subject string) Claims {
	return Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   subject,
			Issuer:    issuer,
			Audience:  jwt.ClaimStrings{audience},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		},
		Email: "user@example.com",
		Roles: []string{"viewer"},
	}
}

func newTestValidator(t *testing.T, jwksURL, issuer, audience string, algs []string, now func() time.Time) *Validator {
	t.Helper()
	cache := NewJWKSCache(jwksURL, time.Minute, nil)
	v, err := NewValidator(ValidatorConfig{
		Issuer: issuer, Audience: audience, AllowedAlgorithms: algs,
		JWKS: cache, Now: now,
	})
	require.NoError(t, err)
	return v
}

func TestValidator_AcceptsValidRS256Token(t *testing.T) {
	key := mustGenRSA(t)
	srv := testKeyServer(t, rsaJWK(t, &key.PublicKey, "kid-1"))
	now := time.Now()
	v := newTestValidator(t, srv.URL, "https://issuer.example.com", "cloudoptix-api", []string{"RS256"}, func() time.Time { return now })

	token := signRS256(t, key, "kid-1", baseClaims(now, "https://issuer.example.com", "cloudoptix-api", "user-1"))
	claims, err := v.Validate(context.Background(), token)
	require.NoError(t, err)
	assert.Equal(t, "user-1", claims.Subject)
	assert.Equal(t, "user@example.com", claims.Email)
}

func TestValidator_RejectsAlgNone(t *testing.T) {
	key := mustGenRSA(t)
	srv := testKeyServer(t, rsaJWK(t, &key.PublicKey, "kid-1"))
	now := time.Now()
	v := newTestValidator(t, srv.URL, "https://issuer.example.com", "cloudoptix-api", []string{"RS256"}, func() time.Time { return now })

	// Forge a token with alg:none and no signature — the classic bypass.
	claims := baseClaims(now, "https://issuer.example.com", "cloudoptix-api", "attacker")
	tok := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
	forged, err := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
	require.NoError(t, err)

	_, err = v.Validate(context.Background(), forged)
	require.Error(t, err, "alg:none must never validate")
}

func TestValidator_RejectsAlgorithmConfusionHS256UsingRSAPublicKeyAsSecret(t *testing.T) {
	key := mustGenRSA(t)
	srv := testKeyServer(t, rsaJWK(t, &key.PublicKey, "kid-1"))
	now := time.Now()
	// Even a permissive-looking allowlist including both families must not
	// let an HS256 token through by signing with the RSA public key's PEM
	// bytes as if it were an HMAC secret — the classic RS256/HS256
	// confusion attack, since an RSA public key is, by definition, public.
	v := newTestValidator(t, srv.URL, "https://issuer.example.com", "cloudoptix-api", []string{"RS256", "HS256"}, func() time.Time { return now })

	pubBytes := x509MarshalPKIXPublicKeyForTest(t, &key.PublicKey)
	claims := baseClaims(now, "https://issuer.example.com", "cloudoptix-api", "attacker")
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tok.Header["kid"] = "kid-1"
	forged, err := tok.SignedString(pubBytes)
	require.NoError(t, err)

	_, err = v.Validate(context.Background(), forged)
	require.Error(t, err, "HS256 signed with the RSA public key's bytes must never validate")
}

func TestValidator_RejectsUnexpectedAlgorithmOutsideAllowlist(t *testing.T) {
	ecKey := mustGenEC(t)
	rsaKey := mustGenRSA(t)
	srv := testKeyServer(t, rsaJWK(t, &rsaKey.PublicKey, "kid-1"))
	now := time.Now()
	v := newTestValidator(t, srv.URL, "https://issuer.example.com", "cloudoptix-api", []string{"RS256"}, func() time.Time { return now })

	claims := baseClaims(now, "https://issuer.example.com", "cloudoptix-api", "user-1")
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	tok.Header["kid"] = "kid-1"
	signed, err := tok.SignedString(ecKey)
	require.NoError(t, err)

	_, err = v.Validate(context.Background(), signed)
	require.Error(t, err, "ES256 is not in the allowlist and must be rejected")
}

func TestValidator_RejectsWrongIssuer(t *testing.T) {
	key := mustGenRSA(t)
	srv := testKeyServer(t, rsaJWK(t, &key.PublicKey, "kid-1"))
	now := time.Now()
	v := newTestValidator(t, srv.URL, "https://issuer.example.com", "cloudoptix-api", []string{"RS256"}, func() time.Time { return now })

	token := signRS256(t, key, "kid-1", baseClaims(now, "https://evil.example.com", "cloudoptix-api", "user-1"))
	_, err := v.Validate(context.Background(), token)
	require.Error(t, err)
}

func TestValidator_RejectsWrongAudience(t *testing.T) {
	key := mustGenRSA(t)
	srv := testKeyServer(t, rsaJWK(t, &key.PublicKey, "kid-1"))
	now := time.Now()
	v := newTestValidator(t, srv.URL, "https://issuer.example.com", "cloudoptix-api", []string{"RS256"}, func() time.Time { return now })

	token := signRS256(t, key, "kid-1", baseClaims(now, "https://issuer.example.com", "wrong-audience", "user-1"))
	_, err := v.Validate(context.Background(), token)
	require.Error(t, err)
}

func TestValidator_RejectsExpiredToken(t *testing.T) {
	key := mustGenRSA(t)
	srv := testKeyServer(t, rsaJWK(t, &key.PublicKey, "kid-1"))
	issuedAt := time.Now().Add(-2 * time.Hour)
	claims := baseClaims(issuedAt, "https://issuer.example.com", "cloudoptix-api", "user-1")
	token := signRS256(t, key, "kid-1", claims) // expires 1h after issuedAt, i.e. 1h ago

	v := newTestValidator(t, srv.URL, "https://issuer.example.com", "cloudoptix-api", []string{"RS256"}, time.Now)
	_, err := v.Validate(context.Background(), token)
	require.Error(t, err)
}

func TestValidator_KeyRotation_RefetchesOnUnknownKid(t *testing.T) {
	oldKey := mustGenRSA(t)
	newKey := mustGenRSA(t)

	var serveNewKey bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if serveNewKey {
			_ = json.NewEncoder(w).Encode(jwkSet{Keys: []jsonWebKey{rsaJWK(t, &newKey.PublicKey, "kid-new")}})
		} else {
			_ = json.NewEncoder(w).Encode(jwkSet{Keys: []jsonWebKey{rsaJWK(t, &oldKey.PublicKey, "kid-old")}})
		}
	}))
	t.Cleanup(srv.Close)

	now := time.Now()
	v := newTestValidator(t, srv.URL, "https://issuer.example.com", "cloudoptix-api", []string{"RS256"}, func() time.Time { return now })

	oldToken := signRS256(t, oldKey, "kid-old", baseClaims(now, "https://issuer.example.com", "cloudoptix-api", "user-1"))
	_, err := v.Validate(context.Background(), oldToken)
	require.NoError(t, err, "should validate against the initially-fetched key")

	// The IdP rotates its signing key. A token signed with the new key,
	// presenting a kid the cache has never seen, must trigger a refetch
	// rather than fail outright.
	serveNewKey = true
	newToken := signRS256(t, newKey, "kid-new", baseClaims(now, "https://issuer.example.com", "cloudoptix-api", "user-1"))
	_, err = v.Validate(context.Background(), newToken)
	require.NoError(t, err, "should refetch the JWKS and validate against the rotated key")
}

func TestValidator_ConstructionRefusesNoneInAllowlist(t *testing.T) {
	cache := NewJWKSCache("http://unused.example.com", time.Minute, nil)
	_, err := NewValidator(ValidatorConfig{
		Issuer: "https://issuer.example.com", AllowedAlgorithms: []string{"RS256", "none"}, JWKS: cache,
	})
	require.Error(t, err)
}

func TestValidator_ConstructionRefusesEmptyAllowlist(t *testing.T) {
	cache := NewJWKSCache("http://unused.example.com", time.Minute, nil)
	_, err := NewValidator(ValidatorConfig{
		Issuer: "https://issuer.example.com", AllowedAlgorithms: nil, JWKS: cache,
	})
	require.Error(t, err)
}

func TestClaims_ToPrincipal_ExcludesSystemRole(t *testing.T) {
	c := Claims{Roles: []string{"viewer", "system", "tenant_admin"}}
	p := c.ToPrincipal("tenant-1")
	for _, r := range p.Roles {
		assert.NotEqual(t, "system", string(r))
	}
	assert.Contains(t, p.Roles, roleOf("viewer"))
	assert.Contains(t, p.Roles, roleOf("tenant_admin"))
}
