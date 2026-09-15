package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/auth"
	"github.com/Nikemas/cozy_backend/internal/catalog"
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

// cartVariantGetter is the subset of *catalog.VariantRepo listCartHandler
// needs to enrich a raw cart line with size/color/price-override, mirroring
// the small-interface pattern used throughout this package (e.g.
// favoriteProductGetter in favorites.go).
type cartVariantGetter interface {
	GetByID(ctx context.Context, id string) (*catalog.Variant, error)
}

// cartProductGetter is the subset of *catalog.ProductRepo listCartHandler
// needs. Deliberately GetByIDAny, not GetByID: GetByID filters
// is_active = true, which would drop a cart line whose product was
// deactivated after being added — the cart should still show what's
// already in it even if the product can no longer be newly purchased
// (rejecting it is checkout's job, via orders.Service.CreateOrder's own
// catalog lookup, not this read-only endpoint's).
type cartProductGetter interface {
	GetByIDAny(ctx context.Context, id string) (*catalog.Product, error)
}

// cartImageGetter is the subset of *catalog.ImageRepo listCartHandler needs
// — one batch call for every product in the cart, not one call per line.
type cartImageGetter interface {
	PrimaryForProducts(ctx context.Context, productIDs []string) (map[string]catalog.ProductImage, error)
}

// RegisterOrderRoutes mounts the customer-facing order and cart endpoints
// under /api/v1/*, per §6 of the ТЗ (Flutter mobile app JSON API — orders
// history/placement) and the cart doc comment on orders.CartItem (server-
// side cart shared between web and mobile for the same logged-in
// customer). Every route here is behind authSvc.RequireCustomer.
//
// Called from cmd/server/main.go's registerAPIRoutes, alongside
// httpapi.RegisterCatalogRoutes(...).
func RegisterOrderRoutes(mux *http.ServeMux, db *sql.DB, authSvc *auth.Service) {
	ordersSvc := orders.NewService(db)
	cartRepo := orders.NewCartRepo(db)
	variants := catalog.NewVariantRepo(db)
	products := catalog.NewProductRepo(db)
	images := catalog.NewImageRepo(db)

	requireCustomer := authSvc.RequireCustomer

	mux.Handle("POST /api/v1/orders", requireCustomer(apperr.Wrap(createOrderHandler(ordersSvc))))
	mux.Handle("GET /api/v1/orders", requireCustomer(apperr.Wrap(listOrdersHandler(ordersSvc))))
	mux.Handle("GET /api/v1/orders/{id}", requireCustomer(apperr.Wrap(getOrderHandler(ordersSvc))))

	mux.Handle("GET /api/v1/cart", requireCustomer(apperr.Wrap(listCartHandler(cartRepo, variants, products, images))))
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

// cartLineResponse is the wire shape of one GET /api/v1/cart line. It
// carries everything a cart screen needs to render without a follow-up
// round-trip per line (product name/photo/price/size/color) — the bare
// orders.CartItem this used to return only had customer_id/variant_id/qty/
// created_at, which forced the client into N extra GET
// /api/v1/products/{id} calls per cart render.
//
// Field naming: Quantity (not Qty, orders.CartItem's own Go field name) —
// every other /api/v1/* body that carries an item count uses "quantity"
// (orderItemRequest, cartQtyRequest), so this follows that convention
// instead of the domain struct's field name.
//
// ProductName is catalog.Product.NameRu — there's no language-negotiation
// mechanism anywhere in /api/v1/* today (see openapi.yaml's top-level
// description note on this), so this just matches catalog.Product's own
// json-tagged fields (name_ru/name_ky), both exposed here for parity.
type cartLineResponse struct {
	VariantID     string  `json:"variant_id"`
	Quantity      int     `json:"quantity"`
	ProductID     string  `json:"product_id"`
	ProductName   string  `json:"product_name"`
	ProductNameKy string  `json:"product_name_ky"`
	Size          string  `json:"size"`
	Color         string  `json:"color"`
	Price         float64 `json:"price"`
	ObjectKey     *string `json:"object_key"`
}

// listCartHandler serves GET /api/v1/cart. It enriches each raw
// orders.CartItem with the current variant/product/primary-image rows so
// the mobile app can render a cart screen from one response.
//
// A cart line whose variant or product has been hard-deleted since being
// added (variants can be hard-deleted if never ordered, see
// catalog.VariantRepo.Delete) is skipped rather than failing the whole
// request — same "stale reference to something the user picked in the
// past" shape as listFavoritesHandler in favorites.go, which skips a
// favorited product that's since gone. The alternative (failing GET /cart
// entirely) would let one dangling line make a customer's whole cart
// inaccessible until they somehow know to remove exactly that line via
// DELETE /api/v1/cart/{variantId} — worse than just not showing it.
//
// Unlike listFavoritesHandler's loop (which treats any lookup error as
// "skip"), this only skips on apperr.NotFound and propagates everything
// else — a transient DB error has no business being silently swallowed
// into "this product doesn't exist".
func listCartHandler(repo cartService, variants cartVariantGetter, products cartProductGetter, images cartImageGetter) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		customerID, ok := auth.CustomerIDFromContext(r.Context())
		if !ok {
			return apperr.Unauthorized("unauthenticated", "требуется вход в систему")
		}

		cartItems, err := repo.List(r.Context(), customerID)
		if err != nil {
			return err
		}

		type resolvedLine struct {
			item    orders.CartItem
			variant catalog.Variant
			product catalog.Product
		}

		resolved := make([]resolvedLine, 0, len(cartItems))
		for _, it := range cartItems {
			variant, err := variants.GetByID(r.Context(), it.VariantID)
			if err != nil {
				if isNotFoundErr(err) {
					continue // dangling variant reference — skip, see doc comment above
				}
				return err
			}

			product, err := products.GetByIDAny(r.Context(), variant.ProductID)
			if err != nil {
				if isNotFoundErr(err) {
					continue // dangling product reference — skip, see doc comment above
				}
				return err
			}

			resolved = append(resolved, resolvedLine{item: it, variant: *variant, product: *product})
		}

		// Batch the primary-image lookup once for every distinct product in
		// the cart, instead of once per line.
		productIDs := make([]string, 0, len(resolved))
		seenProduct := make(map[string]bool, len(resolved))
		for _, l := range resolved {
			if !seenProduct[l.product.ID] {
				seenProduct[l.product.ID] = true
				productIDs = append(productIDs, l.product.ID)
			}
		}
		primaryImages, err := images.PrimaryForProducts(r.Context(), productIDs)
		if err != nil {
			return err
		}

		resp := make([]cartLineResponse, 0, len(resolved))
		for _, l := range resolved {
			// Effective price: variant.PriceOverride if set, else the
			// product's base_price — mirrors loadVariantSnapshots in
			// internal/orders/order.go, the existing pattern for this exact
			// rule at order-creation time.
			price := l.product.BasePrice
			if l.variant.PriceOverride != nil {
				price = *l.variant.PriceOverride
			}

			var objectKey *string
			if img, ok := primaryImages[l.product.ID]; ok {
				key := img.ObjectKey
				objectKey = &key
			}

			resp = append(resp, cartLineResponse{
				VariantID:     l.item.VariantID,
				Quantity:      l.item.Qty,
				ProductID:     l.product.ID,
				ProductName:   l.product.NameRu,
				ProductNameKy: l.product.NameKy,
				Size:          l.variant.Size,
				Color:         l.variant.Color,
				Price:         price,
				ObjectKey:     objectKey,
			})
		}

		return writeJSON(w, http.StatusOK, resp)
	}
}

// isNotFoundErr reports whether err is an *apperr.AppError with a 404
// status — the signal that a variant/product referenced by a cart line no
// longer exists (hard-deleted), as opposed to an unexpected error that
// should still fail the request.
func isNotFoundErr(err error) bool {
	var appErr *apperr.AppError
	return errors.As(err, &appErr) && appErr.Status == http.StatusNotFound
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
