package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequireCustomerRejectsMissingOrBadAuth(t *testing.T) {
	svc := &Service{jwtSecret: []byte("test-secret")}

	called := false
	handler := svc.RequireCustomer(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	cases := []struct {
		name   string
		header string
	}{
		{"no header", ""},
		{"not bearer", "Basic abc123"},
		{"garbage token", "Bearer not-a-real-jwt"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			called = false
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if c.header != "" {
				req.Header.Set("Authorization", c.header)
			}
			rec := httptest.NewRecorder()

			handler(rec, req)

			if called {
				t.Error("handler should not have been called")
			}
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", rec.Code)
			}
		})
	}
}

func TestRequireCustomerAcceptsValidToken(t *testing.T) {
	secret := []byte("test-secret")
	svc := &Service{jwtSecret: secret}

	token, err := issueAccessToken(secret, "customer-123", accessTokenTTL)
	if err != nil {
		t.Fatalf("issueAccessToken: %v", err)
	}

	var gotID string
	handler := svc.RequireCustomer(func(w http.ResponseWriter, r *http.Request) {
		gotID, _ = CustomerIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotID != "customer-123" {
		t.Errorf("customer id = %q, want %q", gotID, "customer-123")
	}
}
