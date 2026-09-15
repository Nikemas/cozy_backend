package httpapi

import (
	"context"
	"database/sql"
	"net/http"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/auth"
	"github.com/Nikemas/cozy_backend/internal/orders"
)

// orderService is the subset of *orders.Service the order handlers depend
// on, so handler-level tests can inject a fake instead of a live database —
// mirrors stockUpserter in admin_catalog.go.
type orderService interface {
	CreateOrder(ctx context.Context, customerID string, items []orders.OrderItemInput, addressID, pickupPointID *string) (*orders.Order, error)
	ListOrders(ctx context.Context, customerID string) ([]orders.Order, error)
	GetOrder(ctx context.Context, customerID, orderID string) (*orders.Order, error)
}

// cartService is the subset of *orders.CartRepo the cart handlers depend
// on, for the same reason as orderService above.
type cartService interface {
	List(ctx context.Context, customerID string) ([]orders.CartItem, error)
	Add(ctx context.Context, customerID, variantID string, qty int) error
	UpdateQty(ctx context.Context, customerID, variantID string, qty int) error
	Remove(ctx context.Context, customerID, variantID string) error
}

// RegisterOrderRoutes mounts the customer-facing order and cart endpoints
// under /api/v1/*, per §6 of the ТЗ (Flutter mobile app JSON API — orders
// history/placement) and the cart doc comment on orders.CartItem (server-
// side cart shared between web and mobile for the same logged-in
// customer). Every route here is behind authSvc.RequireCustomer.
//
// Not yet wired into cmd/server/main.go's registerAPIRoutes — see the task
// report for why (that file is a concurrent-work hot spot); call this
// alongside httpapi.RegisterCatalogRoutes(...) there once it's safe to.
func RegisterOrderRoutes(mux *http.ServeMux, db *sql.DB, authSvc *auth.Service) {
	ordersSvc := orders.NewService(db)
	cartRepo := orders.NewCartRepo(db)

	requireCustomer := authSvc.RequireCustomer

	mux.Handle("POST /api/v1/orders", requireCustomer(apperr.Wrap(createOrderHandler(ordersSvc))))
	mux.Handle("GET /api/v1/orders", requireCustomer(apperr.Wrap(listOrdersHandler(ordersSvc))))
	mux.Handle("GET /api/v1/orders/{id}", requireCustomer(apperr.Wrap(getOrderHandler(ordersSvc))))

	mux.Handle("GET /api/v1/cart", requireCustomer(apperr.Wrap(listCartHandler(cartRepo))))
	mux.Handle("POST /api/v1/cart/{variantId}", requireCustomer(apperr.Wrap(addCartItemHandler(cartRepo))))
	mux.Handle("PUT /api/v1/cart/{variantId}", requireCustomer(apperr.Wrap(updateCartItemHandler(cartRepo))))
	mux.Handle("DELETE /api/v1/cart/{variantId}", requireCustomer(apperr.Wrap(removeCartItemHandler(cartRepo))))
}

// --- orders ---

// orderItemRequest is the wire shape of one line in createOrderRequest.
// Only variant_id/quantity ever come from the client — price/name/size/
// color are always recomputed server-side by orders.Service.CreateOrder.
type orderItemRequest struct {
	VariantID string `json:"variant_id"`
	Quantity  int    `json:"quantity"`
}

// createOrderRequest is the POST /api/v1/orders body. Exactly one of
// AddressID/PickupPointID must be set (delivery XOR self-pickup); that rule
// is enforced by orders.Service.CreateOrder itself
// (apperr.BadRequest("invalid_fulfillment", ...)), so this handler just
// passes both through unvalidated beyond what decodeJSON already gives us.
type createOrderRequest struct {
	Items         []orderItemRequest `json:"items"`
	AddressID     *string            `json:"address_id"`
	PickupPointID *string            `json:"pickup_point_id"`
}

func (req createOrderRequest) toItems() []orders.OrderItemInput {
	items := make([]orders.OrderItemInput, len(req.Items))
	for i, it := range req.Items {
		items[i] = orders.OrderItemInput{VariantID: it.VariantID, Quantity: it.Quantity}
	}
	return items
}

func createOrderHandler(svc orderService) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		customerID, ok := auth.CustomerIDFromContext(r.Context())
		if !ok {
			// Defensive: RequireCustomer should always have populated this.
			return apperr.Unauthorized("unauthenticated", "требуется вход в систему")
		}

		var req createOrderRequest
		if err := decodeJSON(r, &req); err != nil {
			return err
		}

		order, err := svc.CreateOrder(r.Context(), customerID, req.toItems(), req.AddressID, req.PickupPointID)
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusCreated, order)
	}
}

func listOrdersHandler(svc orderService) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		customerID, ok := auth.CustomerIDFromContext(r.Context())
		if !ok {
			return apperr.Unauthorized("unauthenticated", "требуется вход в систему")
		}

		list, err := svc.ListOrders(r.Context(), customerID)
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusOK, list)
	}
}

// getOrderHandler serves GET /api/v1/orders/{id}. {id} may be either the
// order's UUID or its human-readable order_number
// (COZY-YYYYMMDD-NNN) — orders.Service.GetOrder already handles both, so
// the path value is passed straight through.
func getOrderHandler(svc orderService) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		customerID, ok := auth.CustomerIDFromContext(r.Context())
		if !ok {
			return apperr.Unauthorized("unauthenticated", "требуется вход в систему")
		}

		order, err := svc.GetOrder(r.Context(), customerID, r.PathValue("id"))
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusOK, order)
	}
}

// --- cart ---

// cartQtyRequest is the body shape shared by POST (add) and PUT (set qty)
// cart endpoints.
type cartQtyRequest struct {
	Quantity int `json:"quantity"`
}

func listCartHandler(repo cartService) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		customerID, ok := auth.CustomerIDFromContext(r.Context())
		if !ok {
			return apperr.Unauthorized("unauthenticated", "требуется вход в систему")
		}

		items, err := repo.List(r.Context(), customerID)
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusOK, items)
	}
}

func addCartItemHandler(repo cartService) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		customerID, ok := auth.CustomerIDFromContext(r.Context())
		if !ok {
			return apperr.Unauthorized("unauthenticated", "требуется вход в систему")
		}

		var req cartQtyRequest
		if err := decodeJSON(r, &req); err != nil {
			return err
		}

		if err := repo.Add(r.Context(), customerID, r.PathValue("variantId"), req.Quantity); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
}

func updateCartItemHandler(repo cartService) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		customerID, ok := auth.CustomerIDFromContext(r.Context())
		if !ok {
			return apperr.Unauthorized("unauthenticated", "требуется вход в систему")
		}

		var req cartQtyRequest
		if err := decodeJSON(r, &req); err != nil {
			return err
		}

		if err := repo.UpdateQty(r.Context(), customerID, r.PathValue("variantId"), req.Quantity); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
}

func removeCartItemHandler(repo cartService) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		customerID, ok := auth.CustomerIDFromContext(r.Context())
		if !ok {
			return apperr.Unauthorized("unauthenticated", "требуется вход в систему")
		}

		if err := repo.Remove(r.Context(), customerID, r.PathValue("variantId")); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
}
