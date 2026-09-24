// Package csrf provides a minimal, configuration-free CSRF defense for
// every cookie-authenticated surface: the staff admin panel
// (staff_session) and the public storefront (cozy_session).
package csrf

import (
	"net/http"
	"net/url"
	"strings"
)

// exemptPrefix is the one path prefix NOT covered: /api/* — the mobile /
// JSON API authenticates with a bearer JWT, which a browser never attaches
// to a cross-site request on its own, and it also hosts the payment
// provider's server-to-server webhook/callback, which carries no Origin at
// all. (The staff JSON API lives under /admin/api/*, not /api/*, so it
// stays covered.)
const exemptPrefix = "/api/"

// Protect wraps next with a same-origin check on state-changing requests
// (POST/PUT/PATCH/DELETE) outside exemptPrefix: the request's Origin (or,
// failing that, Referer) header must name the same host the request was
// sent to. Both session cookies already set SameSite=Lax, which stops
// browsers attaching them to most cross-site POSTs — this is a second,
// browser-version-independent layer recommended by OWASP for cookie-based
// sessions (it also covers the login-CSRF case, where the attacker's goal
// is to plant THEIR session, which SameSite doesn't prevent). Comparing
// against the request's own Host, rather than a hardcoded allowlist of
// domains, means it needs no configuration and keeps working unchanged
// across cozy.kg, the erpsystemsales.com staging host, and local dev.
func Protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if needsCheck(r) && !sameOrigin(r) {
			http.Error(w, "cross-site request blocked", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func needsCheck(r *http.Request) bool {
	if strings.HasPrefix(r.URL.Path, exemptPrefix) {
		return false
	}
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

// sameOrigin reports whether the request's Origin (or Referer, for older
// clients that omit Origin on same-site requests) names the same host the
// request was sent to. Missing both headers (and no same-origin Fetch
// Metadata) fails closed: a genuine browser form POST or fetch() always
// sends at least one of them.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = r.Header.Get("Referer")
	}
	if origin == "" {
		// No Origin/Referer (e.g. a strict Referrer-Policy on an older
		// browser): fall back to Fetch Metadata, which the browser sets
		// itself and scripts cannot forge.
		return r.Header.Get("Sec-Fetch-Site") == "same-origin"
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}
