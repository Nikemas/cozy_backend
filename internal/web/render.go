package web

import (
	"fmt"
	"html/template"
	"net/http"
	"path/filepath"

	"github.com/Nikemas/cozy_backend/internal/i18n"
)

const templatesDir = "web/templates"

// screenPages maps each of the 9 screens from COZY_WEB_DESIGN.md §3 to its
// content template file.
var screenPages = map[string]string{
	"shop":      "shop.gohtml",
	"product":   "product.gohtml",
	"cart":      "cart.gohtml",
	"fav":       "fav.gohtml",
	"profile":   "profile.gohtml",
	"orders":    "orders.gohtml",
	"branches":  "branches.gohtml",
	"lang":      "lang.gohtml",
	"done":      "done.gohtml",
	"addresses": "addresses.gohtml",
}

// layoutPartials are parsed alongside every page: the shared chrome from
// COZY_WEB_DESIGN.md §2 (header/aside/footer/toast), plus Task 4's
// reusable fragments (the 3-step login card, the address list/form and
// the favorites grid) — kept here rather than per-screen so
// RenderPartial can execute any of them directly for an HTMX swap
// without a full page reload.
var layoutPartials = []string{
	"layout.gohtml",
	"_header.gohtml",
	"_aside_filters.gohtml",
	"_footer.gohtml",
	"_toast.gohtml",
	"_profile_auth.gohtml",
	"_address_list.gohtml",
	"_address_form.gohtml",
	"_fav_grid.gohtml",
}

// PageData is the payload every page template renders against. Screens
// wire their own content through Data; everything else backs the shared
// layout (header/aside/footer/toast) state.
type PageData struct {
	Lang   string
	Screen string // "shop", "product", "cart", "fav", "profile", "orders", "branches", "lang", "done"

	Authed       bool
	CustomerID   string
	CustomerName string
	MaskedPhone  string

	// CartCount/FavCount/OrdersCount back the header/aside badges.
	// Foundation leaves these at 0 — Tasks 2-5 wire real counts once
	// catalog/orders/storefront have working repos.
	CartCount   int
	FavCount    int
	OrdersCount int

	Toast string
	Data  any
}

// Renderer holds one parsed template set per (language, screen) pair,
// built once at startup so a broken .gohtml file fails fast in
// RegisterRoutes rather than mid-request.
type Renderer struct {
	bundle *i18n.Bundle
	tmpl   map[string]map[string]*template.Template // lang -> screen -> template set
}

// NewRenderer parses every screen's templates for every supported
// language.
func NewRenderer(bundle *i18n.Bundle) (*Renderer, error) {
	rr := &Renderer{bundle: bundle, tmpl: map[string]map[string]*template.Template{}}

	for _, lang := range []string{i18n.LangRU, i18n.LangKY} {
		rr.tmpl[lang] = map[string]*template.Template{}

		for screen, page := range screenPages {
			files := make([]string, 0, len(layoutPartials)+1)
			for _, p := range layoutPartials {
				files = append(files, filepath.Join(templatesDir, p))
			}
			files = append(files, filepath.Join(templatesDir, page))

			t, err := template.New("layout.gohtml").Funcs(bundle.FuncMap(lang)).ParseFiles(files...)
			if err != nil {
				return nil, fmt.Errorf("web: parsing templates for screen %q (%s): %w", screen, lang, err)
			}
			rr.tmpl[lang][screen] = t
		}
	}

	return rr, nil
}

// Render executes the "layout" template for screen using data.Lang,
// falling back to i18n.DefaultLang if data.Lang isn't recognized.
func (rr *Renderer) Render(w http.ResponseWriter, screen string, data PageData) error {
	byScreen, ok := rr.tmpl[data.Lang]
	if !ok {
		byScreen = rr.tmpl[i18n.DefaultLang]
	}
	t, ok := byScreen[screen]
	if !ok {
		return fmt.Errorf("web: no template registered for screen %q", screen)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	return t.ExecuteTemplate(w, "layout", data)
}

// T translates key into lang outside of template execution — for handlers
// that need translated copy for something other than static page content
// (e.g. a toast message set on a redirect response), so they don't have to
// hardcode Russian.
func (rr *Renderer) T(lang, key string) string {
	return rr.bundle.T(lang, key)
}

// RenderPartial executes one named template (e.g. "_profile_auth",
// "_address_list", "_fav_grid") from screen's parsed set directly,
// without the surrounding "layout" — used by HTMX handlers that swap a
// single fragment instead of reloading the whole page. name must be one
// of layoutPartials' {{define}} names.
func (rr *Renderer) RenderPartial(w http.ResponseWriter, screen, name string, data PageData) error {
	byScreen, ok := rr.tmpl[data.Lang]
	if !ok {
		byScreen = rr.tmpl[i18n.DefaultLang]
	}
	t, ok := byScreen[screen]
	if !ok {
		return fmt.Errorf("web: no template registered for screen %q", screen)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	return t.ExecuteTemplate(w, name, data)
}
