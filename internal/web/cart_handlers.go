// Cart screen + HTMX endpoints for it (Task 3 — Cart + Checkout + Done).
// Checkout/done handlers live in checkout_handlers.go.
package web

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// deliveryFeeSomFlat is a flat placeholder shown on the cart summary
// before checkout — COZY_WEB_DESIGN.md §4 lists `deliveryPrice` (default
// 200) as an editable canvas prop, not a computed value. It's display-only
// here: orders.Service.CreateOrder's total_amount is the sum of item
// prices only, no delivery line, so nothing about checkout actually
// depends on this number being exact.
// TODO(delivery-fee): replace with real zone/distance-based pricing once
// that's specified — out of scope for this MVP checkout.
const deliveryFeeSomFlat = 200

// CartLineView is one row of the cart screen: a cart_items row enriched
// with the product/variant info CartItem itself doesn't carry.
type CartLineView struct {
	VariantID   string
	ProductName string
	Size        string
	Color       string
	Qty         int
	UnitPrice   float64
	LineTotal   float64
}

// CartPageData backs cart.gohtml's authenticated state.
type CartPageData struct {
	Lines         []CartLineView
	ItemsTotal    float64
	DeliveryLabel string
	GrandTotal    float64
}

// cart renders the cart screen. Per web-plan Architecture Decisions
// ("no guest cart"), an unauthenticated visitor is redirected to /profile
// rather than shown an empty cart.
func (h *handlers) cart(w http.ResponseWriter, r *http.Request) error {
	if CustomerID(r) == "" {
		http.Redirect(w, r, "/profile", http.StatusSeeOther)
		return nil
	}

	data := h.base(r, "cart")
	page, err := h.buildCartPageData(r.Context(), data.CustomerID)
	if err != nil {
		return err
	}
	data.Data = page
	return h.render.Render(w, "cart", data)
}

// cartAddItem backs "в корзину" — called from the product screen (Task 2)
// and, in principle, anywhere else that knows a variant_id. Responds with
// just the toast partial on an HTMX request (there's no #cart-page target
// outside the cart screen itself to swap into), or a plain redirect back
// to /cart for a no-JS form submit.
func (h *handlers) cartAddItem(w http.ResponseWriter, r *http.Request) error {
	customerID := CustomerID(r)
	if customerID == "" {
		return apperr.Unauthorized("unauthorized", "войдите в аккаунт")
	}
	if err := r.ParseForm(); err != nil {
		return apperr.BadRequest("bad_request", "некорректная форма")
	}

	variantID := r.FormValue("variant_id")
	qty := 1
	if v := r.FormValue("qty"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return apperr.BadRequest("invalid_qty", "некорректное количество")
		}
		qty = n
	}

	if err := h.cartRepo.Add(r.Context(), customerID, variantID, qty); err != nil {
		return err
	}

	if r.Header.Get("HX-Request") != "" {
		data := h.base(r, "cart")
		data.Toast = h.bundle.T(data.Lang, "toast.added_to_cart")
		return h.render.RenderPartial(w, "cart", "_toast", data)
	}
	http.Redirect(w, r, "/cart", http.StatusSeeOther)
	return nil
}

// cartIncrement/cartDecrement back the cart line stepper: both re-render
// just the "cart_page" fragment (cart.gohtml) so HTMX can swap it in
// without a full page reload.
func (h *handlers) cartIncrement(w http.ResponseWriter, r *http.Request) error {
	return h.cartAdjustQty(w, r, 1)
}

func (h *handlers) cartDecrement(w http.ResponseWriter, r *http.Request) error {
	return h.cartAdjustQty(w, r, -1)
}

func (h *handlers) cartAdjustQty(w http.ResponseWriter, r *http.Request, delta int) error {
	customerID := CustomerID(r)
	if customerID == "" {
		return apperr.Unauthorized("unauthorized", "войдите в аккаунт")
	}
	variantID := r.PathValue("variantID")

	items, err := h.cartRepo.List(r.Context(), customerID)
	if err != nil {
		return err
	}
	current := 0
	for _, it := range items {
		if it.VariantID == variantID {
			current = it.Qty
			break
		}
	}

	newQty := current + delta
	if newQty <= 0 {
		if err := h.cartRepo.Remove(r.Context(), customerID, variantID); err != nil {
			return err
		}
	} else if err := h.cartRepo.UpdateQty(r.Context(), customerID, variantID, newQty); err != nil {
		return err
	}

	return h.renderCartFragment(w, r)
}

func (h *handlers) renderCartFragment(w http.ResponseWriter, r *http.Request) error {
	data := h.base(r, "cart")
	page, err := h.buildCartPageData(r.Context(), data.CustomerID)
	if err != nil {
		return err
	}
	data.Data = page
	return h.render.RenderPartial(w, "cart", "cart_page", data)
}

// buildCartPageData enriches customerID's cart_items with the product
// name/size/color/price a cart line needs to display — data CartRepo.List
// alone doesn't carry (its CartItem is deliberately just the bare
// cart_items row, per Task 1's frozen contract). Queries product_variants/
// products directly rather than going through internal/catalog, which
// only exposes per-product/per-ID accessors, not a batch-by-cart lookup.
func (h *handlers) buildCartPageData(ctx context.Context, customerID string) (*CartPageData, error) {
	const q = `
		SELECT ci.variant_id, ci.qty, pv.size, pv.color, p.name_ru,
		       COALESCE(pv.price_override, p.base_price)
		FROM cart_items ci
		JOIN product_variants pv ON pv.id = ci.variant_id
		JOIN products p ON p.id = pv.product_id
		WHERE ci.customer_id = $1
		ORDER BY ci.created_at`

	rows, err := h.db.QueryContext(ctx, q, customerID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	page := &CartPageData{Lines: []CartLineView{}}
	for rows.Next() {
		var l CartLineView
		if err := rows.Scan(&l.VariantID, &l.Qty, &l.Size, &l.Color, &l.ProductName, &l.UnitPrice); err != nil {
			return nil, err
		}
		l.LineTotal = l.UnitPrice * float64(l.Qty)
		page.ItemsTotal += l.LineTotal
		page.Lines = append(page.Lines, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(page.Lines) > 0 {
		page.DeliveryLabel = fmt.Sprintf("%d сом", deliveryFeeSomFlat)
		page.GrandTotal = page.ItemsTotal + deliveryFeeSomFlat
	}
	return page, nil
}
