// Package httpapi holds the /api/v1/* JSON REST handlers shared by the
// Flutter app and HTMX/AJAX calls from the site.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/auth"
	"github.com/Nikemas/cozy_backend/internal/httpmw"
	"github.com/Nikemas/cozy_backend/internal/storefront"
)

// authAPI is the subset of *auth.Service the /api/v1/auth/* handlers use,
// so handler tests can inject a fake.
type authAPI interface {
	RequestOTP(ctx context.Context, phone string) error
	VerifyOTP(ctx context.Context, phone, code string) (access, refresh string, customer *storefront.Customer, err error)
	Refresh(ctx context.Context, refreshToken string) (access, refresh string, err error)
	Logout(ctx context.Context, refreshToken string) error
}

var _ authAPI = (*auth.Service)(nil)

// RegisterAuthRoutes mounts the OTP login flow under /api/v1/auth/*.
func RegisterAuthRoutes(mux *http.ServeMux, svc *auth.Service) {
	registerAuthRoutes(mux, svc)
}

func registerAuthRoutes(mux *http.ServeMux, svc authAPI) {
	mux.Handle("POST /api/v1/auth/otp/request", apperr.Wrap(func(w http.ResponseWriter, r *http.Request) error {
		var req struct {
			Phone string `json:"phone"`
		}
		if err := decodeJSON(r, &req); err != nil {
			return err
		}

		if err := svc.RequestOTP(r.Context(), req.Phone); err != nil {
			return err
		}

		return writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
	}))

	mux.Handle("POST /api/v1/auth/otp/verify", apperr.Wrap(func(w http.ResponseWriter, r *http.Request) error {
		var req struct {
			Phone string `json:"phone"`
			Code  string `json:"code"`
		}
		if err := decodeJSON(r, &req); err != nil {
			return err
		}

		access, refresh, customer, err := svc.VerifyOTP(r.Context(), req.Phone, req.Code)
		if err != nil {
			return err
		}

		return writeJSON(w, http.StatusOK, verifyOTPResponse(access, refresh, customer))
	}))

	mux.Handle("POST /api/v1/auth/refresh", apperr.Wrap(func(w http.ResponseWriter, r *http.Request) error {
		var req struct {
			RefreshToken string `json:"refresh_token"`
		}
		if err := decodeJSON(r, &req); err != nil {
			return err
		}

		access, refresh, err := svc.Refresh(r.Context(), req.RefreshToken)
		if err != nil {
			return err
		}

		return writeJSON(w, http.StatusOK, tokenPairResponse(access, refresh))
	}))

	// Logout revokes the refresh token's session. Always 204 — an unknown,
	// expired or already-revoked token (or an empty/garbled body) is not an
	// error, so the app can call it unconditionally on sign-out.
	mux.Handle("POST /api/v1/auth/logout", apperr.Wrap(func(w http.ResponseWriter, r *http.Request) error {
		var req struct {
			RefreshToken string `json:"refresh_token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		if err := svc.Logout(r.Context(), req.RefreshToken); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}))
}

func tokenPairResponse(access, refresh string) map[string]string {
	return map[string]string{
		"access_token":  access,
		"refresh_token": refresh,
	}
}

// verifyOTPResponseBody is the response shape of POST /api/v1/auth/otp/verify:
// a token pair plus the just-authenticated customer's own profile, so the
// mobile app doesn't need a second round-trip to GET /api/v1/customer right
// after login.
type verifyOTPResponseBody struct {
	AccessToken  string                  `json:"access_token"`
	RefreshToken string                  `json:"refresh_token"`
	Customer     customerProfileResponse `json:"customer"`
}

func verifyOTPResponse(access, refresh string, customer *storefront.Customer) verifyOTPResponseBody {
	var c customerProfileResponse
	if customer != nil {
		c = newCustomerProfileResponse(*customer)
	}
	return verifyOTPResponseBody{
		AccessToken:  access,
		RefreshToken: refresh,
		Customer:     c,
	}
}

func decodeJSON(r *http.Request, dst any) error {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return httpmw.ErrBodyTooLarge()
		}
		return apperr.BadRequest("bad_request", "некорректное тело запроса")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, body any) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(body)
}
