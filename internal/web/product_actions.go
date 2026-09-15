package web

import (
	"context"
	"html/template"
	"net/http"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/orders"
)

// productAddToCart backs the product page's "в корзину" button
// (POST /product/{slug}/cart). It calls orders.CartRepo.Add by contract —
// Task 3 owns that method's real body; today it always returns a 501
// apperr, which this handler turns into a "Скоро добавим" toast instead
// of failing the request, per web-plan Task 2's coordination note.
func (h *handlers) productAddToCart(w http.ResponseWriter, r *http.Request) error {
	productID, ok := ResolveProductID(r.PathValue("slug"))
	if !ok {
		return apperr.NotFound("product_not_found", "товар не найден")
	}
	customerID := CustomerID(r)
	if customerID == "" {
		return redirectToLogin(w, r)
	}

	variantID, err := h.resolveSelectedVariant(r.Context(), productID, r)
	if err != nil {
		return err
	}

	lang := h.resolveLang(r)
	msgKey := "toast.added_to_cart"
	if err := h.cartRepo.Add(r.Context(), customerID, variantID, 1); err != nil {
		if !isNotImplemented(err) {
			return err
		}
		msgKey = "toast.coming_soon"
	}

	return renderToastFragment(w, h.bundle.T(lang, msgKey))
}

// productBuyNow backs the product page's "Заказать сразу" button
// (POST /product/{slug}/buy) — the design's instant-buy flow, which
// calls the exact same orders.Service.CreateOrder the regular cart
// checkout will use once Task 3 lands (web-plan Architecture Decisions).
// Until then CreateOrder always 501s, handled the same way as the cart
// button above.
func (h *handlers) productBuyNow(w http.ResponseWriter, r *http.Request) error {
	productID, ok := ResolveProductID(r.PathValue("slug"))
	if !ok {
		return apperr.NotFound("product_not_found", "товар не найден")
	}
	customerID := CustomerID(r)
	if customerID == "" {
		return redirectToLogin(w, r)
	}

	variantID, err := h.resolveSelectedVariant(r.Context(), productID, r)
	if err != nil {
		return err
	}

	order, err := h.ordersSvc.CreateOrder(r.Context(), customerID, []orders.OrderItemInput{
		{VariantID: variantID, Quantity: 1},
	}, nil, nil)
	if err != nil {
		if !isNotImplemented(err) {
			return err
		}
		return renderToastFragment(w, h.bundle.T(h.resolveLang(r), "toast.coming_soon"))
	}

	w.Header().Set("HX-Redirect", "/order/"+order.OrderNumber+"/done")
	w.WriteHeader(http.StatusOK)
	return nil
}

// resolveSelectedVariant looks up the exact variant a product-page POST
// refers to from its "size"/"color" form fields — the same values the
// size/color pickers already resolved server-side into hidden inputs
// (see product.gohtml's _product_detail fragment), so this never trusts
// a client-supplied variant ID directly.
func (h *handlers) resolveSelectedVariant(ctx context.Context, productID string, r *http.Request) (string, error) {
	if err := r.ParseForm(); err != nil {
		return "", apperr.BadRequest("bad_request", "некорректная форма")
	}
	size := r.FormValue("size")
	color := r.FormValue("color")
	if size == "" || color == "" {
		return "", apperr.BadRequest("variant_required", "выберите размер и цвет")
	}

	variantList, err := h.variants.ListByProduct(ctx, productID)
	if err != nil {
		return "", err
	}
	if v := findVariant(variantList, size, color); v != nil {
		return v.ID, nil
	}
	return "", apperr.NotFound("variant_not_found", "такого размера/цвета нет в наличии")
}

// redirectToLogin sends an unauthenticated visitor to /profile instead of
// adding to cart / buying — per web-plan Architecture Decisions, there's
// no guest cart in this MVP. HX-Redirect makes an HTMX request perform a
// full client-side navigation instead of swapping a fragment.
func redirectToLogin(w http.ResponseWriter, r *http.Request) error {
	if r.Header.Get("HX-Request") != "" {
		w.Header().Set("HX-Redirect", "/profile")
		w.WriteHeader(http.StatusOK)
		return nil
	}
	http.Redirect(w, r, "/profile", http.StatusSeeOther)
	return nil
}

// renderToastFragment writes the same toast markup _toast.gohtml renders
// for a full page load, as a standalone HTML fragment an HTMX response
// swaps into layout.gohtml's #toast-slot mount point. Deliberately not
// routed through Renderer (which only ever executes a screen's full
// "layout" template) — this is a fixed, tiny shape that doesn't need
// html/template's per-screen parsing.
func renderToastFragment(w http.ResponseWriter, message string) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, err := w.Write([]byte(`<div class="toast"><i class="ti ti-circle-check"></i> ` +
		template.HTMLEscapeString(message) + `</div>`))
	return err
}
