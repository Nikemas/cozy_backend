// Checkout + done screens (Task 3 — Cart + Checkout + Done). checkout.gohtml
// is a new screen, not one of COZY_WEB_DESIGN.md's original 9 — see
// web-plan Task 3 / Open Questions for why it's minimal.
package web

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/orders"
)

// AddressView is one row of customer_addresses, read directly by SQL
// rather than through internal/storefront: that package's CRUD for
// addresses is Task 4's to build, and per web-plan Task 3's dependency
// note, reading the table directly here is the sanctioned stand-in until
// that lands — this file must not grow a permanent addresses API of its
// own.
type AddressView struct {
	ID          string
	Label       string
	AddressText string
	IsDefault   bool
}

// PointView is one row of points_of_sale — same direct-read rationale as
// AddressView, standing in for Task 5's storefront.Branches read model.
type PointView struct {
	ID      string
	Name    string
	Address string
}

// CheckoutPageData backs checkout.gohtml. DeliveryFee is what a delivery
// order adds to ItemsTotal (pickup is free) — the same value
// orders.Service charges. IdempotencyKey is a fresh token per rendered
// form: a double click or a resubmit after a network error places one
// order, not two.
type CheckoutPageData struct {
	Addresses      []AddressView
	Points         []PointView
	ItemsTotal     float64
	ItemCount      int
	DeliveryFee    float64
	IdempotencyKey string
	CommentMaxLen  int
	HasUnavailable bool
}

// DoneData backs done.gohtml.
type DoneData struct {
	OrderNumber string
	Total       float64
}

// checkoutForm renders the checkout screen: pick a delivery address or a
// pickup point, then place the order. Requires an authenticated customer
// with a non-empty cart — per web-plan Architecture Decisions there's no
// guest cart, and there's nothing to check out with an empty one.
func (h *handlers) checkoutForm(w http.ResponseWriter, r *http.Request) error {
	customerID := CustomerID(r)
	if customerID == "" {
		http.Redirect(w, r, "/profile", http.StatusSeeOther)
		return nil
	}

	items, err := h.cartRepo.List(r.Context(), customerID)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		http.Redirect(w, r, "/cart", http.StatusSeeOther)
		return nil
	}

	addresses, err := listCustomerAddresses(r.Context(), h.db, customerID)
	if err != nil {
		return err
	}
	points, err := listActivePoints(r.Context(), h.db)
	if err != nil {
		return err
	}

	cartPage, err := h.buildCartPageData(r.Context(), customerID)
	if err != nil {
		return err
	}

	data := h.base(r, "checkout")
	data.Data = CheckoutPageData{
		Addresses:      addresses,
		Points:         points,
		ItemsTotal:     cartPage.ItemsTotal,
		ItemCount:      len(items),
		DeliveryFee:    cartPage.DeliveryFee,
		IdempotencyKey: uuid.NewString(),
		CommentMaxLen:  orders.MaxCommentLen,
		HasUnavailable: cartPage.HasUnavailable,
	}
	return h.render.Render(w, "checkout", data)
}

// checkoutSubmit creates the order from the customer's current cart and,
// per the design (no payment step in this MVP — see web-plan Architecture
// Decisions), goes straight to /order/{orderNumber}/done.
//
// The ordered lines leave the cart in the same transaction as the order
// (PlaceOrderInput.ClearCart), and the form's idempotency_key makes a
// double submit land on the first order's done page instead of placing a
// second order.
func (h *handlers) checkoutSubmit(w http.ResponseWriter, r *http.Request) error {
	customerID := CustomerID(r)
	if customerID == "" {
		http.Redirect(w, r, "/profile", http.StatusSeeOther)
		return nil
	}
	if err := r.ParseForm(); err != nil {
		return apperr.BadRequest("bad_request", "некорректная форма")
	}

	var addressID, pickupPointID *string
	switch r.FormValue("fulfillment") {
	case "delivery":
		v := r.FormValue("address_id")
		if v == "" {
			return apperr.BadRequest("address_required", "выберите адрес доставки")
		}
		addressID = &v
	case "pickup":
		v := r.FormValue("point_id")
		if v == "" {
			return apperr.BadRequest("point_required", "выберите точку самовывоза")
		}
		pickupPointID = &v
	default:
		return apperr.BadRequest("fulfillment_required", "выберите способ получения")
	}

	items, err := h.cartRepo.List(r.Context(), customerID)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		// A resubmit of an already-placed checkout finds the cart empty;
		// send it to that order instead of an error.
		if key := r.FormValue("idempotency_key"); key != "" {
			if order, ok := h.findOrderByCheckoutKey(r, customerID, key); ok {
				http.Redirect(w, r, "/order/"+order.OrderNumber+"/done", http.StatusSeeOther)
				return nil
			}
		}
		return apperr.BadRequest("empty_cart", "корзина пуста")
	}

	inputs := make([]orders.OrderItemInput, len(items))
	for i, it := range items {
		inputs[i] = orders.OrderItemInput{VariantID: it.VariantID, Quantity: it.Qty}
	}

	order, _, err := h.ordersSvc.PlaceOrder(r.Context(), orders.PlaceOrderInput{
		CustomerID:     customerID,
		Items:          inputs,
		AddressID:      addressID,
		PickupPointID:  pickupPointID,
		Comment:        r.FormValue("comment"),
		IdempotencyKey: r.FormValue("idempotency_key"),
		ClearCart:      true,
	})
	if err != nil {
		return err
	}

	http.Redirect(w, r, "/order/"+order.OrderNumber+"/done", http.StatusSeeOther)
	return nil
}

// findOrderByCheckoutKey finds the order an earlier submit of the same
// checkout form (same idempotency_key) already placed.
func (h *handlers) findOrderByCheckoutKey(r *http.Request, customerID, key string) (*orders.Order, bool) {
	order, err := h.ordersSvc.FindByIdempotencyKey(r.Context(), customerID, key)
	if err != nil {
		slog.Warn("web: checkout replay lookup failed", "customer_id", customerID, "err", err)
		return nil, false
	}
	return order, order != nil
}

// cancelOrder backs the "Отменить" button on the site's order list: the
// customer cancels their own order while it's still 'placed' (and not
// paid online) — orders.Service.CancelByCustomer returns the stock.
func (h *handlers) cancelOrder(w http.ResponseWriter, r *http.Request) error {
	customerID := CustomerID(r)
	if customerID == "" {
		http.Redirect(w, r, "/profile", http.StatusSeeOther)
		return nil
	}
	if _, err := h.ordersSvc.CancelByCustomer(r.Context(), customerID, r.PathValue("orderID")); err != nil {
		var appErr *apperr.AppError
		if errors.As(err, &appErr) && appErr.Code == "order_not_cancellable" {
			http.Redirect(w, r, "/orders?cancel=failed", http.StatusSeeOther)
			return nil
		}
		return err
	}
	http.Redirect(w, r, "/orders?cancel=done", http.StatusSeeOther)
	return nil
}

// done renders the order-confirmation screen. Requires auth (like cart/
// checkout) since GetOrder is scoped to the session's customer_id.
func (h *handlers) done(w http.ResponseWriter, r *http.Request) error {
	customerID := CustomerID(r)
	if customerID == "" {
		http.Redirect(w, r, "/profile", http.StatusSeeOther)
		return nil
	}

	order, err := h.ordersSvc.GetOrder(r.Context(), customerID, r.PathValue("orderNumber"))
	if err != nil {
		return err
	}

	data := h.base(r, "done")
	data.Data = DoneData{OrderNumber: order.OrderNumber, Total: order.TotalAmount}
	return h.render.Render(w, "done", data)
}

func listCustomerAddresses(ctx context.Context, db *sql.DB, customerID string) ([]AddressView, error) {
	const q = `
		SELECT id, COALESCE(label, ''), address_text, is_default
		FROM customer_addresses
		WHERE customer_id = $1
		ORDER BY is_default DESC, created_at`

	rows, err := db.QueryContext(ctx, q, customerID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	addrs := []AddressView{}
	for rows.Next() {
		var a AddressView
		if err := rows.Scan(&a.ID, &a.Label, &a.AddressText, &a.IsDefault); err != nil {
			return nil, err
		}
		addrs = append(addrs, a)
	}
	return addrs, rows.Err()
}

func listActivePoints(ctx context.Context, db *sql.DB) ([]PointView, error) {
	const q = `SELECT id, name, address FROM points_of_sale WHERE is_active = true ORDER BY name`

	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	points := []PointView{}
	for rows.Next() {
		var p PointView
		if err := rows.Scan(&p.ID, &p.Name, &p.Address); err != nil {
			return nil, err
		}
		points = append(points, p)
	}
	return points, rows.Err()
}
