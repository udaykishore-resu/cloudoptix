package http

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// The client address CloudOptix records is not decoration. It lands in
// govern.Response.IPAddress on an approval and in audit.Record.IPAddress, and
// an approval is the moment a human takes responsibility for changing a
// customer's production infrastructure. If that address can be set by the
// caller, the audit trail can be made to name any address an attacker likes
// for the approval of a change they requested themselves.
//
// chi's middleware.RealIP rewrites r.RemoteAddr from X-Forwarded-For for every
// request, with no check on who sent it (GO-2026-5775, GO-2026-5777). That is
// safe only behind a proxy that strips and re-writes the header, and nothing
// in the deployment forces that to be true — the API is reachable directly
// inside the cluster, and a pod that can reach it can forge the header.
//
// So the header is honoured only when the immediate peer is an address the
// operator has declared a trusted proxy. With no trusted proxies configured
// the socket address is used and forwarding headers are ignored entirely,
// which is the correct default: a wrong-but-honest address is recoverable, a
// forged one is not.
//
// Traceability: REQ-SEC-007, SPEC-SEC-002, SPEC-AUD-003.

// forwardedHeaders are the headers realIPMiddleware will read, in precedence
// order, when the peer is trusted and the configured header does not match a
// more specific one.
var forwardedHeaders = []string{"X-Forwarded-For", "X-Real-IP", "True-Client-IP"}

// realIPMiddleware resolves the caller's address and writes it back to
// r.RemoteAddr so that every downstream consumer — the access log, the OTel
// span, the audit record — sees one consistent answer.
//
// It replaces chi's middleware.RealIP rather than wrapping it, because the
// vulnerability is that RealIP trusts unconditionally and there is no option
// to make it not.
func realIPMiddleware(deps Deps) func(http.Handler) http.Handler {
	header := strings.TrimSpace(deps.TrustedProxyHeader)
	trusted := deps.TrustedProxies

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// No declared header or no declared proxies means no forwarding
			// is trusted. Leave RemoteAddr as the kernel reported it.
			if header == "" || len(trusted) == 0 {
				next.ServeHTTP(w, r)
				return
			}
			peer, ok := peerAddr(r.RemoteAddr)
			if !ok || !isTrustedProxy(peer, trusted) {
				// The request did not arrive from a declared proxy, so its
				// forwarding headers carry no authority. Deliberately do not
				// strip them: a handler that wants to log what was claimed
				// can still see it, and stripping would hide an attempt.
				next.ServeHTTP(w, r)
				return
			}
			if client, found := clientFromForwarded(r, header, trusted); found {
				r.RemoteAddr = net.JoinHostPort(client.String(), peerPort(r.RemoteAddr))
			}
			next.ServeHTTP(w, r)
		})
	}
}

// clientFromForwarded extracts the originating client address from the
// forwarding headers.
//
// For X-Forwarded-For the list reads client, proxy1, proxy2 … with each hop
// appending the address it received from. The rightmost entries are therefore
// the ones our own trusted proxies added, and the client is the rightmost
// entry that is NOT itself a trusted proxy. Taking the leftmost entry — the
// obvious implementation, and the one chi uses — is precisely what makes the
// header forgeable, because the leftmost entry is whatever the original
// caller chose to send.
func clientFromForwarded(r *http.Request, configured string, trusted []netip.Prefix) (netip.Addr, bool) {
	names := forwardedHeaders
	if configured != "" {
		names = append([]string{configured}, forwardedHeaders...)
	}
	for _, name := range names {
		raw := r.Header.Get(name)
		if raw == "" {
			continue
		}
		parts := strings.Split(raw, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			addr, ok := parseForwardedAddr(parts[i])
			if !ok {
				continue
			}
			if isTrustedProxy(addr, trusted) {
				continue // one of our own hops; keep walking left
			}
			return addr, true
		}
	}
	return netip.Addr{}, false
}

// parseForwardedAddr accepts the shapes that appear in forwarding headers: a
// bare address, an address with a port, and a bracketed IPv6 literal.
func parseForwardedAddr(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return netip.Addr{}, false
	}
	s = strings.Trim(s, `"`)
	if addr, err := netip.ParseAddr(s); err == nil {
		return addr.Unmap(), true
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap(), true
	}
	if host, _, err := net.SplitHostPort(s); err == nil {
		if addr, err := netip.ParseAddr(host); err == nil {
			return addr.Unmap(), true
		}
	}
	return netip.Addr{}, false
}

// peerAddr extracts the socket peer address from r.RemoteAddr.
func peerAddr(remote string) (netip.Addr, bool) {
	if host, _, err := net.SplitHostPort(remote); err == nil {
		if addr, err := netip.ParseAddr(host); err == nil {
			return addr.Unmap(), true
		}
	}
	if addr, err := netip.ParseAddr(remote); err == nil {
		return addr.Unmap(), true
	}
	return netip.Addr{}, false
}

// peerPort returns the peer's port, or "0" when RemoteAddr carried none. The
// port is meaningless for the client behind a proxy, but keeping RemoteAddr in
// host:port shape means every existing consumer that splits it keeps working.
func peerPort(remote string) string {
	if _, port, err := net.SplitHostPort(remote); err == nil && port != "" {
		return port
	}
	return "0"
}

// isTrustedProxy reports whether an address falls inside a declared proxy
// range.
func isTrustedProxy(addr netip.Addr, trusted []netip.Prefix) bool {
	for _, p := range trusted {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// ParseTrustedProxies converts operator-supplied CIDRs or bare addresses into
// prefixes. A bare address becomes a single-host prefix, which is what an
// operator naming one load balancer expects. An unparseable entry is an error
// rather than a silent skip: a typo that quietly drops a proxy from the
// trusted set would silently change which addresses get recorded.
func ParseTrustedProxies(entries []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(entries))
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if p, err := netip.ParsePrefix(e); err == nil {
			out = append(out, p.Masked())
			continue
		}
		addr, err := netip.ParseAddr(e)
		if err != nil {
			return nil, err
		}
		addr = addr.Unmap()
		out = append(out, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return out, nil
}
