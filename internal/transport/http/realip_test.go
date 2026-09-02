package http

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// serveWith runs one request through realIPMiddleware and reports the
// RemoteAddr the handler observed — which is exactly what clientIP, the access
// log and the audit record will see.
func serveWith(t *testing.T, deps Deps, req *http.Request) string {
	t.Helper()
	var seen string
	h := realIPMiddleware(deps)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = r.RemoteAddr
	}))
	h.ServeHTTP(httptest.NewRecorder(), req)
	return seen
}

func depsTrusting(t *testing.T, cidrs ...string) Deps {
	t.Helper()
	p, err := ParseTrustedProxies(cidrs)
	require.NoError(t, err)
	return Deps{TrustedProxyHeader: "X-Forwarded-For", TrustedProxies: p}
}

func reqFrom(peer string, headers map[string]string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/approvals", nil)
	r.RemoteAddr = peer
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

// The finding this whole file exists for: an attacker who can reach the API
// directly must not be able to choose the address recorded against an
// approval.
func TestRealIP_SpoofedHeaderFromUntrustedPeerIsIgnored(t *testing.T) {
	deps := depsTrusting(t, "10.0.0.0/8")
	got := serveWith(t, deps, reqFrom("203.0.113.9:51234", map[string]string{
		"X-Forwarded-For": "8.8.8.8",
		"X-Real-IP":       "8.8.8.8",
		"True-Client-IP":  "8.8.8.8",
	}))
	assert.Equal(t, "203.0.113.9:51234", got,
		"a caller that is not a declared proxy must not be able to set its own recorded address")
}

func TestRealIP_HeaderHonouredFromTrustedProxy(t *testing.T) {
	deps := depsTrusting(t, "10.0.0.0/8")
	got := serveWith(t, deps, reqFrom("10.4.1.7:44321", map[string]string{
		"X-Forwarded-For": "198.51.100.23",
	}))
	assert.Equal(t, "198.51.100.23:44321", got)
}

// With several proxies in front, the hops our own infrastructure appended are
// the rightmost entries. The client is the rightmost entry that is not itself
// a trusted proxy — taking the leftmost, as chi does, returns whatever the
// original caller chose to send.
func TestRealIP_MultiHopPicksRightmostUntrustedEntry(t *testing.T) {
	deps := depsTrusting(t, "10.0.0.0/8", "192.168.0.0/16")
	got := serveWith(t, deps, reqFrom("10.4.1.7:44321", map[string]string{
		"X-Forwarded-For": "8.8.8.8, 198.51.100.23, 192.168.10.4, 10.4.1.7",
	}))
	assert.Equal(t, "198.51.100.23:44321", got,
		"8.8.8.8 is caller-supplied and must not win over the address our edge proxy actually saw")
}

func TestRealIP_NoTrustedProxiesConfiguredIgnoresHeaders(t *testing.T) {
	// The safe default: an operator who has not declared a proxy gets the
	// socket address, even from an RFC1918 peer.
	deps := Deps{TrustedProxyHeader: "X-Forwarded-For"}
	got := serveWith(t, deps, reqFrom("10.4.1.7:44321", map[string]string{
		"X-Forwarded-For": "8.8.8.8",
	}))
	assert.Equal(t, "10.4.1.7:44321", got)
}

func TestRealIP_NoHeaderConfiguredIgnoresHeaders(t *testing.T) {
	p, err := ParseTrustedProxies([]string{"10.0.0.0/8"})
	require.NoError(t, err)
	deps := Deps{TrustedProxies: p} // header deliberately unset
	got := serveWith(t, deps, reqFrom("10.4.1.7:44321", map[string]string{
		"X-Forwarded-For": "8.8.8.8",
	}))
	assert.Equal(t, "10.4.1.7:44321", got)
}

func TestRealIP_AllHopsTrustedFallsBackToPeer(t *testing.T) {
	// Every entry is one of ours, so no client address was ever recorded.
	// Reporting the peer is honest; inventing one is not.
	deps := depsTrusting(t, "10.0.0.0/8")
	got := serveWith(t, deps, reqFrom("10.4.1.7:44321", map[string]string{
		"X-Forwarded-For": "10.1.1.1, 10.2.2.2",
	}))
	assert.Equal(t, "10.4.1.7:44321", got)
}

func TestRealIP_IPv6AndBracketedForms(t *testing.T) {
	deps := depsTrusting(t, "fd00::/8")
	got := serveWith(t, deps, reqFrom("[fd00::1]:44321", map[string]string{
		"X-Forwarded-For": "2001:db8::5",
	}))
	assert.Equal(t, "[2001:db8::5]:44321", got)
}

func TestRealIP_MalformedHeaderEntriesAreSkipped(t *testing.T) {
	deps := depsTrusting(t, "10.0.0.0/8")
	got := serveWith(t, deps, reqFrom("10.4.1.7:44321", map[string]string{
		"X-Forwarded-For": "198.51.100.23, not-an-ip, , unknown",
	}))
	assert.Equal(t, "198.51.100.23:44321", got)
}

func TestParseTrustedProxies(t *testing.T) {
	t.Run("cidr and bare address", func(t *testing.T) {
		p, err := ParseTrustedProxies([]string{"10.0.0.0/8", "192.0.2.7", " ", "fd00::/8"})
		require.NoError(t, err)
		require.Len(t, p, 3)
		assert.Equal(t, "10.0.0.0/8", p[0].String())
		assert.Equal(t, "192.0.2.7/32", p[1].String(), "a bare address becomes a single-host prefix")
	})
	t.Run("a typo is an error, never a silent skip", func(t *testing.T) {
		_, err := ParseTrustedProxies([]string{"10.0.0.0/8", "10.0.0.0/999"})
		assert.Error(t, err)
	})
}
