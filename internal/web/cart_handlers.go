// Cart screen + HTMX endpoints for it (Task 3 — Cart + Checkout + Done).
// Checkout/done handlers live in checkout_handlers.go.
package web

import (
	"context"
	"net/http"
	"strconv"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/orders"
)

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
	// Available is false when the product was deactivated or no store has
	// Qty pairs of it; checkout will refuse the order naming this line.
	Available bool
	PhotoURL  string // thumbnail; "" → shoe icon (see photos.go)
}

// CartPageData backs cart.gohtml's authenticated state. DeliveryFee /
// GrandTotal are what a delivery order will actually be charged
// (orders.Settings.DeliveryFee, the same value orders.Service adds to
// total_amount); self-pickup is free (checkout.pickup_free).
type CartPageData struct {
	Lines          []CartLineView
	ItemsTotal     float64
	DeliveryFee    float64
	GrandTotal     float64
	HasUnavailable bool
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

// buildCartPageData loads customerID's cart with product/variant data in
// one query (orders.CartRepo.ListDetailed) and computes the summary with
// the same delivery fee orders.Service charges.
func (h *handlers) buildCartPageData(ctx context.Context, customerID string) (*CartPageData, error) {
	lines, err := h.cartRepo.ListDetailed(ctx, customerID)
	if err != nil {
		return nil, err
	}
	page := cartPageFromLines(lines, orders.CurrentSettings().DeliveryFee)
	if err := h.attachCartPhotos(ctx, page.Lines); err != nil {
		return nil, err
	}
	return page, nil
}

// cartPageFromLines is buildCartPageData's pure part (unit-tested).
func cartPageFromLines(lines []orders.CartLine, deliveryFee float64) *CartPageData {
	page := &CartPageData{Lines: []CartLineView{}}
	for _, cl := range lines {
		l := CartLineView{
			VariantID:   cl.VariantID,
			ProductName: cl.ProductName,
			Size:        cl.Size,
			Color:       cl.Color,
			Qty:         cl.Qty,
			UnitPrice:   cl.Price,
			LineTotal:   cl.Price * float64(cl.Qty),
			Available:   cl.Available(),
		}
		if !l.Available {
			page.HasUnavailable = true
		}
		page.ItemsTotal += l.LineTotal
		page.Lines = append(page.Lines, l)
	}
	if len(page.Lines) > 0 {
		page.DeliveryFee = deliveryFee
		page.GrandTotal = page.ItemsTotal + deliveryFee
	}
	return page
}
