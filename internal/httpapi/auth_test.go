package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/storefront"
)

type fakeAuthAPI struct {
	loggedOut  []string
	refreshErr error
}

func (f *fakeAuthAPI) RequestOTP(context.Context, string) error { return nil }
func (f *fakeAuthAPI) VerifyOTP(context.Context, string, string) (string, string, *storefront.Customer, error) {
	return "a", "r", &storefront.Customer{ID: "c1", Phone: "+996700123456"}, nil
}
func (f *fakeAuthAPI) Refresh(context.Context, string) (string, string, error) {
	if f.refreshErr != nil {
		return "", "", f.refreshErr
	}
	return "a2", "r2", nil
}
func (f *fakeAuthAPI) Logout(_ context.Context, tok string) error {
	f.loggedOut = append(f.loggedOut, tok)
	return nil
}

func TestLogoutAlways204(t *testing.T) {
	fake := &fakeAuthAPI{}
	mux := http.NewServeMux()
	registerAuthRoutes(mux, fake)

	for _, body := range []string{`{"refresh_token":"tok-1"}`, `{}`, ``, `not json`} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", strings.NewReader(body)))
		if rec.Code != http.StatusNoContent {
			t.Errorf("body %q: status %d, want 204", body, rec.Code)
		}
	}
	if len(fake.loggedOut) != 4 || fake.loggedOut[0] != "tok-1" {
		t.Errorf("Logout calls = %q", fake.loggedOut)
	}
}

func TestRefreshInvalidIs401WithCode(t *testing.T) {
	fake := &fakeAuthAPI{refreshErr: apperr.Unauthorized("refresh_invalid", "x")}
	mux := http.NewServeMux()
	registerAuthRoutes(mux, fake)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", strings.NewReader(`{"refresh_token":"t"}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"refresh_invalid"`) {
		t.Errorf("body = %s", rec.Body.String())
	}
}
