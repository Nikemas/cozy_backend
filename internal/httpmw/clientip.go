package httpmw

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

type clientIPKey struct{}

// ClientIP resolves the real client address once per request and stores
// it in the context (ClientIPFromContext) for rate limiters and the access
// log.
//
// X-Forwarded-For is only believed when the direct peer (RemoteAddr) is
// one of trusted — in the compose deployment that is Caddy, the only
// thing that can reach the backend. The header is then walked from the
// RIGHT, skipping trusted hops, and the first untrusted address is the
// client: everything to the left of it was written by the client itself
// and can be forged at will (taking the first/leftmost entry, as the old
// code did, let anyone pick their own "IP" and dodge per-IP limits).
func ClientIP(trusted []netip.Prefix) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := resolveClientIP(r, trusted)
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), clientIPKey{}, ip)))
		})
	}
}

// ClientIPFromContext returns the client IP stored by ClientIP, or "" when
// the middleware didn't run (unit tests calling handlers directly).
func ClientIPFromContext(ctx context.Context) string {
	ip, _ := ctx.Value(clientIPKey{}).(string)
	return ip
}

// WithClientIP returns a copy of ctx carrying ip, as ClientIP would set
// it. For tests in other packages.
func WithClientIP(ctx context.Context, ip string) context.Context {
	return context.WithValue(ctx, clientIPKey{}, ip)
}

func resolveClientIP(r *http.Request, trusted []netip.Prefix) string {
	peer, ok := parseAddr(r.RemoteAddr)
	if !ok {
		return r.RemoteAddr
	}
	if !isTrusted(peer, trusted) {
		return peer.String()
	}

	hops := r.Header.Values("X-Forwarded-For")
	var all []string
	for _, h := range hops {
		all = append(all, strings.Split(h, ",")...)
	}
	for i := len(all) - 1; i >= 0; i-- {
		addr, ok := parseAddr(strings.TrimSpace(all[i]))
		if !ok {
			// Garbage in the chain: nothing to its left can be trusted
			// either, so stop at the last hop we could vouch for.
			break
		}
		if !isTrusted(addr, trusted) {
			return addr.String()
		}
		peer = addr
	}
	return peer.String()
}

// parseAddr accepts "ip", "ip:port" and "[ipv6]:port", normalizing
// IPv4-mapped IPv6 to plain IPv4.
func parseAddr(s string) (netip.Addr, bool) {
	if host, _, err := net.SplitHostPort(s); err == nil {
		s = host
	}
	addr, err := netip.ParseAddr(strings.Trim(s, "[]"))
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap().WithZone(""), true
}

func isTrusted(addr netip.Addr, trusted []netip.Prefix) bool {
	for _, p := range trusted {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}
