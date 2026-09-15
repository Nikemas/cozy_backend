package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

type ctxKey int

const customerIDKey ctxKey = 0

// RequireCustomer gates a mobile/JSON API handler behind a valid access
// token: `Authorization: Bearer <jwt>`. On success, the customer ID is
// available to the handler via CustomerIDFromContext.
func (s *Service) RequireCustomer(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, prefix) {
			apperr.Wrap(unauthenticated)(w, r)
			return
		}

		customerID, err := ParseAccessToken(s.jwtSecret, strings.TrimPrefix(header, prefix))
		if err != nil {
			apperr.Wrap(unauthenticated)(w, r)
			return
		}

		ctx := context.WithValue(r.Context(), customerIDKey, customerID)
		next(w, r.WithContext(ctx))
	}
}

func unauthenticated(w http.ResponseWriter, r *http.Request) error {
	return apperr.Unauthorized("unauthenticated", "требуется вход в систему")
}

// CustomerIDFromContext returns the authenticated customer ID set by
// RequireCustomer, or ok=false if the request wasn't authenticated (which
// shouldn't happen for a handler wrapped in RequireCustomer).
func CustomerIDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(customerIDKey).(string)
	return id, ok
}

// NewContextWithCustomerID attaches customerID to ctx the same way
// RequireCustomer does. It exists for handler-level tests in other packages
// (orders, storefront, favorites/addresses/devices, ...) that need to
// exercise a CustomerIDFromContext-gated handler directly, without spinning
// up a real OTP login flow or signing a JWT — mirrors staff.NewContextWithStaff.
func NewContextWithCustomerID(ctx context.Context, customerID string) context.Context {
	return context.WithValue(ctx, customerIDKey, customerID)
}
