package httpapi

import (
	"net/http"
	"strings"

	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/i18n"
	"github.com/Nikemas/cozy_backend/internal/orders"
)

// varyHeaderLanguage is the request header language-dependent responses
// vary on — the same negotiation GET /api/v1/banner uses.
const varyHeaderLanguage = "Accept-Language"

// apiLang is a JSON API response's language: Accept-Language (ky or ru),
// else i18n.DefaultLang. Only the header counts (no cookie/query), so
// "Vary: Accept-Language" fully describes what the response depends on.
func apiLang(r *http.Request) string {
	if lang, ok := i18n.FromAcceptLanguage(r.Header.Get("Accept-Language")); ok {
		return lang
	}
	return i18n.DefaultLang
}

// writeLocalizedJSON is writeJSON for a body that depends on apiLang: it
// adds "Vary: Accept-Language" so a shared or client cache never serves a
// Kyrgyz body to a Russian request (or vice versa).
func writeLocalizedJSON(w http.ResponseWriter, status int, body any) error {
	addVary(w.Header(), varyHeaderLanguage)
	return writeJSON(w, status, body)
}

// addVary appends field to the Vary header unless it is already listed.
func addVary(h http.Header, field string) {
	for _, v := range h.Values("Vary") {
		for _, f := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(f), field) {
				return
			}
		}
	}
	h.Add("Vary", field)
}

// colorOption is one GET /api/v1/products/facets color: Value is the raw
// stored color (what ?color= filters by), Label its display text in the
// request's language (Value itself when there is no translation).
type colorOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// facetsResponse is catalog.Facets plus the localized color options;
// "colors" stays the raw list for older clients.
type facetsResponse struct {
	catalog.Facets
	ColorOptions []colorOption `json:"color_options"`
}

func newFacetsResponse(f catalog.Facets, lang string) facetsResponse {
	opts := make([]colorOption, len(f.Colors))
	for i, c := range f.Colors {
		opts[i] = colorOption{Value: c, Label: i18n.ColorLabel(lang, c)}
	}
	return facetsResponse{Facets: f, ColorOptions: opts}
}

// orderOut is an orders.Order as the customer API returns it: the same
// fields, with each item's color also given as a localized label. Items
// shadows the embedded Order.Items (the shallower field wins in
// encoding/json), so "items" appears once.
type orderOut struct {
	*orders.Order
	Items []orderItemOut `json:"items"`
}

// orderItemOut adds ColorLabel — ColorSnapshot in the request's language,
// or ColorSnapshot itself when there is no translation.
type orderItemOut struct {
	orders.OrderItem
	ColorLabel string `json:"color_label"`
}

func localizeOrder(o *orders.Order, lang string) orderOut {
	if o == nil || o.Items == nil {
		return orderOut{Order: o}
	}
	items := make([]orderItemOut, len(o.Items))
	for i, it := range o.Items {
		items[i] = orderItemOut{OrderItem: it, ColorLabel: i18n.ColorLabel(lang, it.ColorSnapshot)}
	}
	return orderOut{Order: o, Items: items}
}

func localizeOrders(list []orders.Order, lang string) []orderOut {
	if list == nil {
		return nil
	}
	out := make([]orderOut, len(list))
	for i := range list {
		out[i] = localizeOrder(&list[i], lang)
	}
	return out
}
