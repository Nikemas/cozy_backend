package httpapi

import (
	"context"
	"database/sql"
	"net/http"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/auth"
	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/config"
	"github.com/Nikemas/cozy_backend/internal/orders"
	"github.com/Nikemas/cozy_backend/internal/payments"
)

// orderService is the subset of *orders.Service the order handlers depend
// on, so handler-level tests can inject a fake instead of a live database —
// mirrors stockUpserter in admin_catalog.go.
type orderService interface {
	PlaceOrder(ctx context.Context, in orders.PlaceOrderInput) (*orders.Order, bool, error)
	ListOrders(ctx context.Context, customerID string) ([]orders.Order, error)
	GetOrder(ctx context.Context, customerID, orderID string) (*orders.Order, error)
	CancelByCustomer(ctx context.Context, customerID, orderID string) (*orders.Order, error)
}

// onlineCheckout is the subset of *payments.Service createOrderHandler
// (payment_method = online_card) and payOrderHandler (retry) need.
type onlineCheckout interface {
	PlaceOnlineOrder(ctx context.Context, in orders.PlaceOrderInput) (*orders.Order, string, bool, error)
	RetryPayment(ctx context.Context, customerID, orderID string) (string, error)
	GetPaymentQR(ctx context.Context, customerID, orderID string) (qrLink, qrImage string, err error)
}

// cartService is the subset of *orders.CartRepo the cart handlers depend
// on, for the same reason as orderService above.
type cartService interface {
	ListDetailed(ctx context.Context, customerID string) ([]orders.CartLine, error)
	Add(ctx context.Context, customerID, variantID string, qty int) error
	UpdateQty(ctx context.Context, customerID, variantID string, qty int) error
	Remove(ctx context.Context, customerID, variantID string) error
}

// cartImageGetter is the subset of *catalog.ImageRepo listCartHandler needs
// — one batch call for every product in the cart, not one call per line.
type cartImageGetter interface {
	PrimaryForProducts(ctx context.Context, productIDs []string) (map[string]catalog.ProductImage, error)
}

// IdempotencyKeyHeader is the optional header on POST /api/v1/orders that
// makes a retried checkout return the first attempt's order.
const IdempotencyKeyHeader = "Idempotency-Key"

// RegisterOrderRoutes mounts the customer-facing order and cart endpoints
// under /api/v1/*, per §6 of the ТЗ (Flutter mobile app JSON API — orders
// history/placement) and the cart doc comment on orders.CartItem (server-
// side cart shared between web and mobile for the same logged-in
// customer). Every route here is behind authSvc.RequireCustomer.
//
// Called from cmd/server/main.go's registerAPIRoutes, alongside
// httpapi.RegisterCatalogRoutes(...).
//
// paySvc handles payment_method = online_card; nil disables online orders.
func RegisterOrderRoutes(mux *http.ServeMux, db *sql.DB, authSvc *auth.Service, cfg *config.Config, ordersSvc *orders.Service, paySvc *payments.Service) {
	cartRepo := orders.NewCartRepo(db)
	images := catalog.NewImageRepo(db)

	requireCustomer := authSvc.RequireCustomer

	var checkout onlineCheckout
	if paySvc != nil {
		checkout = paySvc
	}
	mux.Handle("POST /api/v1/orders", requireCustomer(apperr.Wrap(createOrderHandler(ordersSvc, checkout))))
	mux.Handle("GET /api/v1/orders", requireCustomer(apperr.Wrap(listOrdersHandler(ordersSvc))))
	mux.Handle("GET /api/v1/orders/{id}", requireCustomer(apperr.Wrap(getOrderHandler(ordersSvc))))
	mux.Handle("POST /api/v1/orders/{id}/cancel", requireCustomer(apperr.Wrap(cancelOrderHandler(ordersSvc))))
	mux.Handle("POST /api/v1/orders/{id}/pay", requireCustomer(apperr.Wrap(payOrderHandler(checkout))))
	mux.Handle("POST /api/v1/orders/{id}/pay/qr", requireCustomer(apperr.Wrap(payOrderQRHandler(checkout))))

	// Public: the checkout's delivery-zone picker (no auth needed).
	mux.Handle("GET /api/v1/delivery-zones", apperr.Wrap(listDeliveryZonesHandler(orders.NewDeliveryZoneRepo(db))))

	mux.Handle("GET /api/v1/cart", requireCustomer(apperr.Wrap(listCartHandler(cartRepo, images, cfg))))
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
// is enforced by orders.Service itself
// (apperr.BadRequest("invalid_fulfillment", ...)), so this handler just
// passes both through unvalidated beyond what decodeJSON already gives us.
type createOrderRequest struct {
	Items         []orderItemRequest `json:"items"`
	AddressID     *string            `json:"address_id"`
	PickupPointID *string            `json:"pickup_point_id"`
	// DeliveryZoneID is the delivery zone (from GET /api/v1/delivery-zones)
	// for a delivery order — required while any zone is active; ignored
	// for pickup. The fee is computed server-side from it.
	DeliveryZoneID *string `json:"delivery_zone_id"`
	// PaymentMethod is "cash_on_delivery" (default when omitted, the
	// pre-Task-S behavior) or "online_card".
	PaymentMethod string `json:"payment_method"`
	// Comment is an optional note to the store (≤ 500 characters).
	Comment string `json:"comment"`
}

// createOrderResponse is the order plus, for online_card, the URL the app
// must open (WebView/browser) for the customer to pay.
type createOrderResponse struct {
	*orders.Order
	PaymentURL string `json:"payment_url,omitempty"`
}

func (req createOrderRequest) toInput(customerID, idempotencyKey string) orders.PlaceOrderInput {
	items := make([]orders.OrderItemInput, len(req.Items))
	for i, it := range req.Items {
		items[i] = orders.OrderItemInput{VariantID: it.VariantID, Quantity: it.Quantity}
	}
	return orders.PlaceOrderInput{
		CustomerID:     customerID,
		Items:          items,
		AddressID:      req.AddressID,
		PickupPointID:  req.PickupPointID,
		DeliveryZoneID: req.DeliveryZoneID,
		Comment:        req.Comment,
		IdempotencyKey: idempotencyKey,
	}
}

// createOrderHandler serves POST /api/v1/orders. With an Idempotency-Key
// header, a retry of the same checkout (same customer, same key, within
// 24h) returns the order the first attempt created with 200 instead of
// 201, and creates nothing.
func createOrderHandler(svc orderService, checkout onlineCheckout) apperr.HandlerFunc {
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
		in := req.toInput(customerID, r.Header.Get(IdempotencyKeyHeader))

		switch orders.PaymentMethod(req.PaymentMethod) {
		case "", orders.PaymentCashOnDelivery:
			order, created, err := svc.PlaceOrder(r.Context(), in)
			if err != nil {
				return err
			}
			return writeJSON(w, createdStatus(created), createOrderResponse{Order: order})
		case orders.PaymentOnlineCard:
			if checkout == nil {
				return payments.ErrNotConfigured
			}
			order, paymentURL, created, err := checkout.PlaceOnlineOrder(r.Context(), in)
			if err != nil {
				return err
			}
			return writeJSON(w, createdStatus(created), createOrderResponse{Order: order, PaymentURL: paymentURL})
		default:
			return apperr.BadRequest("invalid_payment_method", "неизвестный способ оплаты")
		}
	}
}

// createdStatus is 201 for a new order, 200 for an idempotent replay.
func createdStatus(created bool) int {
	if created {
		return http.StatusCreated
	}
	return http.StatusOK
}

// payOrderResponse is the POST /api/v1/orders/{id}/pay body.
type payOrderResponse struct {
	PaymentURL string `json:"payment_url"`
}

// payOrderHandler serves POST /api/v1/orders/{id}/pay: a new payment
// attempt on the customer's own online_card order that is not cancelled
// and whose payment is pending or failed — 200 {"payment_url"}; otherwise
// 409 payment_not_retryable (404 for someone else's order).
func payOrderHandler(checkout onlineCheckout) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		customerID, ok := auth.CustomerIDFromContext(r.Context())
		if !ok {
			return apperr.Unauthorized("unauthenticated", "требуется вход в систему")
		}
		if checkout == nil {
			return payments.ErrNotConfigured
		}
		url, err := checkout.RetryPayment(r.Context(), customerID, r.PathValue("id"))
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusOK, payOrderResponse{PaymentURL: url})
	}
}

// payOrderQRResponse is the POST /api/v1/orders/{id}/pay/qr body.
type payOrderQRResponse struct {
	QRLink  string `json:"qr_link"`
	QRImage string `json:"qr_image,omitempty"`
}

// payOrderQRHandler serves POST /api/v1/orders/{id}/pay/qr: like
// payOrderHandler, a new payment attempt on the customer's own online_card
// order, but returns a scannable QR instead of a redirect link — 501
// qr_not_configured if the active provider doesn't support QR, 409
// payment_not_retryable for the same reasons /pay would refuse.
func payOrderQRHandler(checkout onlineCheckout) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		customerID, ok := auth.CustomerIDFromContext(r.Context())
		if !ok {
			return apperr.Unauthorized("unauthenticated", "требуется вход в систему")
		}
		if checkout == nil {
			return payments.ErrNotConfigured
		}
		link, image, err := checkout.GetPaymentQR(r.Context(), customerID, r.PathValue("id"))
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusOK, payOrderQRResponse{QRLink: link, QRImage: image})
	}
}

// cancelOrderHandler serves POST /api/v1/orders/{id}/cancel: the customer
// cancels their own order while it is still 'placed' and not paid online.
// Responds with the updated Order; 409 order_not_cancellable otherwise.
func cancelOrderHandler(svc orderService) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		customerID, ok := auth.CustomerIDFromContext(r.Context())
		if !ok {
			return apperr.Unauthorized("unauthenticated", "требуется вход в систему")
		}
		order, err := svc.CancelByCustomer(r.Context(), customerID, r.PathValue("id"))
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusOK, order)
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
// round-trip per line (product name/photo/price/size/color).
//
// Field naming: Quantity (not Qty, orders.CartItem's own Go field name) —
// every other /api/v1/* body that carries an item count uses "quantity"
// (orderItemRequest, cartQtyRequest), so this follows that convention
// instead of the domain struct's field name.
//
// Available is false when the line can't be ordered as is — the product
// was deactivated (IsActive=false) or no single store holds Quantity pairs
// (InStock is the most any one store has). The line is still listed so the
// customer sees why and can remove/adjust it; checkout rejects it with an
// error naming the product.
type cartLineResponse struct {
	VariantID     string  `json:"variant_id"`
	Quantity      int     `json:"quantity"`
	ProductID     string  `json:"product_id"`
	ProductName   string  `json:"product_name"`
	ProductNameKy string  `json:"product_name_ky"`
	Size          string  `json:"size"`
	Color         string  `json:"color"`
	Price         float64 `json:"price"`
	PhotoURL      *string `json:"photo_url"`
	ThumbURL      *string `json:"thumb_url"`
	IsActive      bool    `json:"is_active"`
	InStock       int     `json:"in_stock"`
	Available     bool    `json:"available"`
}

// listCartHandler serves GET /api/v1/cart: one joined query for every line
// (orders.CartRepo.ListDetailed) plus one batch image lookup — no per-line
// variant/product round-trips.
func listCartHandler(repo cartService, images cartImageGetter, cfg *config.Config) apperr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		customerID, ok := auth.CustomerIDFromContext(r.Context())
		if !ok {
			return apperr.Unauthorized("unauthenticated", "требуется вход в систему")
		}

		lines, err := repo.ListDetailed(r.Context(), customerID)
		if err != nil {
			return err
		}

		productIDs := make([]string, 0, len(lines))
		seenProduct := make(map[string]bool, len(lines))
		for _, l := range lines {
			if !seenProduct[l.ProductID] {
				seenProduct[l.ProductID] = true
				productIDs = append(productIDs, l.ProductID)
			}
		}
		primaryImages := map[string]catalog.ProductImage{}
		if len(productIDs) > 0 {
			primaryImages, err = images.PrimaryForProducts(r.Context(), productIDs)
			if err != nil {
				return err
			}
		}

		resp := make([]cartLineResponse, 0, len(lines))
		for _, l := range lines {
			var photoURLPtr, thumbURLPtr *string
			if img, ok := primaryImages[l.ProductID]; ok {
				url, thumb := photoURL(cfg, img.ObjectKey), thumbURL(cfg, img.ObjectKey)
				photoURLPtr, thumbURLPtr = &url, &thumb
			}
			resp = append(resp, cartLineResponse{
				VariantID:     l.VariantID,
				Quantity:      l.Qty,
				ProductID:     l.ProductID,
				ProductName:   l.ProductName,
				ProductNameKy: l.ProductNameKy,
				Size:          l.Size,
				Color:         l.Color,
				Price:         l.Price,
				PhotoURL:      photoURLPtr,
				ThumbURL:      thumbURLPtr,
				IsActive:      l.ProductActive,
				InStock:       l.InStock,
				Available:     l.Available(),
			})
		}

		return writeJSON(w, http.StatusOK, resp)
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
