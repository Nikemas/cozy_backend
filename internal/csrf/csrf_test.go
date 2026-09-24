package csrf

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func newProtected() http.Handler {
	return Protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
}

func TestProtectAllowsSameOriginPost(t *testing.T) {
	h := newProtected()

	req := httptest.NewRequest(http.MethodPost, "/admin/staff", nil)
	req.Host = "cozy.kg"
	req.Header.Set("Origin", "https://cozy.kg")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestProtectAllowsSameOriginViaReferer(t *testing.T) {
	h := newProtected()

	req := httptest.NewRequest(http.MethodPost, "/admin/staff", nil)
	req.Host = "cozy.erpsystemsales.com"
	req.Header.Set("Referer", "https://cozy.erpsystemsales.com/admin/staff")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestProtectBlocksCrossSiteOrigin(t *testing.T) {
	h := newProtected()

	req := httptest.NewRequest(http.MethodPost, "/admin/staff", nil)
	req.Host = "cozy.kg"
	req.Header.Set("Origin", "https://evil.example")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestProtectBlocksMissingOriginAndReferer(t *testing.T) {
	h := newProtected()

	req := httptest.NewRequest(http.MethodPost, "/admin/staff", nil)
	req.Host = "cozy.kg"

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestProtectIgnoresGetRequests(t *testing.T) {
	h := newProtected()

	req := httptest.NewRequest(http.MethodGet, "/admin/staff", nil)
	req.Host = "cozy.kg"
	req.Header.Set("Origin", "https://evil.example")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected GET to pass through unchecked, got %d", rec.Code)
	}
}

func TestProtectIgnoresAPIPaths(t *testing.T) {
	h := newProtected()

	for _, path := range []string{"/api/v1/auth/otp/verify", "/api/v1/payments/bakai/webhook", "/api/v1/payments/bakai/callback"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Host = "cozy.kg"

		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s: expected bearer/webhook API path to pass through unchecked, got %d", path, rec.Code)
		}
	}
}

func TestProtectCoversStorefrontPosts(t *testing.T) {
	h := newProtected()

	for _, path := range []string{"/login/otp/verify", "/checkout", "/cart/items", "/logout", "/addresses/1", "/admin/api/staff"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Host = "cozy.kg"
		req.Header.Set("Origin", "https://evil.example")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s cross-site: expected 403, got %d", path, rec.Code)
		}

		req = httptest.NewRequest(http.MethodPost, path, nil)
		req.Host = "cozy.kg"
		req.Header.Set("Origin", "https://cozy.kg")
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("%s same-origin: expected 200, got %d", path, rec.Code)
		}
	}
}

func TestProtectFetchMetadataFallback(t *testing.T) {
	h := newProtected()

	for site, want := range map[string]int{"same-origin": http.StatusOK, "cross-site": http.StatusForbidden, "same-site": http.StatusForbidden} {
		req := httptest.NewRequest(http.MethodPost, "/checkout", nil)
		req.Host = "cozy.kg"
		req.Header.Set("Sec-Fetch-Site", site)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("Sec-Fetch-Site=%s: got %d, want %d", site, rec.Code, want)
		}
	}
}
