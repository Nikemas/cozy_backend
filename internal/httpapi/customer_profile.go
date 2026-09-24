package httpapi

import (
	"context"
	"database/sql"
	"net/http"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/auth"
	"github.com/Nikemas/cozy_backend/internal/storefront"
)

// customerProfileStore is the subset of *storefront.CustomerRepo this
// handler depends on, so handler-level tests can inject a fake instead of a
// live database — mirrors favoriteLister/addressStore in this package.
type customerProfileStore interface {
	GetByID(ctx context.Context, id string) (*storefront.Customer, error)
	SetName(ctx context.Context, id, name string) error
}

// registerCustomerProfileRoutes mounts the authenticated customer's own
// profile endpoints under /api/v1/customer, gated by authSvc.RequireCustomer
// — there is no anonymous access. Called from RegisterCustomerRoutes
// (customer.go), the one exported entry point for this whole file group.
func registerCustomerProfileRoutes(mux *http.ServeMux, db *sql.DB, authSvc *auth.Service) {
	customers := storefront.NewCustomerRepo(db)

	mux.Handle("GET /api/v1/customer", authSvc.RequireCustomer(apperr.Wrap(getCustomerProfileHandler(customers))))
	mux.Handle("PUT /api/v1/customer", authSvc.RequireCustomer(apperr.Wrap(updateCustomerProfileHandler(customers))))
	mux.Handle("DELETE /api/v1/customer", authSvc.RequireCustomer(apperr.Wrap(deleteCustomerHandler(authSvc))))
}

// customerDeleter is the subset of *auth.Service DELETE /api/v1/customer
// needs.
type customerDeleter interface {
	DeleteCustomer(ctx context.Context, customerID string) error
}

// deleteCustomerHandler deletes the authenticated customer's account:
// 204 on success, 409 has_active_orders while an order is in progress —
// see auth.Service.DeleteCustomer for what is removed vs anonymized.
func deleteCustomerHandler(svc customerDeleter) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		customerID, ok := auth.CustomerIDFromContext(r.Context())
		if !ok {
			return apperr.Unauthorized("unauthenticated", "требуется вход в систему")
		}
		if err := svc.DeleteCustomer(r.Context(), customerID); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
}

// customerProfileResponse is the JSON shape of the authenticated customer's
// own profile. storefront.Customer has no json tags of its own — see
// addressResponse in addresses.go for why this handler-local DTO exists
// instead of adding tags to the shared domain type — so this carries the
// snake_case naming the rest of /api/v1/* uses.
type customerProfileResponse struct {
	ID    string  `json:"id"`
	Phone string  `json:"phone"`
	Name  *string `json:"name,omitempty"`
}

func newCustomerProfileResponse(c storefront.Customer) customerProfileResponse {
	return customerProfileResponse{
		ID:    c.ID,
		Phone: c.Phone,
		Name:  c.Name,
	}
}

// getCustomerProfileHandler returns the authenticated customer's own
// profile. GetByID returning nil, nil (customer id from a valid JWT with no
// matching row) shouldn't happen, but is handled defensively as
// customer_not_found rather than a nil-pointer panic.
func getCustomerProfileHandler(customers customerProfileStore) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		customerID, ok := auth.CustomerIDFromContext(r.Context())
		if !ok {
			return apperr.Unauthorized("unauthenticated", "требуется вход в систему")
		}

		c, err := customers.GetByID(r.Context(), customerID)
		if err != nil {
			return err
		}
		if c == nil {
			return apperr.NotFound("customer_not_found", "покупатель не найден")
		}

		return writeJSON(w, http.StatusOK, newCustomerProfileResponse(*c))
	}
}

// customerProfileRequest is what PUT /api/v1/customer's body decodes into.
type customerProfileRequest struct {
	Name string `json:"name"`
}

// updateCustomerProfileHandler sets the authenticated customer's name and
// returns the updated profile in the same shape as
// getCustomerProfileHandler. Blank-name validation lives in
// storefront.CustomerRepo.SetName (apperr.BadRequest("invalid_name", ...))
// and isn't duplicated here.
func updateCustomerProfileHandler(customers customerProfileStore) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		customerID, ok := auth.CustomerIDFromContext(r.Context())
		if !ok {
			return apperr.Unauthorized("unauthenticated", "требуется вход в систему")
		}

		var req customerProfileRequest
		if err := decodeJSON(r, &req); err != nil {
			return err
		}

		if err := customers.SetName(r.Context(), customerID, req.Name); err != nil {
			return err
		}

		c, err := customers.GetByID(r.Context(), customerID)
		if err != nil {
			return err
		}
		if c == nil {
			return apperr.NotFound("customer_not_found", "покупатель не найден")
		}

		return writeJSON(w, http.StatusOK, newCustomerProfileResponse(*c))
	}
}
