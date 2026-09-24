// Package admin renders the staff-facing admin panel (html/template +
// minimal HTMX/vanilla JS) under /admin/* — distinct from the JSON API
// already served under /admin/api/* by internal/staff, internal/points and
// internal/httpapi's admin_*.go handlers (Wave 3). Task 1 (Foundation)
// wires the login screen, app shell (sidebar/header), HTML-appropriate
// auth-gate, shared toast/confirm-modal partials, and one stub page per
// remaining screen; Tasks 2-5 fill each stub screen in without needing to
// touch layout, routing, or the auth-gate set up here — mirroring how
// internal/web's Task 1 set up the public storefront's own shell.
package admin

import (
	"fmt"
	"html/template"
	"net/http"
	"path/filepath"

	"github.com/Nikemas/cozy_backend/internal/staff"
)

const templatesDir = "admin/templates"

// screenPages maps each admin screen to its content template file. "login"
// and "no_access" render without the sidebar/header chrome (PageData.
// ShowSidebar = false); the rest render inside the full app shell. Task
// 2-5 don't add new entries here — per the Wave 4 plan they replace an
// existing stub .gohtml's content, not the routing/screen set itself.
var screenPages = map[string]string{
	"login":          "login.gohtml",
	"no_access":      "no_access.gohtml",
	"orders":         "orders.gohtml",
	"order_detail":   "order_detail.gohtml", // Task 3: /admin/orders/{id}, not in the sidebar's navDefs — reached only by drilling into the orders list.
	"products":       "products.gohtml",
	"product_form":   "product_form.gohtml",
	"product_import": "product_import.gohtml",
	"reports":        "reports.gohtml",
	"points":         "points.gohtml",
	"staff":          "staff.gohtml",
	"categories":     "categories.gohtml",
	"delivery":       "delivery.gohtml",
	"stock":          "stock.gohtml",      // fix/admin: per-point stock screen (point_staff's catalog view)
	"broadcasts":     "broadcasts.gohtml", // W2 fix/promo-push: Рассылки (promo push)
}

// layoutPartials are parsed alongside every screen: the shared app-shell
// chrome (sidebar/header) plus the toast and generic confirm-modal
// fragments Tasks 2-5 reuse for their own prompts.
var layoutPartials = []string{
	"layout.gohtml",
	"_sidebar.gohtml",
	"_header.gohtml",
	"_toast.gohtml",
	"_confirm_modal.gohtml",
}

// NavItem is one already-RBAC-filtered, already-ordered sidebar entry for
// the current staff member — see nav.go's navItemsForRole. _sidebar.gohtml
// just ranges over these; it doesn't know about roles at all.
type NavItem struct {
	Key      string
	Label    string
	IconPath string // SVG <path d="...">, copied verbatim from the design canvas
	URL      string
	Active   bool
}

// PageData is the payload every admin page template renders against.
// Screens wire their own content through Data; everything else backs the
// shared layout/sidebar/header/toast/modal state.
type PageData struct {
	Screen      string // "login", "no_access", "orders", "order_detail", "products", "reports", "points", "staff", "categories"
	PageTitle   string
	ShowSidebar bool // false only for "login"/"no_access", which have no chrome
	ShowBack    bool
	ShowSearch  bool
	SearchQuery string

	// Staff/Initials/RoleLabel/NavItems back the sidebar footer + nav —
	// nil/empty on the login screen (no session yet).
	Staff     *staff.Staff
	Initials  string
	RoleLabel string
	NavItems  []NavItem

	// Login screen state (design canvas's phone/err bindings).
	Phone string
	Err   string

	Toast string

	Data any
}

// Renderer holds one parsed template set per screen, built once at startup
// (RegisterRoutes) so a broken .gohtml file fails fast there instead of
// mid-request.
type Renderer struct {
	tmpl map[string]*template.Template
}

// NewRenderer parses every screen's templates. Template paths are relative
// to the process's working directory (admin/templates/...), matching how
// internal/web's NewRenderer already resolves web/templates/... — both
// assume cmd/server runs from the repo root.
func NewRenderer() (*Renderer, error) {
	rr := &Renderer{tmpl: map[string]*template.Template{}}

	for screen, page := range screenPages {
		files := make([]string, 0, len(layoutPartials)+1)
		for _, p := range layoutPartials {
			files = append(files, filepath.Join(templatesDir, p))
		}
		files = append(files, filepath.Join(templatesDir, page))

		t, err := template.New("layout.gohtml").ParseFiles(files...)
		if err != nil {
			return nil, fmt.Errorf("admin: parsing templates for screen %q: %w", screen, err)
		}
		rr.tmpl[screen] = t
	}

	return rr, nil
}

// Render executes the "layout" template for screen using data.
func (rr *Renderer) Render(w http.ResponseWriter, screen string, data PageData) error {
	t, ok := rr.tmpl[screen]
	if !ok {
		return fmt.Errorf("admin: no template registered for screen %q", screen)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	return t.ExecuteTemplate(w, "layout", data)
}
