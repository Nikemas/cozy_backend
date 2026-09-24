package web

import (
	"bytes"
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
	// "checkout" isn't one of COZY_WEB_DESIGN.md's original 9 screens — it's
	// a new screen Task 3 adds per web-plan Task 3 (delivery-address-or-
	// pickup-point selection, not covered 1:1 by the design canvas).
	"checkout": "checkout.gohtml",
	// "error" is the branded 404/500 page (errors.go's renderHTMLError).
	"error": "error.gohtml",
	// "pay_return" is where the payment provider sends the customer back
	// (pay_handlers.go).
	"pay_return": "pay_return.gohtml",
}

// staticPages are the legal/info pages (/about, /contacts, /delivery,
// /privacy, /terms). Their long-form copy doesn't fit the flat
// one-line-per-key locales/*.yaml, so each has one content file per
// language: web/templates/pages/<name>.<lang>.gohtml, registered as
// screen "page_<name>".
var staticPages = []string{"about", "contacts", "delivery", "privacy", "terms"}

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

	// SearchQuery mirrors the shop screen's current `q` filter so
	// _header.gohtml's search box can keep showing it across
	// navigations/HTMX partial-swaps without reaching into the
	// screen-specific .Data payload (which isn't `shop`'s ShopData on
	// every other screen). Empty on every screen except shop (Task 2).
	SearchQuery string

	Toast string
	Data  any

	// NoIndex marks pages search engines must not index (errors, private
	// account screens) — layout.gohtml emits <meta name="robots">.
	NoIndex bool
	// SEO backs <title>, meta description, canonical/hreflang, OpenGraph
	// and JSON-LD (see seo.go).
	SEO SEOMeta
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

		pages := make(map[string]string, len(screenPages)+len(staticPages))
		for screen, page := range screenPages {
			pages[screen] = page
		}
		for _, name := range staticPages {
			pages["page_"+name] = filepath.Join("pages", name+"."+lang+".gohtml")
		}

		for screen, page := range pages {
			files := make([]string, 0, len(layoutPartials)+1)
			for _, p := range layoutPartials {
				files = append(files, filepath.Join(templatesDir, p))
			}
			files = append(files, filepath.Join(templatesDir, page))

			t, err := template.New("layout.gohtml").Funcs(bundle.FuncMap(lang)).Funcs(viewFuncs(bundle, lang)).ParseFiles(files...)
			if err != nil {
				return nil, fmt.Errorf("web: parsing templates for screen %q (%s): %w", screen, lang, err)
			}
			rr.tmpl[lang][screen] = t
		}
	}

	return rr, nil
}

// viewFuncs are the storefront's own template helpers, on top of
// i18n's "t".
func viewFuncs(bundle *i18n.Bundle, lang string) template.FuncMap {
	return template.FuncMap{
		// inc turns a 0-based range index into a 1-based label.
		"inc": func(i int) int { return i + 1 },
		// money formats a som amount: {{money .Total}} → "7 900 сом".
		"money": func(v float64) string { return formatAmount(v, bundle.T(lang, "common.currency")) },
		// plural picks key.one / key.few / key.many for n (Russian rules;
		// Kyrgyz nouns don't inflect for number, so ky.yaml repeats the
		// same word in all three): {{plural .Total "shop.results"}}.
		"plural": func(n int, key string) string { return bundle.T(lang, key+"."+pluralForm(n)) },
	}
}

// pluralForm is the Russian plural category of n: "one" (1, 21, 101…),
// "few" (2–4, 22–24…) or "many" (0, 5–20, 25…).
func pluralForm(n int) string {
	if n < 0 {
		n = -n
	}
	switch {
	case n%10 == 1 && n%100 != 11:
		return "one"
	case n%10 >= 2 && n%10 <= 4 && (n%100 < 12 || n%100 > 14):
		return "few"
	default:
		return "many"
	}
}

// Render executes the "layout" template for screen using data.Lang,
// falling back to i18n.DefaultLang if data.Lang isn't recognized.
func (rr *Renderer) Render(w http.ResponseWriter, screen string, data PageData) error {
	return rr.RenderStatus(w, http.StatusOK, screen, data)
}

// RenderStatus is Render with an explicit status code (the error page
// renders with 404/500). The page is executed into a buffer first, so a
// template failure mid-page surfaces as an error (and the error page)
// instead of a half-written 200 response.
func (rr *Renderer) RenderStatus(w http.ResponseWriter, status int, screen string, data PageData) error {
	byScreen, ok := rr.tmpl[data.Lang]
	if !ok {
		byScreen = rr.tmpl[i18n.DefaultLang]
	}
	t, ok := byScreen[screen]
	if !ok {
		return fmt.Errorf("web: no template registered for screen %q", screen)
	}

	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", data); err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, err := buf.WriteTo(w)
	return err
}

// T translates key into lang outside of template execution — for handlers
// that need translated copy for something other than static page content
// (e.g. a toast message set on a redirect response), so they don't have to
// hardcode Russian.
func (rr *Renderer) T(lang, key string) string {
	return rr.bundle.T(lang, key)
}

// RenderPartial executes one named template directly, without the
// surrounding "layout" wrapper — either a shared fragment from
// layoutPartials (e.g. "_profile_auth", "_address_list", "_fav_grid") or a
// `{{define}}` block inside screen's own file (e.g. a cart-stepper
// fragment in cart.gohtml). Used by HTMX endpoints that swap one fragment
// of an already-loaded page instead of doing a full page reload. name must
// match a `{{define "name"}}` block visible in screen's parsed template set.
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
