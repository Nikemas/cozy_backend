package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRateLimitExempt(t *testing.T) {
	cases := []struct {
		method, path string
		want         bool
	}{
		{http.MethodGet, "/healthz", true},
		{http.MethodGet, "/readyz", true},
		{http.MethodGet, "/static/css/site.css", true},
		{http.MethodGet, "/admin/static/admin.js", true},
		{http.MethodPost, "/api/v1/payments/bakai/webhook", true},
		{http.MethodPost, "/api/v1/payments/bakai/callback", true},
		{http.MethodPost, "/api/v1/payments/mock/callback", true},

		{http.MethodGet, "/", false},
		{http.MethodGet, "/api/v1/products", false},
		{http.MethodGet, "/api/v1/products/facets", false},
		{http.MethodGet, "/catalog", false},
		{http.MethodGet, "/admin/products", false},
		{http.MethodPost, "/api/v1/auth/otp/request", false},
		// Only the provider webhook shapes are exempt, and only for POST.
		{http.MethodGet, "/api/v1/payments/bakai/webhook", false},
		{http.MethodGet, "/api/v1/payments/mock/checkout/abc", false},
		{http.MethodPost, "/api/v1/payments/mock/checkout/abc", false},
		{http.MethodPost, "/api/v1/payments/a/b/callback", false},
		{http.MethodGet, "/staticfoo", false},
		{http.MethodGet, "/healthz/extra", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, c.path, nil)
		if got := rateLimitExempt(r); got != c.want {
			t.Errorf("%s %s: exempt = %v, want %v", c.method, c.path, got, c.want)
		}
	}
}
