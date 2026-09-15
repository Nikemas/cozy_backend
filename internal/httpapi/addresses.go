package httpapi

import (
	"context"
	"database/sql"
	"net/http"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/auth"
	"github.com/Nikemas/cozy_backend/internal/storefront"
)

// addressStore is the subset of *storefront.AddressRepo these handlers
// depend on, so handler-level tests can inject a fake instead of a live
// database — mirrors stockUpserter in admin_catalog.go. Every method takes
// customerID so a customer can only ever read/write their own addresses;
// handlers here never trust an address id alone.
type addressStore interface {
	List(ctx context.Context, customerID string) ([]storefront.Address, error)
	Create(ctx context.Context, customerID string, in storefront.AddressInput) (*storefront.Address, error)
	Update(ctx context.Context, customerID, id string, in storefront.AddressInput) (*storefront.Address, error)
	Delete(ctx context.Context, customerID, id string) error
}

// registerAddressesRoutes mounts the customer delivery-address endpoints
// under /api/v1/addresses, all gated by authSvc.RequireCustomer — there is
// no anonymous access to a customer's addresses. Called from
// RegisterCustomerRoutes (customer.go), the one exported entry point for
// this whole file group.
func registerAddressesRoutes(mux *http.ServeMux, db *sql.DB, authSvc *auth.Service) {
	addresses := storefront.NewAddressRepo(db)

	mux.Handle("GET /api/v1/addresses", authSvc.RequireCustomer(apperr.Wrap(listAddressesHandler(addresses))))
	mux.Handle("POST /api/v1/addresses", authSvc.RequireCustomer(apperr.Wrap(createAddressHandler(addresses))))
	mux.Handle("PUT /api/v1/addresses/{id}", authSvc.RequireCustomer(apperr.Wrap(updateAddressHandler(addresses))))
	mux.Handle("DELETE /api/v1/addresses/{id}", authSvc.RequireCustomer(apperr.Wrap(deleteAddressHandler(addresses))))
}

// addressRequest is what the create/update JSON body decodes into.
type addressRequest struct {
	Label       *string  `json:"label"`
	AddressText string   `json:"address_text"`
	Lat         *float64 `json:"lat"`
	Lng         *float64 `json:"lng"`
	IsDefault   bool     `json:"is_default"`
}

func (req addressRequest) toInput() storefront.AddressInput {
	return storefront.AddressInput{
		Label:       req.Label,
		AddressText: req.AddressText,
		Lat:         req.Lat,
		Lng:         req.Lng,
		IsDefault:   req.IsDefault,
	}
}

// addressResponse is the JSON shape of one address in API responses.
// storefront.Address has no json tags of its own — internal/web only ever
// renders it through html/template, which reads exported Go field names
// directly and has no use for snake_case tags — so this handler-local DTO
// carries the naming convention the rest of /api/v1/* uses (see
// catalog.Product's own json tags) without adding a JSON-specific
// dependency to the shared storefront domain type.
type addressResponse struct {
	ID          string   `json:"id"`
	Label       *string  `json:"label,omitempty"`
	AddressText string   `json:"address_text"`
	Lat         *float64 `json:"lat,omitempty"`
	Lng         *float64 `json:"lng,omitempty"`
	IsDefault   bool     `json:"is_default"`
}

func newAddressResponse(a storefront.Address) addressResponse {
	return addressResponse{
		ID:          a.ID,
		Label:       a.Label,
		AddressText: a.AddressText,
		Lat:         a.Lat,
		Lng:         a.Lng,
		IsDefault:   a.IsDefault,
	}
}

func listAddressesHandler(addresses addressStore) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		customerID, ok := auth.CustomerIDFromContext(r.Context())
		if !ok {
			return apperr.Unauthorized("unauthenticated", "требуется вход в систему")
		}

		list, err := addresses.List(r.Context(), customerID)
		if err != nil {
			return err
		}
		items := make([]addressResponse, len(list))
		for i, a := range list {
			items[i] = newAddressResponse(a)
		}
		return writeJSON(w, http.StatusOK, addressListResponse{Items: items})
	}
}

type addressListResponse struct {
	Items []addressResponse `json:"items"`
}

func createAddressHandler(addresses addressStore) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		customerID, ok := auth.CustomerIDFromContext(r.Context())
		if !ok {
			return apperr.Unauthorized("unauthenticated", "требуется вход в систему")
		}

		var req addressRequest
		if err := decodeJSON(r, &req); err != nil {
			return err
		}

		a, err := addresses.Create(r.Context(), customerID, req.toInput())
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusCreated, newAddressResponse(*a))
	}
}

func updateAddressHandler(addresses addressStore) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		customerID, ok := auth.CustomerIDFromContext(r.Context())
		if !ok {
			return apperr.Unauthorized("unauthenticated", "требуется вход в систему")
		}

		var req addressRequest
		if err := decodeJSON(r, &req); err != nil {
			return err
		}

		a, err := addresses.Update(r.Context(), customerID, r.PathValue("id"), req.toInput())
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusOK, newAddressResponse(*a))
	}
}

func deleteAddressHandler(addresses addressStore) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		customerID, ok := auth.CustomerIDFromContext(r.Context())
		if !ok {
			return apperr.Unauthorized("unauthenticated", "требуется вход в систему")
		}

		if err := addresses.Delete(r.Context(), customerID, r.PathValue("id")); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
}
