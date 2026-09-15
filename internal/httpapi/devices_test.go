package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// fakeDeviceTokenRegisterer is an in-memory deviceTokenRegisterer for
// tests, recording the last call so a test can assert what the handler
// passed through.
type fakeDeviceTokenRegisterer struct {
	customerID, fcmToken, platform string
	err                            error
}

func (f *fakeDeviceTokenRegisterer) Register(_ context.Context, customerID, fcmToken, platform string) error {
	f.customerID, f.fcmToken, f.platform = customerID, fcmToken, platform
	return f.err
}

func TestRegisterDeviceHandlerRegistersForAuthenticatedCustomer(t *testing.T) {
	fake := &fakeDeviceTokenRegisterer{}
	handler := apperr.Wrap(registerDeviceHandler(fake))

	body := `{"fcm_token":"token-abc","platform":"android"}`
	req := withCustomer(httptest.NewRequest(http.MethodPost, "/api/v1/devices", strings.NewReader(body)), "customer-1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if fake.customerID != "customer-1" || fake.fcmToken != "token-abc" || fake.platform != "android" {
		t.Errorf("Register called with (%q, %q, %q), want (customer-1, token-abc, android)",
			fake.customerID, fake.fcmToken, fake.platform)
	}
}

func TestRegisterDeviceHandlerRequiresAuth(t *testing.T) {
	handler := apperr.Wrap(registerDeviceHandler(&fakeDeviceTokenRegisterer{}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices", strings.NewReader(`{"fcm_token":"t","platform":"ios"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestRegisterDeviceHandlerPropagatesInvalidPlatform(t *testing.T) {
	fake := &fakeDeviceTokenRegisterer{err: apperr.BadRequest("invalid_platform", "platform должен быть ios или android")}
	handler := apperr.Wrap(registerDeviceHandler(fake))

	body := `{"fcm_token":"token-abc","platform":"windows-phone"}`
	req := withCustomer(httptest.NewRequest(http.MethodPost, "/api/v1/devices", strings.NewReader(body)), "customer-1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}
