// Package csrf provides a minimal, configuration-free CSRF defense for the
// staff admin panel.
package csrf

import (
	"net/http"
	"net/url"
	"strings"
)

// protectedPrefix is the path prefix under which requests carry the
// cookie-based staff_session (internal/staff, internal/admin) and so need
// this check. internal/httpapi's customer/mobile routes authenticate with a
// bearer JWT instead, which a browser never attaches to a cross-site
// request on its own, so that surface is already immune to CSRF and isn't
// covered here.
const protectedPrefix = "/admin"

// Protect wraps next with a same-origin check on state-changing requests
// (POST/PUT/PATCH/DELETE) under protectedPrefix: the request's Origin (or,
// failing that, Referer) header must name the same host the request was
// sent to. staff_session already sets SameSite=Lax, which stops browsers
// attaching it to most cross-site POSTs — this is a second,
// browser-version-independent layer recommended by OWASP for cookie-based
// sessions. Comparing against the request's own Host, rather than a
// hardcoded allowlist of domains, means it needs no configuration and
// keeps working unchanged across cozy.kg, the erpsystemsales.com staging
// host, and local dev.
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
	if !strings.HasPrefix(r.URL.Path, protectedPrefix) {
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
// request was sent to. Missing both headers fails closed: a genuine browser
// form POST or fetch() always sends at least one of them.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = r.Header.Get("Referer")
	}
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}
