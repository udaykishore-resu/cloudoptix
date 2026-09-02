package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// discoveryDocument is the subset of an OIDC provider's
// /.well-known/openid-configuration response CloudOptix actually uses.
type discoveryDocument struct {
	Issuer                string   `json:"issuer"`
	JWKSURI               string   `json:"jwks_uri"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	IDTokenSigningAlgs    []string `json:"id_token_signing_alg_values_supported"`
}

// DiscoverJWKSURL fetches the OIDC discovery document at
// issuer + "/.well-known/openid-configuration" and returns its jwks_uri. It
// also cross-checks that the document's own "issuer" field matches the
// issuer it was fetched from — RFC 8414 requires this, and skipping the
// check is a known way a misconfigured (or spoofed) discovery endpoint
// smuggles in a different trust root than the one an operator configured.
func DiscoverJWKSURL(ctx context.Context, issuer string, httpClient *http.Client) (string, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	u := strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("auth: fetching OIDC discovery document from %s: %w", u, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("auth: OIDC discovery endpoint %s returned %d", u, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	var doc discoveryDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", fmt.Errorf("auth: parsing OIDC discovery document: %w", err)
	}
	if doc.Issuer != issuer && doc.Issuer != strings.TrimRight(issuer, "/") {
		return "", fmt.Errorf("auth: discovery document issuer %q does not match configured issuer %q — refusing to trust it", doc.Issuer, issuer)
	}
	if doc.JWKSURI == "" {
		return "", fmt.Errorf("auth: discovery document from %s carries no jwks_uri", u)
	}
	return doc.JWKSURI, nil
}
