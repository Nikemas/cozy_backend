package main

import (
	"net/http"
	"strings"

	"github.com/Nikemas/cozy_backend/internal/config"
	"github.com/Nikemas/cozy_backend/internal/httpmw"
)

// rateLimitExemptPrefixes are static-asset trees: cheap, cached files a
// single page view fetches many of at once.
var rateLimitExemptPrefixes = []string{"/static/", "/admin/static/"}

// paymentsPrefix is where the provider webhooks live:
// POST /api/v1/payments/{provider}/callback and the Bakai alias
// POST /api/v1/payments/bakai/webhook (internal/httpapi/payments.go).
const paymentsPrefix = "/api/v1/payments/"

// rateLimitExempt reports whether r bypasses the global per-IP limit:
// health probes (deploy script, monitoring), static assets, and the
// payment provider's webhook — a bank retrying a callback from one IP must
// never be told 429 and give up on marking an order paid. The webhook is
// authenticated by token and body-capped in its handler.
func rateLimitExempt(r *http.Request) bool {
	p := r.URL.Path
	if p == "/healthz" || p == "/readyz" {
		return true
	}
	for _, prefix := range rateLimitExemptPrefixes {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return r.Method == http.MethodPost && isPaymentWebhookPath(p)
}

func isPaymentWebhookPath(p string) bool {
	rest, ok := strings.CutPrefix(p, paymentsPrefix)
	if !ok {
		return false
	}
	provider, action, ok := strings.Cut(rest, "/")
	if !ok || provider == "" {
		return false
	}
	return action == "callback" || (provider == "bakai" && action == "webhook")
}

// rateLimitMiddleware builds the global limiter from config.
func rateLimitMiddleware(rl config.RateLimit) func(http.Handler) http.Handler {
	return httpmw.RateLimit(httpmw.RateLimitConfig{RPS: rl.RPS, Burst: rl.Burst}, rateLimitExempt)
}
