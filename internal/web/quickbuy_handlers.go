package web

import "net/http"

// quickBuy backs the "Купить" quick-buy modal (GET /quickbuy/{id}):
// opened by a shop-grid card's button and re-fetched on every size/color
// pick inside the modal (see shop.gohtml's _quickbuy_modal). There's no
// plain <a href> pointing here — only hx-get buttons/links inside a page
// that's already loaded — so unlike product()/shop(), this always
// returns the modal fragment alone, never a full page.
func (h *handlers) quickBuy(w http.ResponseWriter, r *http.Request) error {
	productID := r.PathValue("id")
	lang := h.resolveLang(r)

	qd, err := h.buildQuickBuyData(r.Context(), r.URL.Query(), lang, productID)
	if err != nil {
		return err
	}

	page := PageData{Lang: lang, Screen: "shop"}
	page.Data = qd
	return h.render.RenderPartial(w, "shop", "_quickbuy_modal", page)
}
