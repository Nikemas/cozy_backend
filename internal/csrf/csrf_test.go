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

func TestProtectIgnoresNonAdminPaths(t *testing.T) {
	h := newProtected()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/otp/verify", nil)
	req.Host = "cozy.kg"

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected non-admin path to pass through unchecked, got %d", rec.Code)
	}
}
