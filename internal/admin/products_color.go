// products_color.go is the products list's color filter (feat/product-
// code): a select of every color some variant actually has, labelled in
// the admin's language (i18n.ColorLabel), kept across category chips,
// paging, the page-size switch and bulk actions (they all carry the
// list's query string).
package admin

import (
	"net/url"

	"github.com/Nikemas/cozy_backend/internal/i18n"
)

// ColorOptionVM is one <option> of the color filter: Value is the raw
// stored color (what ?color= filters by), Label its display text.
type ColorOptionVM struct {
	Value    string
	Label    string
	Selected bool
}

// resolveProductsColor validates the requested ?color= against the known
// variant colors (an unknown color is dropped, not filtered on — it would
// only ever show an empty list) and builds the select's options, "all
// colors" first.
func resolveProductsColor(t tr, colors []string, requested string) (selected string, options []ColorOptionVM) {
	for _, c := range colors {
		if c == requested {
			selected = c
		}
	}
	options = make([]ColorOptionVM, 0, len(colors)+1)
	options = append(options, ColorOptionVM{Value: "", Label: t.T("admin.products.all_colors"), Selected: selected == ""})
	for _, c := range colors {
		options = append(options, ColorOptionVM{Value: c, Label: i18n.ColorLabel(t.Lang(), c), Selected: c == selected})
	}
	return selected, options
}

// withColorParam carries the color filter over to chip links.
func withColorParam(chips []ChipVM, color string) []ChipVM {
	if color == "" {
		return chips
	}
	out := make([]ChipVM, len(chips))
	for i, c := range chips {
		out[i] = c
		u, err := url.Parse(c.URL)
		if err != nil {
			continue
		}
		q := u.Query()
		q.Set("color", color)
		u.RawQuery = q.Encode()
		out[i].URL = u.String()
	}
	return out
}
